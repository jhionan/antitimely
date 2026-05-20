package store_test

import (
	"context"
	"database/sql"
	"os"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/rian/antitimely/internal/store"
)

// readSchema reads schema.sql from the project root (two levels up from this test).
func readSchema(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../schema.sql")
	if err != nil {
		t.Fatalf("read schema.sql: %v", err)
	}
	return string(b)
}

func openTestDB(t *testing.T) (*store.Queries, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", "file::memory:?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.Exec(readSchema(t)); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	return store.New(db), db
}

func TestSchemaApplies(t *testing.T) {
	_, db := openTestDB(t)
	defer db.Close()
}

func TestUpsertObservation_Dedupes(t *testing.T) {
	q, db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	args := store.UpsertObservationParams{
		Source: "agent", BundleID: "", WindowTitle: "",
		BinaryName: "claude", Cwd: "/Users/rian/work/foca-api/src",
		FirstSeen: 1000,
	}
	id1, err := q.UpsertObservation(ctx, args)
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	id2, err := q.UpsertObservation(ctx, args)
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if id1 != id2 {
		t.Errorf("expected dedup: id1=%d id2=%d", id1, id2)
	}
}

func TestInsertTick_AndTotals(t *testing.T) {
	q, db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	projID, err := q.AddProject(ctx, store.AddProjectParams{Name: "foca-api", CreatedAt: 1000})
	if err != nil {
		t.Fatalf("add project: %v", err)
	}

	obsID, err := q.UpsertObservation(ctx, store.UpsertObservationParams{
		Source: "agent", BinaryName: "claude", Cwd: "/Users/rian/work/foca-api/", FirstSeen: 1000,
	})
	if err != nil {
		t.Fatalf("upsert obs: %v", err)
	}

	// 3 distinct ts × foca-api → 3 distinct ticks
	for _, ts := range []int64{1100, 1105, 1110} {
		if err := q.InsertTick(ctx, store.InsertTickParams{
			Ts: ts, ObservationID: obsID, ProjectID: sql.NullInt64{Int64: projID, Valid: true},
		}); err != nil {
			t.Fatalf("insert tick ts=%d: %v", ts, err)
		}
	}

	rows, err := q.TotalsByProject(ctx, store.TotalsByProjectParams{Ts: 0, Ts_2: 9999})
	if err != nil {
		t.Fatalf("totals: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].Name != "foca-api" || rows[0].TickCount != 3 {
		t.Errorf("got %+v, want name=foca-api count=3", rows[0])
	}
}

func TestInsertTick_SameTsDifferentObsSameProject_DistinctTsCountsOne(t *testing.T) {
	// "Same project from N sources in the same tick = 1 credit" via COUNT(DISTINCT ts).
	q, db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	projID, _ := q.AddProject(ctx, store.AddProjectParams{Name: "foca-api", CreatedAt: 1000})

	obsA, _ := q.UpsertObservation(ctx, store.UpsertObservationParams{
		Source: "agent", BinaryName: "claude", Cwd: "/Users/rian/work/foca-api/a", FirstSeen: 1000,
	})
	obsB, _ := q.UpsertObservation(ctx, store.UpsertObservationParams{
		Source: "agent", BinaryName: "claude", Cwd: "/Users/rian/work/foca-api/b", FirstSeen: 1000,
	})

	for _, obs := range []int64{obsA, obsB} {
		if err := q.InsertTick(ctx, store.InsertTickParams{
			Ts: 1100, ObservationID: obs, ProjectID: sql.NullInt64{Int64: projID, Valid: true},
		}); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	rows, _ := q.TotalsByProject(ctx, store.TotalsByProjectParams{Ts: 0, Ts_2: 9999})
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].TickCount != 1 {
		t.Errorf("expected 1 (dedup by ts), got %d", rows[0].TickCount)
	}
}
