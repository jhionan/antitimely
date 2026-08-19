package herdr

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSpaceForPane(t *testing.T) {
	r := NewResolver("testdata/session.json")
	s, ok := r.SpaceForPane("wN:p1")
	if !ok || s.ID != "wN" || s.Name != "MD-tracker" {
		t.Fatalf("SpaceForPane(wN:p1) = %+v, %v", s, ok)
	}
	s, ok = r.SpaceForPane("wM:p1")
	if !ok || s.ID != "wM" || s.Name != "" {
		t.Fatalf("unnamed space must resolve with an empty Name, got %+v, %v", s, ok)
	}
	if _, ok := r.SpaceForPane("wZ:p9"); ok {
		t.Fatal("unknown workspace must not resolve")
	}
	if _, ok := r.SpaceForPane(""); ok {
		t.Fatal("empty pane id must not resolve")
	}
}

func TestSpaceForSession(t *testing.T) {
	r := NewResolver("testdata/session.json")
	s, ok := r.SpaceForSession("bbbbbbbb-1111-1111-1111-111111111111")
	if !ok || s.ID != "wN" {
		t.Fatalf("SpaceForSession = %+v, %v", s, ok)
	}
	if _, ok := r.SpaceForSession("cccccccc-2222-2222-2222-222222222222"); ok {
		t.Fatal("unknown session must not resolve")
	}
}

func TestMissingFileIsNotAnError(t *testing.T) {
	r := NewResolver("testdata/does-not-exist.json")
	if _, ok := r.SpaceForPane("wN:p1"); ok {
		t.Fatal("missing session.json must resolve nothing, not panic")
	}
}

// TestFailsClosedWhenFileDisappears is the regression test for the bug where
// reloadLocked kept serving a stale mapping after the first successful load
// once session.json stopped existing (e.g. herdr exited).
func TestFailsClosedWhenFileDisappears(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.json")

	data, err := os.ReadFile("testdata/session.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write copy: %v", err)
	}

	r := NewResolver(path)
	if _, ok := r.SpaceForPane("wN:p1"); !ok {
		t.Fatal("expected SpaceForPane to resolve before the file disappears")
	}

	if err := os.Remove(path); err != nil {
		t.Fatalf("remove copy: %v", err)
	}

	if _, ok := r.SpaceForPane("wN:p1"); ok {
		t.Fatal("expected SpaceForPane to fail closed once session.json disappears, not keep serving the stale mapping")
	}
}

// TestFailsClosedOnUnparseableJSON covers the other failure mode named in the
// spec's failure table: an unparseable session.json must resolve nothing
// rather than panicking or returning a stale mapping.
func TestFailsClosedOnUnparseableJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.json")
	if err := os.WriteFile(path, []byte(`{"workspaces":`), 0o644); err != nil {
		t.Fatalf("write invalid json: %v", err)
	}

	r := NewResolver(path)
	if _, ok := r.SpaceForPane("wN:p1"); ok {
		t.Fatal("expected SpaceForPane to fail closed on unparseable JSON")
	}
	if _, ok := r.SpaceForSession("bbbbbbbb-1111-1111-1111-111111111111"); ok {
		t.Fatal("expected SpaceForSession to fail closed on unparseable JSON")
	}
}

// sessionFileWith renders a one-workspace session.json whose single pane
// reports sessionUUID as its CURRENT agent session — the only session herdr
// records for a pane.
func sessionFileWith(sessionUUID string) string {
	return `{
  "version": 1,
  "workspaces": [
    {
      "id": "wN",
      "custom_name": "MD-tracker",
      "tabs": [
        {"panes": {"1": {"agent_session": {"agent": "claude", "kind": "id", "value": "` + sessionUUID + `"}}}}
      ]
    }
  ]
}`
}

// TestSessionBindingSurvivesRotation is the regression test for transcript
// space bindings being lost on session rotation. herdr records only each
// pane's CURRENT agent_session, so when a session rotates (--resume,
// compaction, a new session in the same pane) the previous uuid vanishes from
// the file while its transcript keeps emitting for the whole grace window
// (600s live). Rebuilding the map from scratch dropped that binding, sending
// those still-live seconds to the cwd rule — i.e. to another client's project
// — while the new session billed the right one, so the same second landed in
// two timesheets. A session uuid belongs to exactly one pane for its lifetime,
// so bindings must ACCUMULATE across successful reloads.
func TestSessionBindingSurvivesRotation(t *testing.T) {
	const sessA = "aaaaaaaa-1111-1111-1111-111111111111"
	const sessB = "bbbbbbbb-2222-2222-2222-222222222222"

	dir := t.TempDir()
	path := filepath.Join(dir, "session.json")
	if err := os.WriteFile(path, []byte(sessionFileWith(sessA)), 0o644); err != nil {
		t.Fatal(err)
	}

	r := NewResolver(path)
	s, ok := r.SpaceForSession(sessA)
	if !ok || s.ID != "wN" {
		t.Fatalf("session A must resolve while it is the pane's current session, got %+v %v", s, ok)
	}

	// The pane rotates to a new session. A is no longer named in the file.
	if err := os.WriteFile(path, []byte(sessionFileWith(sessB)), 0o644); err != nil {
		t.Fatal(err)
	}
	// Both files are the same size, so bump mtime explicitly rather than
	// relying on filesystem timestamp resolution to trigger the reload.
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}

	if s, ok := r.SpaceForSession(sessB); !ok || s.ID != "wN" {
		t.Fatalf("the pane's new session must resolve, got %+v %v", s, ok)
	}
	if s, ok := r.SpaceForSession(sessA); !ok || s.ID != "wN" {
		t.Fatalf("session A must still resolve to its pane's space after rotation, got %+v %v — "+
			"its transcript keeps emitting for the grace window and would otherwise "+
			"be attributed by cwd, i.e. to the wrong project", s, ok)
	}
}

// TestAccumulatedSessionsStillFailClosed pins the boundary of the
// accumulation above: retaining uuid bindings across SUCCESSFUL reloads must
// not weaken the fail-closed contract. A missing or unparseable session.json
// still resolves nothing at all, learned bindings included.
func TestAccumulatedSessionsStillFailClosed(t *testing.T) {
	const sessA = "aaaaaaaa-1111-1111-1111-111111111111"
	dir := t.TempDir()
	path := filepath.Join(dir, "session.json")
	if err := os.WriteFile(path, []byte(sessionFileWith(sessA)), 0o644); err != nil {
		t.Fatal(err)
	}
	r := NewResolver(path)
	if _, ok := r.SpaceForSession(sessA); !ok {
		t.Fatal("expected session A to resolve before the file goes bad")
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.SpaceForSession(sessA); ok {
		t.Fatal("a learned session binding must be dropped when session.json disappears")
	}

	if err := os.WriteFile(path, []byte(`{"workspaces":`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.SpaceForSession(sessA); ok {
		t.Fatal("a learned session binding must be dropped when session.json is unparseable")
	}
}
