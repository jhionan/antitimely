package daemon

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rian/antitimely/internal/domain"
)

// writeOpencodeDB creates an opencode.db with the v2 session tables and one
// message per (sessionID, directory, createdUnix) row. Only the columns the
// collector reads are modelled.
func writeOpencodeDB(t *testing.T, rows [][3]any) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "opencode.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, stmt := range []string{
		"CREATE TABLE session_v2 (id TEXT PRIMARY KEY, directory TEXT NOT NULL)",
		"CREATE TABLE session_message (id TEXT PRIMARY KEY, session_id TEXT NOT NULL, time_created INTEGER NOT NULL)",
		"CREATE INDEX session_message_time_created_idx ON session_message(time_created)",
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	for i, r := range rows {
		sid, dir, created := r[0].(string), r[1].(string), r[2].(int64)
		if _, err := db.Exec("INSERT OR IGNORE INTO session_v2(id, directory) VALUES(?,?)", sid, dir); err != nil {
			t.Fatal(err)
		}
		// opencode stores milliseconds.
		if _, err := db.Exec("INSERT INTO session_message(id, session_id, time_created) VALUES(?,?,?)",
			sid+"-m"+string(rune('a'+i)), sid, created*1000); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func newOpencodePipeline(t *testing.T, dbPath string, graceSec int, prefixes []string) *Pipeline {
	t.Helper()
	cache := NewCache()
	cache.Store(&CacheSnapshot{CwdPatterns: prefixes})
	return NewPipeline(nil, nil, cache, PipelineConfig{
		TranscriptTracking: true,
		TranscriptGraceSec: graceSec,
		OpencodeDB:         dbPath,
	})
}

func TestCollectOpencode_EmitsWithinGrace(t *testing.T) {
	now := time.Now().Unix()
	path := writeOpencodeDB(t, [][3]any{{"ses_1", "/work/daas", now - 71}})
	p := newOpencodePipeline(t, path, 600, []string{"/work/daas"})

	sigs := p.collectOpencodeSignals(context.Background(), p.cache.Snapshot(), now)
	if len(sigs) != 1 {
		t.Fatalf("got %d signals, want 1", len(sigs))
	}
	s := sigs[0]
	if s.Source != domain.SourceTranscript || s.Cwd != "/work/daas" || s.BinaryName != "opencode" || s.WindowTitle != "ses_1" {
		t.Fatalf("bad signal: %+v", s)
	}
}

func TestCollectOpencode_StaleBeyondGrace(t *testing.T) {
	now := time.Now().Unix()
	path := writeOpencodeDB(t, [][3]any{{"ses_1", "/work/daas", now - 7200}})
	p := newOpencodePipeline(t, path, 600, []string{"/work/daas"})

	if sigs := p.collectOpencodeSignals(context.Background(), p.cache.Snapshot(), now); len(sigs) != 0 {
		t.Fatalf("got %d signals, want 0 (stale)", len(sigs))
	}
}

func TestCollectOpencode_CwdNotTracked(t *testing.T) {
	now := time.Now().Unix()
	path := writeOpencodeDB(t, [][3]any{{"ses_1", "/work/other", now - 5}})
	p := newOpencodePipeline(t, path, 600, []string{"/work/daas"})

	if sigs := p.collectOpencodeSignals(context.Background(), p.cache.Snapshot(), now); len(sigs) != 0 {
		t.Fatalf("got %d signals, want 0 (untracked cwd)", len(sigs))
	}
}

// One signal per session however many messages it wrote, and two live
// sessions stay two signals: each is its own observation, like Claude's.
func TestCollectOpencode_OneSignalPerSession(t *testing.T) {
	now := time.Now().Unix()
	path := writeOpencodeDB(t, [][3]any{
		{"ses_1", "/work/daas", now - 60},
		{"ses_1", "/work/daas", now - 30},
		{"ses_2", "/work/daas/wt", now - 10},
	})
	p := newOpencodePipeline(t, path, 600, []string{"/work/daas"})

	sigs := p.collectOpencodeSignals(context.Background(), p.cache.Snapshot(), now)
	if len(sigs) != 2 {
		t.Fatalf("got %d signals, want 2", len(sigs))
	}
}

func TestCollectOpencode_MissingDBIsInert(t *testing.T) {
	p := newOpencodePipeline(t, filepath.Join(t.TempDir(), "absent.db"), 600, []string{"/work/daas"})
	if sigs := p.collectOpencodeSignals(context.Background(), p.cache.Snapshot(), time.Now().Unix()); len(sigs) != 0 {
		t.Fatalf("got %d signals, want 0", len(sigs))
	}
	// And no DB file may be created by merely looking for one.
	if _, err := os.Stat(filepath.Join(filepath.Dir(p.cfg.OpencodeDB), "absent.db")); err == nil {
		t.Fatal("collector created the database it was supposed to observe")
	}
}

// A database from before the v2 tables (or any schema drift) must degrade to
// "no signal", never an error that stalls the tick loop.
func TestCollectOpencode_SchemaWithoutV2TablesIsInert(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE message (id TEXT)"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	p := newOpencodePipeline(t, path, 600, []string{"/work/daas"})
	if sigs := p.collectOpencodeSignals(context.Background(), p.cache.Snapshot(), time.Now().Unix()); len(sigs) != 0 {
		t.Fatalf("got %d signals, want 0", len(sigs))
	}
}

func TestCollectOpencode_DisabledWithoutPathOrTracking(t *testing.T) {
	now := time.Now().Unix()
	path := writeOpencodeDB(t, [][3]any{{"ses_1", "/work/daas", now - 5}})

	p := newOpencodePipeline(t, "", 600, []string{"/work/daas"})
	if sigs := p.collectOpencodeSignals(context.Background(), p.cache.Snapshot(), now); len(sigs) != 0 {
		t.Fatalf("empty path: got %d signals, want 0", len(sigs))
	}
	p = newOpencodePipeline(t, path, 600, []string{"/work/daas"})
	p.cfg.TranscriptTracking = false
	if sigs := p.collectOpencodeSignals(context.Background(), p.cache.Snapshot(), now); len(sigs) != 0 {
		t.Fatalf("tracking off: got %d signals, want 0", len(sigs))
	}
}

func TestPipelineConfigCarriesOpencodeDB(t *testing.T) {
	cfg, err := DefaultConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.OpencodeDB = "/x/opencode.db"
	if got := pipelineConfigFor(cfg).OpencodeDB; got != "/x/opencode.db" {
		t.Fatalf("OpencodeDB = %q", got)
	}
}
