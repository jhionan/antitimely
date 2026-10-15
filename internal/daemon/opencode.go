package daemon

import (
	"context"
	"database/sql"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/rian/antitimely/internal/domain"
)

// opencodeRetryBackoffSec is how long a failed opencode.db read is not retried.
// A schema the collector does not understand fails identically every tick, and
// one log line per five seconds would bury the daemon log.
const opencodeRetryBackoffSec = 60

// defaultOpencodeDB is opencode's SQLite store: $XDG_DATA_HOME/opencode when
// set, else ~/.local/share/opencode (opencode uses the XDG path on macOS too).
func defaultOpencodeDB() string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "opencode", "opencode.db")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "share", "opencode", "opencode.db")
}

// collectOpencodeSignals emits a transcript signal for every opencode session
// that wrote a message within the grace window and whose directory is under a
// tracked cwd prefix.
//
// opencode keeps no per-session transcript file to tail, only one SQLite
// database, so this reads it read-only. Before this existed an opencode run
// was seen only through the CPU-sustained agent signal, which is blind while
// the agent waits on the model: on 2026-10-15 a BClouder session was active
// 7.6h and 3.6h of it never reached a tick (17:30-19:10 landed on Dentix, whose
// Chrome helper happened to be busy).
//
// Space attribution is not resolved: opencode publishes no pid registry like
// Claude's sessions/<pid>.json, so a signal here matches on cwd only.
func (p *Pipeline) collectOpencodeSignals(ctx context.Context, snap *CacheSnapshot, now int64) []domain.Signal {
	if !p.cfg.TranscriptTracking || p.cfg.OpencodeDB == "" {
		return nil
	}
	if now < p.opencodeRetryAt {
		return nil
	}
	grace := int64(p.cfg.TranscriptGraceSec)
	// Free gate: opencode appends to the WAL on every message, so neither file
	// being touched within grace means no session can have a recent turn.
	if !opencodeTouchedSince(p.cfg.OpencodeDB, now-grace) {
		return nil
	}
	db, err := p.opencodeHandle()
	if err != nil {
		p.opencodeFailed(now, err)
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	rows, err := db.QueryContext(ctx, `
		SELECT s.id, s.directory
		FROM session_message m
		JOIN session_v2 s ON s.id = m.session_id
		WHERE m.time_created >= ?
		GROUP BY s.id, s.directory`, (now-grace)*1000)
	if err != nil {
		p.opencodeFailed(now, err)
		return nil
	}
	defer rows.Close()

	var out []domain.Signal
	for rows.Next() {
		var id, dir string
		if err := rows.Scan(&id, &dir); err != nil {
			p.opencodeFailed(now, err)
			return nil
		}
		if !cwdMatchesAnyPattern(dir, snap.CwdPatterns) {
			continue
		}
		out = append(out, domain.Signal{
			Source:      domain.SourceTranscript,
			BinaryName:  "opencode",
			Cwd:         dir,
			WindowTitle: id,
		})
	}
	if err := rows.Err(); err != nil {
		p.opencodeFailed(now, err)
		return nil
	}
	return out
}

// opencodeTouchedSince reports whether opencode.db or its WAL was modified at
// or after the given unix second. A missing database reports false.
func opencodeTouchedSince(path string, since int64) bool {
	for _, f := range []string{path, path + "-wal"} {
		if fi, err := os.Stat(f); err == nil && fi.ModTime().Unix() >= since {
			return true
		}
	}
	return false
}

// opencodeHandle lazily opens the database read-only (mode=ro never creates a
// file) and keeps the handle for the life of the pipeline.
func (p *Pipeline) opencodeHandle() (*sql.DB, error) {
	if p.opencodeDB != nil {
		return p.opencodeDB, nil
	}
	dsn := (&url.URL{
		Scheme:   "file",
		Path:     p.cfg.OpencodeDB,
		RawQuery: "mode=ro&_pragma=busy_timeout(250)",
	}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	p.opencodeDB = db
	return db, nil
}

// opencodeFailed drops the handle (so the next attempt re-opens it, which
// recovers from opencode replacing the file) and backs off.
func (p *Pipeline) opencodeFailed(now int64, err error) {
	log.Printf("opencode: %v (retry in %ds)", err, opencodeRetryBackoffSec)
	if p.opencodeDB != nil {
		_ = p.opencodeDB.Close()
		p.opencodeDB = nil
	}
	p.opencodeRetryAt = now + opencodeRetryBackoffSec
}
