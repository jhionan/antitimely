package tasktimer

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
)

type Selection struct {
	ProjectID int64
	StartUnix int64
	EndUnix   int64
	// Seconds = 0 selects all available time. A positive amount selects
	// the earliest available portions in the requested date range.
	Seconds int64
}

type Preview struct {
	Seconds          int64
	AvailableSeconds int64
	Confirmation     string
}

func (s *Store) Preview(ctx context.Context, now int64, selection Selection, task Task) (Preview, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Preview{}, err
	}
	defer tx.Rollback()
	preview, _, err := s.selection(ctx, tx, now, selection, task)
	return preview, err
}

// Allocate rechecks availability and confirms the same seconds and task that
// the user saw. It only writes the ClickUp outbox, leaving tracker history and
// invoice totals intact.
func (s *Store) Allocate(ctx context.Context, now int64, selection Selection, task Task, confirmation string) (Preview, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Preview{}, err
	}
	defer tx.Rollback()
	preview, spans, err := s.selection(ctx, tx, now, selection, task)
	if err != nil {
		return Preview{}, err
	}
	if preview.Seconds < 1 || confirmation == "" || preview.Confirmation != confirmation {
		return Preview{}, errors.New("available time changed or no time was selected")
	}
	if err := enqueue(ctx, tx, task, spans); err != nil {
		return Preview{}, err
	}
	return preview, tx.Commit()
}

func (s *Store) selection(ctx context.Context, tx *sql.Tx, now int64, sel Selection, task Task) (Preview, []Span, error) {
	if !task.valid() || sel.StartUnix < 1 || sel.EndUnix > now ||
		sel.StartUnix >= sel.EndUnix || sel.EndUnix-sel.StartUnix > 31*86400 ||
		sel.ProjectID < 0 || sel.Seconds < 0 {
		return Preview{}, nil, errors.New("select a task and a past time range of at most 31 days")
	}
	recorded, err := spansFromQuery(ctx, tx, `
		SELECT DISTINCT ts,ts+?1 FROM ticks
		WHERE ts > ?2-?1 AND ts < ?3 AND project_id IS NOT NULL
			AND (?4 = 0 OR project_id = ?4) ORDER BY ts`, s.interval, sel.StartUnix, sel.EndUnix, sel.ProjectID)
	if err != nil {
		return Preview{}, nil, err
	}
	reserved, err := spansFromQuery(ctx, tx, `
		SELECT start_unix,end_unix FROM clickup_outbox WHERE start_unix < ?2 AND end_unix > ?1
		UNION ALL SELECT started_unix,?2 FROM clickup_timer WHERE started_unix < ?2
		ORDER BY 1`, sel.StartUnix, sel.EndUnix)
	if err != nil {
		return Preview{}, nil, err
	}
	spans := subtract(recorded, reserved, Span{Start: sel.StartUnix, End: sel.EndUnix})
	var preview Preview
	for _, span := range spans {
		preview.AvailableSeconds += span.End - span.Start
	}
	want := preview.AvailableSeconds
	if sel.Seconds != 0 {
		if sel.Seconds > want {
			return preview, nil, nil
		}
		want = sel.Seconds
	}
	preview.Seconds = want
	var selected []Span
	for _, span := range spans {
		if want == 0 {
			break
		}
		span.End = min(span.End, span.Start+want)
		want -= span.End - span.Start
		selected = append(selected, span)
	}
	proof, err := json.Marshal(struct {
		Task  Task
		Spans []Span
	}{task, selected})
	if err != nil {
		return Preview{}, nil, err
	}
	digest := sha256.Sum256(proof)
	preview.Confirmation = hex.EncodeToString(digest[:])
	return preview, selected, nil
}

func spansFromQuery(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]Span, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Span
	for rows.Next() {
		var next Span
		if err := rows.Scan(&next.Start, &next.End); err != nil {
			return nil, err
		}
		if n := len(out); n > 0 && out[n-1].End >= next.Start {
			out[n-1].End = max(out[n-1].End, next.End)
		} else {
			out = append(out, next)
		}
	}
	return out, rows.Err()
}

func subtract(recorded, reserved []Span, window Span) []Span {
	var available []Span
	for _, span := range recorded {
		start, end := max(span.Start, window.Start), min(span.End, window.End)
		for len(reserved) > 0 && reserved[0].End <= start {
			reserved = reserved[1:]
		}
		for _, used := range reserved {
			if used.Start >= end {
				break
			}
			if start < used.Start {
				available = append(available, Span{start, used.Start})
			}
			start = max(start, used.End)
		}
		if start < end {
			available = append(available, Span{start, end})
		}
	}
	return available
}
