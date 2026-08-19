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
}
