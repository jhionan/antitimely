package domain

import "testing"

func ptr(s string) *string { return &s }

func TestMatchRules(t *testing.T) {
	rules := []RuleSpec{
		{ID: 1, ProjectID: 10, Priority: 100,
			MatchBinaryName: ptr("claude"),
			MatchCwdPrefix:  ptr("/Users/rian/work/foca-api/")},
		{ID: 2, ProjectID: 20, Priority: 100,
			MatchBundleID:    ptr("com.google.antigravity"),
			MatchTitleSubstr: ptr("antitimely")},
		{ID: 3, ProjectID: 30, Priority: 200, // lower priority (higher number)
			MatchBundleID: ptr("com.google.antigravity")},
	}

	tests := []struct {
		name   string
		sig    Signal
		wantID int64 // 0 = no match
	}{
		{
			name:   "agent claude in foca-api → rule 1",
			sig:    Signal{Source: SourceAgent, BinaryName: "claude", Cwd: "/Users/rian/work/foca-api/src"},
			wantID: 10,
		},
		{
			name:   "agent claude elsewhere → no match",
			sig:    Signal{Source: SourceAgent, BinaryName: "claude", Cwd: "/Users/rian/personal/"},
			wantID: 0,
		},
		{
			name:   "antigravity with antitimely in title → rule 2 (priority 100)",
			sig:    Signal{Source: SourceFocus, BundleID: "com.google.antigravity", WindowTitle: "antitimely — main — Antigravity"},
			wantID: 20,
		},
		{
			name:   "antigravity without that substring → falls through to rule 3",
			sig:    Signal{Source: SourceFocus, BundleID: "com.google.antigravity", WindowTitle: "untitled"},
			wantID: 30,
		},
		{
			name:   "unrelated bundle → no match",
			sig:    Signal{Source: SourceFocus, BundleID: "com.apple.Slack"},
			wantID: 0,
		},
		{
			name:   "agent matches binary-only rule (no cwd constraint)",
			sig:    Signal{Source: SourceAgent, BinaryName: "opencode"},
			wantID: 0, // no such rule in the set
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := MatchRules(tc.sig, rules)
			if tc.wantID == 0 {
				if got != nil {
					t.Errorf("expected no match, got project_id=%d", *got)
				}
				return
			}
			if got == nil {
				t.Fatalf("expected project_id=%d, got no match", tc.wantID)
			}
			if *got != tc.wantID {
				t.Errorf("project_id = %d, want %d", *got, tc.wantID)
			}
		})
	}
}

func TestMatchRules_CwdPrefixSemantics(t *testing.T) {
	// Prefix with trailing slash should match: exact dir, subdirs.
	// Should NOT match: sibling dirs with similar names.
	rules := []RuleSpec{
		{ID: 1, ProjectID: 100, Priority: 100,
			MatchBinaryName: ptr("claude"),
			MatchCwdPrefix:  ptr("/work/antitimely/")},
	}
	cases := []struct {
		cwd  string
		want bool
	}{
		{"/work/antitimely", true},        // exact dir, no trailing slash
		{"/work/antitimely/", true},       // exact dir, trailing slash
		{"/work/antitimely/src", true},    // subdir
		{"/work/antitimely/src/foo.go", true},
		{"/work/antitimely-other", false}, // sibling with similar name
		{"/work/antitimelypics", false},   // sibling without separator
		{"/work/other", false},            // unrelated
	}
	for _, tc := range cases {
		sig := Signal{Source: SourceAgent, BinaryName: "claude", Cwd: tc.cwd}
		got := MatchRules(sig, rules) != nil
		if got != tc.want {
			t.Errorf("cwd %q: match=%v, want %v", tc.cwd, got, tc.want)
		}
	}
}

func TestMatchRules_PriorityOrder(t *testing.T) {
	// Rule A: high priority (lower number), broad match.
	// Rule B: low priority (higher number), narrow match.
	// Signal matches both — high-priority should win.
	rules := []RuleSpec{
		{ID: 1, ProjectID: 100, Priority: 50, MatchBundleID: ptr("com.foo")},
		{ID: 2, ProjectID: 200, Priority: 100, MatchBundleID: ptr("com.foo"), MatchTitleSubstr: ptr("bar")},
	}
	sig := Signal{Source: SourceFocus, BundleID: "com.foo", WindowTitle: "bar"}
	got := MatchRules(sig, rules)
	if got == nil || *got != 100 {
		t.Errorf("expected 100 (priority 50 wins), got %v", got)
	}
}

