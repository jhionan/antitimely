package domain

import "testing"

func TestProposeRule_AgentWithProjectInPath(t *testing.T) {
	obs := Observation{
		Source:     SourceAgent,
		BinaryName: "claude",
		Cwd:        "/Users/rian/work/foca-api/src/handlers",
	}
	got := ProposeRule(obs, "foca-api")

	if got.MatchBinaryName == nil || *got.MatchBinaryName != "claude" {
		t.Errorf("MatchBinaryName = %v, want claude", got.MatchBinaryName)
	}
	if got.MatchCwdPrefix == nil || *got.MatchCwdPrefix != "/Users/rian/work/foca-api/" {
		t.Errorf("MatchCwdPrefix = %v, want /Users/rian/work/foca-api/", got.MatchCwdPrefix)
	}
	if got.MatchBundleID != nil || got.MatchTitleSubstr != nil {
		t.Errorf("focus-only fields should be nil")
	}
}

func TestProposeRule_AgentWithProjectInPath_CaseMismatch(t *testing.T) {
	// Project name is "Antitimely" (capital) but cwd directory is "antitimely" (lowercase).
	// The generalizer should still find it via case-insensitive match.
	obs := Observation{
		Source:     SourceAgent,
		BinaryName: "opencode",
		Cwd:        "/Users/rian/focaApp/antitimely",
	}
	got := ProposeRule(obs, "Antitimely")

	if got.MatchCwdPrefix == nil || *got.MatchCwdPrefix != "/Users/rian/focaApp/antitimely/" {
		t.Errorf("MatchCwdPrefix = %v, want /Users/rian/focaApp/antitimely/ (case-insensitive match)", got.MatchCwdPrefix)
	}
}

func TestProposeRule_AgentWithoutProjectInPath(t *testing.T) {
	obs := Observation{
		Source:     SourceAgent,
		BinaryName: "claude",
		Cwd:        "/Users/rian/scratch/exp1",
	}
	got := ProposeRule(obs, "experiments")

	if got.MatchBinaryName == nil || *got.MatchBinaryName != "claude" {
		t.Errorf("MatchBinaryName = %v, want claude", got.MatchBinaryName)
	}
	// Fallback: parent dir of cwd, with trailing slash.
	if got.MatchCwdPrefix == nil || *got.MatchCwdPrefix != "/Users/rian/scratch/" {
		t.Errorf("MatchCwdPrefix = %v, want /Users/rian/scratch/", got.MatchCwdPrefix)
	}
}

func TestProposeRule_FocusWithProjectInTitle(t *testing.T) {
	obs := Observation{
		Source:      SourceFocus,
		BundleID:    "com.google.antigravity",
		WindowTitle: "foca-api — main — Antigravity",
	}
	got := ProposeRule(obs, "foca-api")

	if got.MatchBundleID == nil || *got.MatchBundleID != "com.google.antigravity" {
		t.Errorf("MatchBundleID = %v", got.MatchBundleID)
	}
	if got.MatchTitleSubstr == nil || *got.MatchTitleSubstr != "foca-api" {
		t.Errorf("MatchTitleSubstr = %v, want foca-api", got.MatchTitleSubstr)
	}
}

func TestProposeRule_FocusWithoutProjectInTitle(t *testing.T) {
	obs := Observation{
		Source:      SourceFocus,
		BundleID:    "com.apple.Mail",
		WindowTitle: "Inbox — me@example.com",
	}
	got := ProposeRule(obs, "client-work")

	if got.MatchBundleID == nil || *got.MatchBundleID != "com.apple.Mail" {
		t.Errorf("MatchBundleID = %v", got.MatchBundleID)
	}
	// Fallback: the full title becomes the substring match.
	if got.MatchTitleSubstr == nil || *got.MatchTitleSubstr != "Inbox — me@example.com" {
		t.Errorf("MatchTitleSubstr = %v, want full title fallback", got.MatchTitleSubstr)
	}
}
