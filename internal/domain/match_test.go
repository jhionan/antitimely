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