func TestMatchesCwd(t *testing.T) {
	const wt = "/Users/rian/focaApp/bclouder/daas/daas-back-end/.claude/worktrees"
	cases := []struct {
		name    string
		pattern string
		cwd     string
		want    bool
	}{
		{"literal exact", wt + "/md-tracker", wt + "/md-tracker", true},
		{"literal subdir", wt + "/md-tracker", wt + "/md-tracker/DAAS.API", true},
		{"literal sibling not matched", wt + "/md-tracker", wt + "/md-tracker-scaffold", false},
		{"literal trailing slash", wt + "/md-tracker/", wt + "/md-tracker/DAAS.API", true},
		{"glob worktree", wt + "/md-*", wt + "/md-engine", true},
		{"glob nested build dir", wt + "/md-*", wt + "/md-engine/DAAS.Application.Services.Gos", true},
		{"glob future worktree", wt + "/md-*", wt + "/md-anything-new", true},
		{"glob excludes non-md", wt + "/md-*", wt + "/packing-slip-default-assignee", false},
		{"glob excludes repo root", wt + "/md-*", "/Users/rian/focaApp/bclouder/daas/daas-back-end", false},
		{"glob excludes worktrees parent", wt + "/md-*", wt, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MatchesCwd(tc.pattern, tc.cwd); got != tc.want {
				t.Fatalf("MatchesCwd(%q, %q) = %v, want %v", tc.pattern, tc.cwd, got, tc.want)
			}
		})
	}
}

// TestValidateCwdPattern pins the accepted shapes. Parity between MatchesCwd
// (path.Match + ancestor walk) and the retroactive SQL (SQLite GLOB) holds
// only when '*' is the FINAL CHARACTER - not merely somewhere in the last
// segment, which is what this used to allow. Verified against both engines:
// "/wt/*-md" vs "/wt/a/b-md" and "/wt/md-*x" vs "/wt/md-a/b/cx" both GLOB-match
// and both fail path.Match, and a bare "*" matches everything in SQL and
// nothing in Go.
func TestValidateCwdPattern(t *testing.T) {
	valid := []string{
		"/a/b/md-*",  // star final
		"/a/b/md-*/", // trailing slash is trimmed first
		"/a/b/c",     // plain literal prefix
		"/a/b/c/",
		"/*", // degenerate but consistent: star final, has a '/'
	}
	for _, p := range valid {
		if err := ValidateCwdPattern(p); err != nil {
			t.Errorf("ValidateCwdPattern(%q) = %v, want nil", p, err)
		}
	}

	invalid := []string{
		"/a/*/md-x", // star before the last '/'
		"/wt/*-md",  // star in the last segment but not final
		"/wt/md-*x", // ditto, one character short of final
		"/wt/*/*",   // more than one star
		"*",         // no '/': matches everything in GLOB, nothing in Go
		"md-*",      // no '/'
		"",          // empty
		"/",         // empty after trimming
	}
	for _, p := range invalid {
		if err := ValidateCwdPattern(p); err == nil {
			t.Errorf("ValidateCwdPattern(%q) = nil, want an error", p)
		}
	}
}

// TestValidateCwdPatternMatchesRealDivergence is the empirical half: every
// pattern ValidateCwdPattern rejects for parity reasons must actually diverge
// between MatchesCwd and GLOB semantics, and the accepted shape must not.
// (GLOB's answers below are the measured ones - SQLite matches all four.)
func TestValidateCwdPatternMatchesRealDivergence(t *testing.T) {
	cases := []struct {
		pattern, cwd string
		matchesCwd   bool // what the live matcher says; GLOB says true for all
	}{
		{"/wt/*-md", "/wt/a/b-md", false},        // rejected: diverges
		{"/wt/md-*x", "/wt/md-a/b/cx", false},    // rejected: diverges
		{"*", "/a/b", false},                     // rejected: diverges
		{"/wt/md-*", "/wt/md-tracker/sub", true}, // accepted: agrees
	}
	for _, c := range cases {
		if got := MatchesCwd(c.pattern, c.cwd); got != c.matchesCwd {
			t.Errorf("MatchesCwd(%q, %q) = %v, want %v", c.pattern, c.cwd, got, c.matchesCwd)
		}
		err := ValidateCwdPattern(c.pattern)
		if c.matchesCwd && err != nil {
			t.Errorf("ValidateCwdPattern(%q) rejected a pattern that agrees with GLOB: %v", c.pattern, err)
		}
		if !c.matchesCwd && err == nil {
			t.Errorf("ValidateCwdPattern(%q) accepted a pattern whose live and retroactive "+
				"matching disagree", c.pattern)
		}
	}
}

func TestMatchRulesSpaceID(t *testing.T) {
	space := "wN"
	daasCwd := "/Users/rian/focaApp/bclouder/daas/"
	rules := []RuleSpec{
		{ID: 18, ProjectID: 7, Priority: 100, MatchCwdPrefix: &daasCwd},
		{ID: 50, ProjectID: 8, Priority: 50, MatchSpaceID: &space},
	}
	sig := Signal{
		Source:  SourceAgent,
		Cwd:     "/Users/rian/focaApp/bclouder/daas/daas-back-end",
		SpaceID: "wN",
	}
	got := MatchRules(sig, rules)
	if got == nil || *got != 8 {
		t.Fatalf("priority-50 space rule must beat priority-100 cwd rule, got %v", got)
	}

	sig.SpaceID = "wM"
	got = MatchRules(sig, rules)
	if got == nil || *got != 7 {
		t.Fatalf("unbound space must fall through to the cwd rule, got %v", got)
	}
}
