package tasktimer

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestTimerRestartAndHistoricalAllocation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "timer.sqlite")
	open := func() (*sql.DB, *Store) {
		t.Helper()
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		timer, err := Open(ctx, db, 5)
		if err != nil {
			t.Fatal(err)
		}
		return db, timer
	}
	db, store := open()
	defer func() { db.Close() }()
	a := Task{ConnectionID: strings.Repeat("a", 32), WorkspaceID: "1", TaskID: "a", Name: "First"}
	b := Task{ConnectionID: a.ConnectionID, WorkspaceID: "1", TaskID: "b", Name: "Second"}
	if _, err := store.Switch(ctx, 109, &a); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db, store = open()
	if current, err := store.Current(ctx); err != nil || current.StartedUnix != 109 || current.TaskID != "a" {
		t.Fatalf("lost active timer: %+v %v", current, err)
	}
	if current, err := store.Switch(ctx, 111, &a); err != nil || current.StartedUnix != 109 {
		t.Fatalf("repeated start changed active timer: %+v %v", current, err)
	}
	if _, err := store.Switch(ctx, 116, &b); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Switch(ctx, 119, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		CREATE TABLE ticks (ts INTEGER, observation_id INTEGER, project_id INTEGER);
		INSERT INTO ticks VALUES (100,1,1),(100,2,1),(105,1,1),(110,1,1),(115,1,1),(120,1,1),(125,1,NULL);
	`); err != nil {
		t.Fatal(err)
	}
	sel := Selection{ProjectID: 1, StartUnix: 100, EndUnix: 130, Seconds: 12}
	preview, err := store.Preview(ctx, 130, sel, a)
	if err != nil || preview.Seconds != 12 || preview.AvailableSeconds != 15 {
		t.Fatalf("bad preview %+v %v", preview, err)
	}
	if _, err := store.Allocate(ctx, 130, sel, b, preview.Confirmation); err == nil {
		t.Fatal("confirmation should be tied to a task")
	}
	if _, err := store.Allocate(ctx, 130, sel, a, preview.Confirmation); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Allocate(ctx, 130, sel, a, preview.Confirmation); err == nil {
		t.Fatal("confirmation replay allocated time twice")
	}
	entries, err := store.Entries(ctx, 20)
	if err != nil || len(entries) != 4 {
		t.Fatalf("unexpected queue %+v %v", entries, err)
	}
	var ticks int
	if err := db.QueryRow("SELECT count(*) FROM ticks").Scan(&ticks); err != nil || ticks != 7 {
		t.Fatalf("history changed: %d %v", ticks, err)
	}
}

func TestTimedEntryRecordsUseBoundedIntervals(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "timer.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := Open(ctx, db, 5)
	if err != nil {
		t.Fatal(err)
	}
	task := Task{ConnectionID: strings.Repeat("b", 32), WorkspaceID: "1", TaskID: "a", Name: "First"}
	if _, err := store.Switch(ctx, 100, &task); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Switch(ctx, 100+86400+10, nil); err != nil {
		t.Fatal(err)
	}
	entries, err := store.Entries(ctx, 10)
	if err != nil || len(entries) != 2 || entries[1].End-entries[1].Start != 86400 || entries[0].End-entries[0].Start != 10 {
		t.Fatalf("unexpected intervals %+v %v", entries, err)
	}
}
