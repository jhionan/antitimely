package daemon

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rian/antitimely/internal/domain"
	"github.com/rian/antitimely/internal/store"
)

func importTranscripts(db *sql.DB, q *store.Queries, snap *CacheSnapshot, root string, fromUnix, toUnix int64, graceSec, intervalSec int) (int, error) {
	ctx := context.Background()
	projDirs, err := os.ReadDir(root)
	if err != nil {
		return 0, nil
	}
	inserted := 0
	for _, pd := range projDirs {
		if !pd.IsDir() {
			continue
		}
		sessDir := filepath.Join(root, pd.Name())
		files, err := os.ReadDir(sessDir)
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() || filepath.Ext(f.Name()) != ".jsonl" {
				continue
			}
			data, err := os.ReadFile(filepath.Join(sessDir, f.Name()))
			if err != nil {
				continue
			}
			cwd, stamps := scanEntries(data, fromUnix, toUnix)
			if cwd == "" {
				cwd = decodeProjectDir(pd.Name())
			}
			if len(stamps) == 0 {
				continue
			}
			pid := domain.MatchRules(domain.Signal{Source: domain.SourceTranscript, Cwd: cwd}, snap.Rules)
			if pid == nil {
				continue
			}
			obsID, err := q.UpsertObservation(ctx, store.UpsertObservationParams{
				Source:      string(domain.SourceTranscript),
				Cwd:         cwd,
				WindowTitle: strings.TrimSuffix(f.Name(), ".jsonl"),
				FirstSeen:   stamps[0],
			})
			if err != nil {
				continue
			}
			for _, blk := range splitBlocks(stamps, int64(graceSec)) {
				for ts := blk[0]; ts <= blk[1]; ts += int64(intervalSec) {
					var exists int
					db.QueryRowContext(ctx, `SELECT 1 FROM ticks WHERE ts=? AND project_id=?`, ts, *pid).Scan(&exists)
					if exists == 1 {
						continue
					}
					if err := q.InsertTick(ctx, store.InsertTickParams{
						Ts:            ts,
						ObservationID: obsID,
						ProjectID:     sql.NullInt64{Int64: *pid, Valid: true},
					}); err == nil {
						inserted++
					}
				}
			}
		}
	}
	return inserted, nil
}

// scanEntries returns the last cwd and all in-range entry timestamps (sorted).
func scanEntries(data []byte, fromUnix, toUnix int64) (string, []int64) {
	var cwd string
	var out []int64
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var e transcriptEntry
		if json.Unmarshal([]byte(line), &e) != nil {
			continue
		}
		if e.Cwd != "" {
			cwd = e.Cwd
		}
		if e.Timestamp == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, e.Timestamp)
		if err != nil {
			continue
		}
		u := t.Unix()
		if u >= fromUnix && u <= toUnix {
			out = append(out, u)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return cwd, out
}

// splitBlocks groups sorted timestamps into [start,end] blocks, breaking where
// the gap between consecutive stamps exceeds graceSec.
func splitBlocks(stamps []int64, graceSec int64) [][2]int64 {
	if len(stamps) == 0 {
		return nil
	}
	var blocks [][2]int64
	start, prev := stamps[0], stamps[0]
	for _, s := range stamps[1:] {
		if s-prev > graceSec {
			blocks = append(blocks, [2]int64{start, prev})
			start = s
		}
		prev = s
	}
	blocks = append(blocks, [2]int64{start, prev})
	return blocks
}
