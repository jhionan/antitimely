package herdr

import "testing"

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
