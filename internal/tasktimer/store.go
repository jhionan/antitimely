// Package tasktimer persists explicit timers and historical allocations in the
// daemon's local database.
package tasktimer

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"
)

type Task struct {
	ConnectionID string
	WorkspaceID  string
	TaskID       string
	Name         string
}

type Span struct {
	Start int64
	End   int64
}

type Timer struct {
	Task
	StartedUnix int64
	Local       bool
}

type Entry struct {
	Task
	Span
	ID              string
	State           string
	Message         string
	ProviderEntryID string
}

type Store struct {
	db       *sql.DB
	interval int64
}

var (
	connectionID = regexp.MustCompile(`^[a-f0-9]{32}$`)
	workspaceID  = regexp.MustCompile(`^[0-9]{1,20}$`)
	taskID       = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,120}$`)
)

func Open(ctx context.Context, db *sql.DB, interval int) (*Store, error) {
	_, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS clickup_catalog (
			singleton INTEGER PRIMARY KEY CHECK(singleton = 1),
			data BLOB NOT NULL
		) STRICT;
		CREATE TABLE IF NOT EXISTS clickup_timer (
			singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
			connection_id TEXT NOT NULL, workspace_id TEXT NOT NULL,
			task_id TEXT NOT NULL, name TEXT NOT NULL, started_unix INTEGER NOT NULL
		) STRICT;
		CREATE TABLE IF NOT EXISTS local_timer_sessions (
			id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
			name TEXT NOT NULL, start_unix INTEGER NOT NULL CHECK (start_unix > 0),
			end_unix INTEGER CHECK (end_unix > start_unix)
		) STRICT;
		CREATE UNIQUE INDEX IF NOT EXISTS local_timer_running ON local_timer_sessions((1)) WHERE end_unix IS NULL;
		CREATE INDEX IF NOT EXISTS local_timer_history ON local_timer_sessions(start_unix DESC,id) WHERE end_unix IS NOT NULL;
		CREATE TABLE IF NOT EXISTS clickup_outbox (
			id TEXT PRIMARY KEY,
			connection_id TEXT NOT NULL, workspace_id TEXT NOT NULL,
			task_id TEXT NOT NULL, name TEXT NOT NULL, start_unix INTEGER NOT NULL, end_unix INTEGER NOT NULL,
			state TEXT NOT NULL DEFAULT 'queued' CHECK (state IN ('queued','needs_review','published','failed')),
			message TEXT NOT NULL DEFAULT '', provider_entry_id TEXT NOT NULL DEFAULT '', retry_at INTEGER NOT NULL DEFAULT 0
		) STRICT;
		CREATE INDEX IF NOT EXISTS clickup_outbox_due ON clickup_outbox(retry_at) WHERE state IN ('queued','needs_review');
		CREATE INDEX IF NOT EXISTS clickup_outbox_range ON clickup_outbox(start_unix,end_unix);
	`)
	if err != nil {
		return nil, err
	}
	return &Store{db: db, interval: int64(max(1, interval))}, nil
}

func (task Task) valid() bool {
	return connectionID.MatchString(task.ConnectionID) && workspaceID.MatchString(task.WorkspaceID) &&
		taskID.MatchString(task.TaskID) && len(task.Name) > 0 && len(task.Name) <= 1024
}

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readTimer(ctx context.Context, db queryer) (Timer, error) {
	var t Timer
	err := db.QueryRowContext(ctx, `
		SELECT connection_id,workspace_id,task_id,name,started_unix,0 AS local FROM clickup_timer
		UNION ALL SELECT '','','',name,start_unix,1 FROM local_timer_sessions WHERE end_unix IS NULL
		ORDER BY local LIMIT 1`).Scan(
		&t.ConnectionID, &t.WorkspaceID, &t.TaskID, &t.Name, &t.StartedUnix, &t.Local)
	if errors.Is(err, sql.ErrNoRows) {
		return Timer{}, nil
	}
	return t, err
}

func (s *Store) Current(ctx context.Context) (Timer, error) {
	return readTimer(ctx, s.db)
}

// Switch closes the running timer and optionally starts a ClickUp task in
// the same transaction. Timers measure elapsed wall time through idle, sleep,
// and daemon restart. An exact repeat leaves the current timer running.
func (s *Store) Switch(ctx context.Context, now int64, task *Task) (Timer, error) {
	var next Timer
	if task != nil {
		if !task.valid() {
			return Timer{}, errors.New("invalid ClickUp task")
		}
		next = Timer{Task: *task, StartedUnix: now}
	}
	return s.transition(ctx, now, next)
}

// StartLocal starts a session on this device. Its time never enters the ClickUp outbox.
func (s *Store) StartLocal(ctx context.Context, now int64, name string) (Timer, error) {
	name = strings.TrimSpace(name)
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) > 120 {
		return Timer{}, errors.New("session name must be at most 120 characters")
	}
	if name == "" {
		name = "Untitled session"
	}
	return s.transition(ctx, now, Timer{Task: Task{Name: name}, StartedUnix: now, Local: true})
}

