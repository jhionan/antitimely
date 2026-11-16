package herdr

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// herdr-core is the terminal server behind the Herdr GUI (Herdr.app). Its
// panes do not set HERDR_PANE_ID; each process instead inherits
// HC_TERMINAL_ID ("t13") naming its terminal in the core's state directory.
// That directory holds two files read here:
//
//   - terminals.json: every terminal with its labels (the GUI stamps the
//     owning space as "herdr.space") and the "parent" terminal that spawned
//     it. Terminals spawned by an agent (`hc run` from a pane) carry no space
//     label, only the parent.
//   - docs.json: the GUI's layout, naming each space and the terminals placed
//     in its tabs.
//
// Space ids are "sp<hex>" and cannot collide with the TUI's "wN" ids.

// CorePanePrefix marks a pane token that names a herdr-core terminal rather
// than a TUI HERDR_PANE_ID. CorePaneID builds one from HC_TERMINAL_ID.
const CorePanePrefix = "hc:"

// CorePaneID turns an HC_TERMINAL_ID value into a pane token for
// Resolver.SpaceForPane. Empty in, empty out.
func CorePaneID(terminalID string) string {
	if terminalID == "" {
		return ""
	}
	return CorePanePrefix + terminalID
}

// DefaultCoreDir is the state directory of herdr-core's default instance.
func DefaultCoreDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "state", "herdr-core", "default")
}

// maxParentHops bounds the parent walk so a malformed (cyclic) file can't
// spin; real agent trees are a few levels deep.
const maxParentHops = 16

type coreTerminalsFile struct {
	Terminals map[string]struct {
		Address string            `json:"address"`
		Labels  map[string]string `json:"labels"`
		Parent  *string           `json:"parent"`
	} `json:"terminals"`
}

type coreDocsFile struct {
	App struct {
		Value struct {
			Spaces []struct {
				ID   string          `json:"id"`
				Name string          `json:"name"`
				Tabs []coreLayoutTab `json:"tabs"`
			} `json:"spaces"`
		} `json:"value"`
	} `json:"herdr-app"`
}

type coreLayoutTab struct {
	Root json.RawMessage `json:"root"`
}

type coreLayoutNode struct {
	Content *struct {
		Terminal string `json:"terminal"`
	} `json:"content"`
	Children []json.RawMessage `json:"children"`
}

type fileStamp struct {
	modTime time.Time
	size    int64
}

// coreState is the parsed herdr-core state. All fields are guarded by the
// owning Resolver's mutex.
type coreState struct {
	dir string

	terms, docs fileStamp
	loaded      bool
	warned      bool

	spaces     map[string]Space  // space id -> Space
	termSpace  map[string]string // terminal address -> space id (label or layout)
	termParent map[string]string // terminal address -> parent address
}

