// Package herdr resolves herdr workspace ("space") identity from herdr's
// on-disk session state. It performs no subprocess calls and no I/O beyond
// reading one JSON file, so it is unit-testable against a fixture.
//
// The file format is undocumented and may change on a herdr update; every
// lookup fails closed (returns ok=false) so attribution degrades to cwd rules
// rather than binding to the wrong project.
package herdr

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Space is a herdr workspace. ID is stable across renames; Name is for display
// only and is empty for a workspace the user has not named.
type Space struct {
	ID   string
	Name string
}

// DefaultSessionPath is where herdr keeps its session state.
func DefaultSessionPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "herdr", "session.json")
}

type sessionFile struct {
	Workspaces []struct {
		ID         string  `json:"id"`
		CustomName *string `json:"custom_name"`
		Tabs       []struct {
			Panes map[string]struct {
				AgentSession *struct {
					Value string `json:"value"`
				} `json:"agent_session"`
			} `json:"panes"`
		} `json:"tabs"`
	} `json:"workspaces"`
}

// Resolver caches a parse of session.json, reloading when the file's mtime or
// size changes. Safe for concurrent use.
type Resolver struct {
	path string

	mu       sync.Mutex
	modTime  time.Time
	size     int64
	loaded   bool
	warned   bool              // true once a failure has been logged, until the next successful parse
	spaces   map[string]Space  // workspace id -> Space
	sessions map[string]string // claude session uuid -> workspace id
}

func NewResolver(path string) *Resolver {
	return &Resolver{path: path}
}

// SpaceForPane resolves a HERDR_PANE_ID such as "wN:p1" to its workspace.
func (r *Resolver) SpaceForPane(paneID string) (Space, bool) {
	if paneID == "" {
		return Space{}, false
	}
	wid, _, _ := strings.Cut(paneID, ":")
	if wid == "" {
		return Space{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reloadLocked()
	s, ok := r.spaces[wid]
	return s, ok
}

// SpaceForSession resolves a Claude Code session uuid to the workspace whose
// pane reported it. The binding outlives the pane's *current* session: once
// learned it is kept across reloads, so a rotated session whose transcript is
// still emitting keeps resolving to the space it ran in.
func (r *Resolver) SpaceForSession(uuid string) (Space, bool) {
	if uuid == "" {
		return Space{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reloadLocked()
	wid, ok := r.sessions[uuid]
	if !ok {
		return Space{}, false
	}
	s, ok := r.spaces[wid]
	return s, ok
}

// Spaces returns every known workspace, ordered by id.
func (r *Resolver) Spaces() []Space {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reloadLocked()
	out := make([]Space, 0, len(r.spaces))
	for _, s := range r.spaces {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// reloadLocked re-parses the file when it has changed. Workspaces are
// replaced wholesale by what the file currently says; session uuid bindings
// are accumulated across successful reloads (see below). If session.json is
// missing, unreadable, or fails to parse, the resolver fails closed: it
// discards any previously loaded spaces/sessions (so a stale mapping is never
// served after herdr exits or the file is briefly truncated/corrupt) and
// resets modTime/size to their zero values so a later, valid reappearance of
// the file is detected and reloaded on the next call. The failure is logged
// at most once per transition into the failed state (via r.warned), not on
// every call, since callers poll this every few seconds.
func (r *Resolver) reloadLocked() {
	if r.path == "" {
		return
	}
	fi, err := os.Stat(r.path)
	if err != nil {
		r.failClosedLocked(fmt.Sprintf("stat %s: %v", r.path, err))
		return
	}
	if r.loaded && !r.warned && fi.ModTime().Equal(r.modTime) && fi.Size() == r.size {
		return
	}
	raw, err := os.ReadFile(r.path)
	if err != nil {
		r.failClosedLocked(fmt.Sprintf("read %s: %v", r.path, err))
		return
	}
	var f sessionFile
	if err := json.Unmarshal(raw, &f); err != nil {
		r.failClosedLocked(fmt.Sprintf("parse %s: %v", r.path, err))
		return
	}
	spaces := make(map[string]Space, len(f.Workspaces))
	// Session bindings ACCUMULATE across successful reloads instead of being
	// replaced. herdr records only each pane's CURRENT agent_session, so a
	// session that rotates (--resume, compaction, a new session started in
	// the same pane) disappears from the file while its transcript keeps
	// emitting for the whole grace window. Rebuilding the map from scratch
	// dropped that binding, so those still-live seconds fell back to cwd
	// matching — another client's project — while the new session billed the
	// right one, putting the same second in two timesheets. A Claude session
	// uuid belongs to exactly one pane for its lifetime, so keeping an
	// already-learned binding is sound; a later file that names the uuid
	// again simply overwrites it with the same (or newer) workspace.
	//
	// This does not weaken the fail-closed contract: failClosedLocked still
	// wipes this map wholesale, so a missing or unparseable session.json
	// resolves nothing, learned bindings included. Only successful reloads
	// retain. Growth is bounded by the number of distinct Claude sessions
	// observed during one daemon lifetime (tens per day, a uuid each).
	sessions := r.sessions
	if sessions == nil {
		sessions = map[string]string{}
	}
	for _, w := range f.Workspaces {
		if w.ID == "" {
			continue
		}
		name := ""
		if w.CustomName != nil {
			name = *w.CustomName
		}
		spaces[w.ID] = Space{ID: w.ID, Name: name}
		for _, t := range w.Tabs {
			for _, p := range t.Panes {
				if p.AgentSession != nil && p.AgentSession.Value != "" {
					sessions[p.AgentSession.Value] = w.ID
				}
			}
		}
	}
	r.spaces, r.sessions = spaces, sessions
	r.modTime, r.size, r.loaded = fi.ModTime(), fi.Size(), true
	r.warned = false
}

// failClosedLocked clears any previously loaded mapping so lookups resolve
// nothing, and logs reason at most once per transition into the failed
// state. Callers must hold r.mu.
func (r *Resolver) failClosedLocked(reason string) {
	r.spaces, r.sessions = map[string]Space{}, map[string]string{}
	r.loaded = true
	r.modTime, r.size = time.Time{}, 0
	if !r.warned {
		log.Printf("herdr: %s; spaces unresolved until session.json is valid again", reason)
		r.warned = true
	}
}