func (s *Store) transition(ctx context.Context, now int64, next Timer) (Timer, error) {
	if now < 1 {
		return Timer{}, errors.New("invalid task timer")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Timer{}, err
	}
	defer tx.Rollback()
	old, err := readTimer(ctx, tx)
	if err != nil {
		return Timer{}, err
	}
	if next.StartedUnix > 0 && old.StartedUnix > 0 && sameSession(old, next) {
		return old, nil
	}
	if old.StartedUnix > 0 {
		if now < old.StartedUnix {
			return Timer{}, errors.New("the clock is earlier than the timer start")
		}
		if err := finishTimer(ctx, tx, old, now); err != nil {
			return Timer{}, err
		}
	}
	if next.Local {
		_, err = tx.ExecContext(ctx, `INSERT INTO local_timer_sessions (name,start_unix) VALUES (?,?)`, next.Name, now)
	} else if next.StartedUnix > 0 {
		_, err = tx.ExecContext(ctx, `INSERT INTO clickup_timer VALUES (1,?,?,?,?,?)`,
			next.ConnectionID, next.WorkspaceID, next.TaskID, next.Name, now)
	}
	if err != nil {
		return Timer{}, err
	}
	return next, tx.Commit()
}

func sameSession(a, b Timer) bool {
	if a.Local || b.Local {
		return a.Local && b.Local && a.Name == b.Name
	}
	return a.ConnectionID == b.ConnectionID && a.WorkspaceID == b.WorkspaceID && a.TaskID == b.TaskID
}

func finishTimer(ctx context.Context, tx *sql.Tx, timer Timer, now int64) error {
	if timer.Local {
		if now == timer.StartedUnix {
			_, err := tx.ExecContext(ctx, `DELETE FROM local_timer_sessions WHERE end_unix IS NULL`)
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE local_timer_sessions SET end_unix=? WHERE end_unix IS NULL`, now)
		return err
	}
	if err := enqueue(ctx, tx, timer.Task, []Span{{Start: timer.StartedUnix, End: now}}); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM clickup_timer WHERE singleton = 1`)
	return err
}

func enqueue(ctx context.Context, tx *sql.Tx, task Task, spans []Span) error {
	for _, span := range spans {
		for start := span.Start; start < span.End; {
			end := min(start+24*60*60, span.End)
			_, err := tx.ExecContext(ctx, `
				INSERT INTO clickup_outbox (id,connection_id,workspace_id,task_id,name,start_unix,end_unix)
				VALUES (lower(hex(randomblob(16))),?,?,?,?,?,?)`,
				task.ConnectionID, task.WorkspaceID, task.TaskID, task.Name, start, end)
			if err != nil {
				return err
			}
			start = end
		}
	}
	return nil
}

func (s *Store) Entries(ctx context.Context, limit int) ([]Entry, error) {
	return readEntries(ctx, s.db, limit)
}

// Snapshot reads the running timer and recent history from the same transaction.
func (s *Store) Snapshot(ctx context.Context, limit int) (Timer, []Entry, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Timer{}, nil, err
	}
	defer tx.Rollback()
	timer, err := readTimer(ctx, tx)
	if err != nil {
		return Timer{}, nil, err
	}
	entries, err := readEntries(ctx, tx, limit)
	return timer, entries, err
}

func readEntries(ctx context.Context, db interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, limit int) ([]Entry, error) {
	rows, err := db.QueryContext(ctx, `
		WITH recent_clickup AS (
			SELECT id,connection_id,workspace_id,task_id,name,start_unix,end_unix,state,message,provider_entry_id
			FROM clickup_outbox ORDER BY (state = 'published'), start_unix DESC,id LIMIT ?1
		), recent_local AS (
			SELECT id,name,start_unix,end_unix FROM local_timer_sessions WHERE end_unix IS NOT NULL
			ORDER BY start_unix DESC,id LIMIT ?1
		)
		SELECT * FROM (
			SELECT * FROM recent_clickup
			UNION ALL SELECT id,'','','',name,start_unix,end_unix,'local','','' FROM recent_local
		) ORDER BY (state = 'published'), start_unix DESC,id LIMIT ?1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []Entry
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.ConnectionID, &e.WorkspaceID, &e.TaskID, &e.Name, &e.Start, &e.End, &e.State, &e.Message, &e.ProviderEntryID); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

func (s *Store) Due(ctx context.Context, now int64) (Entry, error) {
	var e Entry
	err := s.db.QueryRowContext(ctx, `
		SELECT id,connection_id,workspace_id,task_id,name,start_unix,end_unix,state
		FROM clickup_outbox WHERE state IN ('queued','needs_review') AND retry_at <= ?
		ORDER BY retry_at,start_unix,id LIMIT 1`, now).Scan(
		&e.ID, &e.ConnectionID, &e.WorkspaceID, &e.TaskID, &e.Name, &e.Start, &e.End, &e.State)
	return e, err
}

func (s *Store) SetResult(ctx context.Context, e Entry, retryAt int64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE clickup_outbox SET state=?,message=?,provider_entry_id=?,retry_at=? WHERE id=?`,
		e.State, e.Message, e.ProviderEntryID, retryAt, e.ID)
	return err
}