// reloadLocked re-parses terminals.json and docs.json when either changed.
// It fails closed like the session.json reader: if terminals.json is missing
// or unparseable every core lookup resolves nothing. docs.json only supplies
// names and layout membership, so its absence alone is tolerated.
func (c *coreState) reloadLocked() {
	if c == nil || c.dir == "" {
		return
	}
	termPath := filepath.Join(c.dir, "terminals.json")
	docsPath := filepath.Join(c.dir, "docs.json")
	tfi, err := os.Stat(termPath)
	if err != nil {
		c.failClosedLocked(fmt.Sprintf("stat %s: %v", termPath, err))
		return
	}
	var dstamp fileStamp
	if dfi, err := os.Stat(docsPath); err == nil {
		dstamp = fileStamp{dfi.ModTime(), dfi.Size()}
	}
	tstamp := fileStamp{tfi.ModTime(), tfi.Size()}
	if c.loaded && !c.warned && tstamp == c.terms && dstamp == c.docs {
		return
	}

	raw, err := os.ReadFile(termPath)
	if err != nil {
		c.failClosedLocked(fmt.Sprintf("read %s: %v", termPath, err))
		return
	}
	var tf coreTerminalsFile
	if err := json.Unmarshal(raw, &tf); err != nil {
		c.failClosedLocked(fmt.Sprintf("parse %s: %v", termPath, err))
		return
	}

	spaces := map[string]Space{}
	termSpace := map[string]string{}
	termParent := map[string]string{}

	// Layout first, so an explicit space label (below) wins over it.
	if raw, err := os.ReadFile(docsPath); err == nil {
		var df coreDocsFile
		if err := json.Unmarshal(raw, &df); err == nil {
			for _, s := range df.App.Value.Spaces {
				if s.ID == "" {
					continue
				}
				spaces[s.ID] = Space{ID: s.ID, Name: s.Name}
				for _, tab := range s.Tabs {
					for _, term := range layoutTerminals(tab.Root, 0) {
						termSpace[term] = s.ID
					}
				}
			}
		}
	}

	for key, t := range tf.Terminals {
		addr := t.Address
		if addr == "" {
			addr = "t" + key
		}
		if sp := t.Labels["herdr.space"]; sp != "" {
			termSpace[addr] = sp
			if _, ok := spaces[sp]; !ok {
				// A space closed in the GUI is gone from docs.json, but its
				// terminals (and their transcripts) may still be billing.
				spaces[sp] = Space{ID: sp, Name: spaceNameFromWhere(t.Labels["where"])}
			}
		}
		if t.Parent != nil && *t.Parent != "" {
			termParent[addr] = *t.Parent
		}
	}

	c.spaces, c.termSpace, c.termParent = spaces, termSpace, termParent
	c.terms, c.docs, c.loaded, c.warned = tstamp, dstamp, true, false
}

func (c *coreState) failClosedLocked(reason string) {
	c.spaces, c.termSpace, c.termParent = map[string]Space{}, map[string]string{}, map[string]string{}
	c.loaded = true
	c.terms, c.docs = fileStamp{}, fileStamp{}
	if !c.warned {
		log.Printf("herdr-core: %s; GUI spaces unresolved until it is valid again", reason)
		c.warned = true
	}
}

// spaceForTerminalLocked walks from a terminal up its parent chain to the
// first one the GUI assigned to a space.
func (c *coreState) spaceForTerminalLocked(term string) (Space, bool) {
	if c == nil {
		return Space{}, false
	}
	for hop := 0; term != "" && hop <= maxParentHops; hop++ {
		if sp, ok := c.termSpace[term]; ok {
			s, ok := c.spaces[sp]
			return s, ok
		}
		term = c.termParent[term]
	}
	return Space{}, false
}

// layoutTerminals collects terminal addresses from a docs.json layout tree.
func layoutTerminals(raw json.RawMessage, depth int) []string {
	if len(raw) == 0 || depth > 64 {
		return nil
	}
	var n coreLayoutNode
	if err := json.Unmarshal(raw, &n); err != nil {
		return nil
	}
	var out []string
	if n.Content != nil && n.Content.Terminal != "" {
		out = append(out, n.Content.Terminal)
	}
	for _, ch := range n.Children {
		out = append(out, layoutTerminals(ch, depth+1)...)
	}
	return out
}

// spaceNameFromWhere extracts the space name from a "where" label such as
// "Bclouder / boss5".
func spaceNameFromWhere(where string) string {
	name, _, _ := strings.Cut(where, " / ")
	return strings.TrimSpace(name)
}

// Environment variables that identify a herdr pane. HERDR_PANE_ID (TUI) is
// checked first: a TUI started inside a GUI terminal sets it on its own panes,
// and the innermost multiplexer is the one that knows the space.
const (
	EnvTUIPane      = "HERDR_PANE_ID"
	EnvCoreTerminal = "HC_TERMINAL_ID"
)

// PaneEnvKeys lists the variables PaneFromEnv reads, for a single env probe.
var PaneEnvKeys = []string{EnvTUIPane, EnvCoreTerminal}

// PaneFromEnv returns the pane token for a process environment: the TUI's
// HERDR_PANE_ID, else a CorePaneID built from HC_TERMINAL_ID, else "" (the
// process is not under herdr).
func PaneFromEnv(env map[string]string) string {
	if v := env[EnvTUIPane]; v != "" {
		return v
	}
	return CorePaneID(env[EnvCoreTerminal])
}
