package herdr

import (
	"os"
	"path/filepath"
	"testing"
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
