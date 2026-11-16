package herdr

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPaneFromEnv(t *testing.T) {
	cases := []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{"HERDR_PANE_ID": "wN:p1"}, "wN:p1"},
		{map[string]string{"HC_TERMINAL_ID": "t13"}, "hc:t13"},
		// A TUI inside a GUI terminal: the innermost (TUI) pane wins.
		{map[string]string{"HERDR_PANE_ID": "wN:p1", "HC_TERMINAL_ID": "t13"}, "wN:p1"},
		{map[string]string{}, ""},
		{nil, ""},
	}
	for _, c := range cases {
		if got := PaneFromEnv(c.env); got != c.want {
			t.Errorf("PaneFromEnv(%v) = %q, want %q", c.env, got, c.want)
		}
	}
}

func TestCoreSpaceForPane(t *testing.T) {
	r := NewResolverWithCore("testdata/session.json", "testdata/core")
	cases := []struct {
		pane, id, name string
		ok             bool
	}{
		{"hc:t5", "spAAA", "Bclouder", true},  // explicit herdr.space label
		{"hc:t13", "spAAA", "Bclouder", true}, // agent-spawned: via parent t5
		{"hc:t14", "spAAA", "Bclouder", true}, // grandchild
		{"hc:t15", "spBBB", "", true},         // only in the docs.json layout
		{"hc:t18", "spBBB", "", true},         // nested layout node, no terminal entry
		{"hc:t16", "spGONE", "Old", true},     // space closed: name from "where"
		{"hc:t17", "", "", false},             // no space anywhere
		{"hc:t30", "", "", false},             // parent cycle terminates
		{"hc:t99", "", "", false},             // unknown terminal
		{"wN:p1", "wN", "MD-tracker", true},   // TUI still works alongside
	}
	for _, c := range cases {
		s, ok := r.SpaceForPane(c.pane)
		if ok != c.ok || s.ID != c.id || s.Name != c.name {
			t.Errorf("SpaceForPane(%q) = %+v, %v; want {%s %s}, %v", c.pane, s, ok, c.id, c.name, c.ok)
		}
	}
	if s, ok := r.Space("spAAA"); !ok || s.Name != "Bclouder" {
		t.Errorf("Space(spAAA) = %+v, %v", s, ok)
	}
	var sawCore, sawTUI bool
	for _, s := range r.Spaces() {
		sawCore = sawCore || s.ID == "spAAA"
		sawTUI = sawTUI || s.ID == "wN"
	}
	if !sawCore || !sawTUI {
		t.Errorf("Spaces() must list both TUI and GUI spaces: %+v", r.Spaces())
	}
}

func TestCoreDisabledResolvesNothing(t *testing.T) {
	r := NewResolver("testdata/session.json")
	if _, ok := r.SpaceForPane("hc:t5"); ok {
		t.Fatal("a resolver without a core dir must not resolve hc: panes")
	}
}

func TestCoreFailsClosedWhenTerminalsDisappear(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"terminals.json", "docs.json"} {
		b, err := os.ReadFile(filepath.Join("testdata/core", f))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, f), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r := NewResolverWithCore("", dir)
	if _, ok := r.SpaceForPane("hc:t5"); !ok {
		t.Fatal("expected t5 to resolve before removal")
	}
	if err := os.Remove(filepath.Join(dir, "terminals.json")); err != nil {
		t.Fatal(err)
	}
	if s, ok := r.SpaceForPane("hc:t5"); ok {
		t.Fatalf("missing terminals.json must fail closed, got %+v", s)
	}
	if err := os.WriteFile(filepath.Join(dir, "terminals.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.SpaceForPane("hc:t5"); ok {
		t.Fatal("unparseable terminals.json must fail closed")
	}
}
