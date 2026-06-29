package daemon

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// TestCheckpointWALTruncatesFile reproduces the unbounded-WAL condition
// (auto-checkpoint disabled so the WAL grows like the live daemon's did) and
// asserts checkpointWAL shrinks the -wal file back to ~0.
func TestCheckpointWALTruncatesFile(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "t.sqlite")
	// wal_autocheckpoint(0) disables passive auto-checkpoint, so the WAL grows
	// without bound — exactly the daemon's failure mode, made deterministic.
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=wal_autocheckpoint(0)&_pragma=busy_timeout(2000)", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, blob TEXT)`); err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("x", 4096)
	for i := 0; i < 2000; i++ {
		if _, err := db.Exec(`INSERT INTO t(blob) VALUES (?)`, big); err != nil {
			t.Fatal(err)
		}
	}

	walPath := dbPath + "-wal"
	fi, err := os.Stat(walPath)
	if err != nil {
		t.Fatalf("wal should exist after writes: %v", err)
	}
	if fi.Size() < 1<<20 {
		t.Fatalf("expected WAL to have grown >1MB, got %d bytes", fi.Size())
	}

	if err := checkpointWAL(context.Background(), db); err != nil {
		t.Fatalf("checkpointWAL: %v", err)
	}

	// TRUNCATE either removes the -wal file or zeroes it; both are acceptable.
	if fi2, err := os.Stat(walPath); err == nil && fi2.Size() > 4096 {
		t.Fatalf("expected WAL truncated to ~0 after checkpoint, got %d bytes", fi2.Size())
	}

	// Data must survive the checkpoint (it was flushed into the main DB).
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM t`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2000 {
		t.Fatalf("expected 2000 rows after checkpoint, got %d", n)
	}
}
