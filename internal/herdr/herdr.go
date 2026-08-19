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
	"os"
	"path/filepath"
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
// pane reported it.
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

// reloadLocked re-parses the file when it has changed. Any failure leaves the
// previous (possibly empty) maps in place; callers then simply resolve nothing.
func (r *Resolver) reloadLocked() {
	if r.path == "" {
		return
	}
	fi, err := os.Stat(r.path)
	if err != nil {
		if !r.loaded {
			r.spaces, r.sessions = map[string]Space{}, map[string]string{}
			r.loaded = true
		}
		return
	}
	if r.loaded && fi.ModTime().Equal(r.modTime) && fi.Size() == r.size {
		return
	}
	raw, err := os.ReadFile(r.path)
	if err != nil {
		return
	}
	var f sessionFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return
	}
	spaces := make(map[string]Space, len(f.Workspaces))
	sessions := map[string]string{}
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
}
