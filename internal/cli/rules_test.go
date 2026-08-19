package cli

import (
	"io"
	"os"
	"strings"
	"testing"
)

// captureRulesAddOutput redirects os.Stderr to a pipe for the duration of
// cmdRules(args), returning what was written plus the exit code. Reading
// happens on a goroutine (started before cmdRules runs) so a write that
// fills the pipe's kernel buffer can't deadlock against a reader that
// hasn't started yet; the writer end is swapped back and closed before the
// read is drained, which is what unblocks io.ReadAll on EOF.
func captureRulesAddOutput(t *testing.T, args []string) (output string, code int) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w

	outCh := make(chan string, 1)
	go func() {
		buf, _ := io.ReadAll(r)
		outCh <- string(buf)
	}()

	code = cmdRules(args)

	os.Stderr = orig
	w.Close()
	output = <-outCh
	r.Close()
	return output, code
}

// TestRulesAddRejectsNoMatchField and TestRulesAddRejectsStarBeforeLastSlash
// must fail if `rules add` is ever unwired from cmdRules's switch, not just
// if its validation logic breaks. Asserting on the exit code alone can't
// tell those apart: cmdRules's `default` branch ("unknown subcommand: rules
// add") also returns 64, so a deleted `case "add"` would still make the
// exit-code-only version of this test pass. Worse, a validation regression
// that falls through to dialOrExit would reach the REAL
// ~/.antitimely/antitimely.sock — against a live daemon that understands
// RuleAdd, that could insert a rule into the user's real billing database.
// So every case here also asserts the specific validation message, and
// asserts the "unknown subcommand" text is absent — that absence check is
// what actually catches the subcommand being deleted or misspelled.
func TestRulesAddRejectsNoMatchField(t *testing.T) {
	out, code := captureRulesAddOutput(t, []string{"add", "--project=MD-Tracker"})
	if code != 64 {
		t.Fatalf("a rule with no match field must exit 64, got %d (output: %q)", code, out)
	}
	if !strings.Contains(out, "at least one match field is required") {
		t.Fatalf("expected the match-field validation message, got %q", out)
	}
	if strings.Contains(out, "unknown subcommand") {
		t.Fatalf("rules add must be wired up in cmdRules's switch, not falling through to the unknown-subcommand branch: %q", out)
	}
}

func TestRulesAddRejectsStarBeforeLastSlash(t *testing.T) {
	out, code := captureRulesAddOutput(t, []string{"add", "--project=MD-Tracker", "--cwd=/a/*/md-x"})
	if code != 64 {
		t.Fatalf("star before the last / must exit 64, got %d (output: %q)", code, out)
	}
	if !strings.Contains(out, "final path segment") {
		t.Fatalf("expected the ValidateCwdPattern error naming the final path segment, got %q", out)
	}
	if strings.Contains(out, "unknown subcommand") {
		t.Fatalf("rules add must be wired up in cmdRules's switch, not falling through to the unknown-subcommand branch: %q", out)
	}
}
