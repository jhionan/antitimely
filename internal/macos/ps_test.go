package macos

import (
	"strings"
	"testing"
)

func TestParseCPUTime(t *testing.T) {
	tests := []struct {
		in   string
		want uint64
	}{
		{"0:00.00", 0},
		{"0:01.50", 150},
		{"1:23.45", 8345},
		{"1:00:00", 360000},
	}
	for _, tc := range tests {
		got, err := parseCPUTime(tc.in)
		if err != nil {
			t.Errorf("parseCPUTime(%q) error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseCPUTime(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// ps pads every column but the last to a fixed width, so comm must come last:
// an earlier comm is cut at 16 bytes ("/Users/rian/Libr"), which turned a
// Claude desktop-app agent into binary "Libr" and dropped it off the
// "claude" allowlist.
func TestParsePSLine(t *testing.T) {
	tests := []struct {
		line     string
		wantPID  int
		wantName string
		wantCS   uint64
	}{
		{"84454   2:32.03 /Users/rian/Library/Application Support/Claude/claude-code/2.1.280/claude.app/Contents/MacOS/claude", 84454, "claude", 15203},
		{"59116   0:06.81 /opt/homebrew/Cellar/dotnet/10.0.400/libexec/dotnet", 59116, "dotnet", 681},
		{"  1 292:55.32 /sbin/launchd", 1, "launchd", 1757532},
		{"4242   0:00.10 zsh", 4242, "zsh", 10},
	}
	for _, tc := range tests {
		got, ok := parsePSLine(tc.line)
		if !ok {
			t.Errorf("parsePSLine(%q) rejected", tc.line)
			continue
		}
		if got.PID != tc.wantPID || got.Name != tc.wantName || got.CPUTicks != tc.wantCS {
			t.Errorf("parsePSLine(%q) = %+v, want pid=%d name=%q cs=%d", tc.line, got, tc.wantPID, tc.wantName, tc.wantCS)
		}
	}
	for _, bad := range []string{"", "abc 0:01.00 zsh", "123 notatime zsh", "123 0:01.00"} {
		if got, ok := parsePSLine(bad); ok {
			t.Errorf("parsePSLine(%q) = %+v, want rejected", bad, got)
		}
	}
}

func TestBinaryName(t *testing.T) {
	long := "npm exec chrome-devtools-mcp@latest --autoConnect --no-usage-statistics --no-performance-crux"
	tests := []struct{ in, want string }{
		{"/opt/homebrew/Cellar/dotnet/10.0.400/libexec/dotnet", "dotnet"},
		{"/Users/rian/Library/Application Support/Claude/claude-code/2.1.280/claude.app/Contents/MacOS/claude", "claude"},
		{"./antitimely", "antitimely"},
		{"zsh", "zsh"},
		{"-zsh", "-zsh"},
		// Titles stay whole: Base would have made this "mcp@latest".
		{"npm exec @playwright/mcp@latest", "npm exec @playwright/mcp@latest"},
		{"ng serve --port 4200", "ng serve --port 4200"},
		{long, long[:maxTitleLen]},
		// A cut landing inside a multi-byte rune backs off to its start.
		{strings.Repeat("a", maxTitleLen-1) + "étude", strings.Repeat("a", maxTitleLen-1)},
	}
	for _, tc := range tests {
		if got := binaryName(tc.in); got != tc.want {
			t.Errorf("binaryName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
