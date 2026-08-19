package cli

import "testing"

func TestRulesAddRejectsNoMatchField(t *testing.T) {
	if code := cmdRules([]string{"add", "--project=MD-Tracker"}); code != 64 {
		t.Fatalf("a rule with no match field must exit 64, got %d", code)
	}
}

func TestRulesAddRejectsStarBeforeLastSlash(t *testing.T) {
	code := cmdRules([]string{"add", "--project=MD-Tracker", "--cwd=/a/*/md-x"})
	if code != 64 {
		t.Fatalf("star before the last / must exit 64, got %d", code)
	}
}
