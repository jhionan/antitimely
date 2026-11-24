package tasktimer

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalTimerSurvivesRestartAndStaysOffTheClickUpQueue(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "timer.sqlite")
	db, timer := openTimerStore(t, path)
	started, err := timer.StartLocal(ctx, 100, "  Plan the week  ")
	if err != nil || !started.Local || started.Name != "Plan the week" || started.StartedUnix != 100 {
		t.Fatalf("start returned %+v, %v", started, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	_, timer = openTimerStore(t, path)
	if current, err := timer.Current(ctx); err != nil || current != started {
		t.Fatalf("after reopening, current = %+v, %v", current, err)
	}
	if current, err := timer.StartLocal(ctx, 112, "Plan the week"); err != nil || current != started {
		t.Fatalf("repeated start changed timer: %+v, %v", current, err)
	}
	if _, err := timer.Switch(ctx, 99, nil); err == nil {
		t.Fatal("stop before the start succeeded")
	}
	for _, now := range []int64{130, 131} {
		if current, err := timer.Switch(ctx, now, nil); err != nil || current.StartedUnix != 0 {
			t.Fatalf("stop returned %+v, %v", current, err)
		}
	}
	entries, err := timer.Entries(ctx, 40)
	if err != nil || len(entries) != 1 || entries[0].Name != "Plan the week" || entries[0].State != "local" ||
		entries[0].Start != 100 || entries[0].End != 130 || entries[0].TaskID != "" {
		t.Fatalf("history = %+v, %v", entries, err)
	}
	if _, err := timer.Due(ctx, 150); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("local session reached the ClickUp queue: %v", err)
	}
}

func TestSwitchesBetweenLocalAndClickUpTimers(t *testing.T) {
	ctx := context.Background()
	_, timer := openTimerStore(t, filepath.Join(t.TempDir(), "timer.sqlite"))
	task := Task{ConnectionID: strings.Repeat("a", 32), WorkspaceID: "1", TaskID: "task", Name: "Review"}
	if _, err := timer.Switch(ctx, 100, &task); err != nil {
		t.Fatal(err)
	}
	if current, err := timer.StartLocal(ctx, 130, "   "); err != nil || !current.Local || current.Name != "Untitled session" {
		t.Fatalf("start without a name returned %+v, %v", current, err)
	}
	if _, err := timer.StartLocal(ctx, 140, strings.Repeat("x", 121)); err == nil {
		t.Fatal("oversized name was accepted")
	}
	if _, err := timer.Switch(ctx, 140, &Task{Name: "Incomplete ClickUp task"}); err == nil {
		t.Fatal("invalid ClickUp switch succeeded")
	}
	if current, err := timer.Switch(ctx, 160, &task); err != nil || current.Local || current.StartedUnix != 160 {
		t.Fatalf("switch to ClickUp returned %+v, %v", current, err)
	}
	entries, err := timer.Entries(ctx, 40)
	if err != nil || len(entries) != 2 || entries[0].State != "local" || entries[0].Start != 130 || entries[0].End != 160 ||
		entries[1].State != "queued" || entries[1].Start != 100 || entries[1].End != 130 {
		t.Fatalf("history = %+v, %v", entries, err)
	}
	entry, err := timer.Due(ctx, 160)
	if err != nil || entry.TaskID != task.TaskID || entry.End != 130 {
		t.Fatalf("ClickUp queue = %+v, %v", entry, err)
	}
}

func TestLocalTimerDoesNotReserveRecordedTimeForClickUp(t *testing.T) {
	ctx := context.Background()
	db, timer := openTimerStore(t, filepath.Join(t.TempDir(), "timer.sqlite"))
	if _, err := db.Exec(`
		CREATE TABLE ticks (ts INTEGER, project_id INTEGER);
		INSERT INTO ticks VALUES (100,1), (105,1), (110,1);
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := timer.StartLocal(ctx, 100, "Local work"); err != nil {
		t.Fatal(err)
	}
	task := Task{ConnectionID: strings.Repeat("a", 32), WorkspaceID: "1", TaskID: "task", Name: "Review"}
	selection := Selection{StartUnix: 100, EndUnix: 115}
	for _, running := range []bool{true, false} {
		if !running {
			if _, err := timer.Switch(ctx, 115, nil); err != nil {
				t.Fatal(err)
			}
		}
		preview, err := timer.Preview(ctx, 115, selection, task)
		if err != nil || preview.AvailableSeconds != 15 {
			t.Fatalf("local timer running=%t: allocation preview = %+v, %v", running, preview, err)
		}
	}
}

func TestFailedSwitchLeavesLocalTimerRunning(t *testing.T) {
	ctx := context.Background()
	db, timer := openTimerStore(t, filepath.Join(t.TempDir(), "timer.sqlite"))
	started, err := timer.StartLocal(ctx, 100, "In progress")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER reject_new_timer BEFORE INSERT ON clickup_timer
		BEGIN SELECT RAISE(ABORT, 'fixture rejected timer'); END`); err != nil {
		t.Fatal(err)
	}
	task := Task{ConnectionID: strings.Repeat("a", 32), WorkspaceID: "1", TaskID: "task", Name: "Review"}
	if _, err := timer.Switch(ctx, 115, &task); err == nil {
		t.Fatal("switch succeeded despite rejected write")
	}
	current, entries, err := timer.Snapshot(ctx, 40)
	if err != nil || current != started || len(entries) != 0 {
		t.Fatalf("failed switch changed the timer or history: %+v, %+v, %v", current, entries, err)
	}
}

func openTimerStore(t *testing.T, path string) (*sql.DB, *Store) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	timer, err := Open(context.Background(), db, 5)
	if err != nil {
		t.Fatal(err)
	}
	return db, timer
}
