package macos

import "testing"

func TestParseEnvVar(t *testing.T) {
	out := "  PID TTY           TIME CMD\n" +
		"22260 ??         1:23.45 claude --resume FOO=notenv " +
		"HERDR_ENV=1 HERDR_PANE_ID=wN:p1 SHELL=/bin/zsh\n"
	if got := parseEnvVar(out, "HERDR_PANE_ID"); got != "wN:p1" {
		t.Fatalf("parseEnvVar = %q, want %q", got, "wN:p1")
	}
	if got := parseEnvVar(out, "HERDR_SOCKET_PATH"); got != "" {
		t.Fatalf("absent key must yield empty string, got %q", got)
	}
	if got := parseEnvVar("", "HERDR_PANE_ID"); got != "" {
		t.Fatalf("empty output must yield empty string, got %q", got)
	}

	// Documented limitation, not a bug: ps -Eww does not quote or escape
	// values, so a value containing whitespace is truncated at the first
	// space. This assertion pins that contract so a future change to the
	// parser has to consciously update it rather than silently regress.
	if got := parseEnvVar("SOMEVAR=foo bar", "SOMEVAR"); got != "foo" {
		t.Fatalf("value containing whitespace must be truncated at the space, got %q, want %q", got, "foo")
	}

	// A key that is a strict prefix of another key must not match the
	// longer key's token: the "key+\"=\"" construction (not "key" alone)
	// is what prevents FOO from matching inside FOOBAR=2.
	prefixOut := "FOO=1 FOOBAR=2"
	if got := parseEnvVar(prefixOut, "FOO"); got != "1" {
		t.Fatalf("parseEnvVar(%q, %q) = %q, want %q", prefixOut, "FOO", got, "1")
	}
	if got := parseEnvVar(prefixOut, "FOOBAR"); got != "2" {
		t.Fatalf("parseEnvVar(%q, %q) = %q, want %q", prefixOut, "FOOBAR", got, "2")
	}

	// A variable explicitly set to the empty string returns "" — the same
	// value returned for an absent variable. That ambiguity is the intended
	// contract (see ProcessEnvVarReal's doc comment), not tested here as an
	// error case.
	if got := parseEnvVar("EMPTY= NEXT=1", "EMPTY"); got != "" {
		t.Fatalf("empty value must yield empty string, got %q", got)
	}
}
