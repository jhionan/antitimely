package macos

import (
	"errors"
	"testing"
)

func TestClassifyOsascriptErr(t *testing.T) {
	tests := []struct {
		name       string
		in         string
		wantDenied bool
	}{
		{
			// The real message observed on a Spanish-locale machine: the
			// numeric code (-1728) is the locale-independent anchor.
			name:       "spanish assistive-access denial (-1728)",
			in:         "43:47: execution error: System Events ha detectado un error: osascript no tiene permitido el acceso de ayuda. (-1728)",
			wantDenied: true,
		},
		{
			// Observed live: accessing `windows of <proc>` via the AX API on a
			// non-trusted binary returns kAXErrorAPIDisabled in the local language.
			name:       "spanish AX-api-disabled denial (-25211)",
			in:         "(-25211) osascript no tiene permitido el acceso de ayuda.",
			wantDenied: true,
		},
		{
			name:       "english not-allowed-assistive-access (-1743)",
			in:         "execution error: System Events got an error: osascript is not allowed assistive access. (-1743)",
			wantDenied: true,
		},
		{
			name:       "english not authorized phrase without known code",
			in:         "execution error: Not authorized to send Apple events to System Events. (-1750)",
			wantDenied: true,
		},
		{
			name:       "real window title is not a denial",
			in:         "air — Main.kt",
			wantDenied: false,
		},
		{
			name:       "empty output is not a denial",
			in:         "",
			wantDenied: false,
		},
		{
			name:       "unrelated osascript error is not a denial",
			in:         "execution error: A descriptor type mismatch occurred. (-1703)",
			wantDenied: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := classifyOsascriptErr(tc.in)
			gotDenied := errors.Is(err, ErrAccessibilityDenied)
			if gotDenied != tc.wantDenied {
				t.Errorf("classifyOsascriptErr(%q) denied=%v (err=%v), want denied=%v", tc.in, gotDenied, err, tc.wantDenied)
			}
		})
	}
}

func TestParseTitleOutput(t *testing.T) {
	t.Run("normal title is trimmed and returned", func(t *testing.T) {
		got, err := parseTitleOutput("air — Main.kt\n")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "air — Main.kt" {
			t.Errorf("got %q, want %q", got, "air — Main.kt")
		}
	})

	t.Run("empty output means app has no readable title (no error)", func(t *testing.T) {
		got, err := parseTitleOutput("\n")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})

	t.Run("error sentinel with denial code surfaces ErrAccessibilityDenied", func(t *testing.T) {
		raw := titleErrPrefix + "-1728" + titleErrSep + "System Events ... osascript no tiene permitido el acceso de ayuda."
		got, err := parseTitleOutput(raw)
		if !errors.Is(err, ErrAccessibilityDenied) {
			t.Fatalf("got err=%v, want ErrAccessibilityDenied", err)
		}
		if got != "" {
			t.Errorf("title should be empty on error, got %q", got)
		}
	})

	t.Run("real live denial payload (-25211, spanish) is ErrAccessibilityDenied", func(t *testing.T) {
		// Captured verbatim from a Spanish-locale machine with Accessibility
		// not granted; osascript exits 0 and emits this on stdout.
		raw := titleErrPrefix + "-25211" + titleErrSep + "osascript no tiene permitido el acceso de ayuda.\n"
		got, err := parseTitleOutput(raw)
		if !errors.Is(err, ErrAccessibilityDenied) {
			t.Fatalf("got err=%v, want ErrAccessibilityDenied", err)
		}
		if got != "" {
			t.Errorf("title should be empty on denial, got %q", got)
		}
	})

	t.Run("error sentinel with non-denial code is a generic error", func(t *testing.T) {
		raw := titleErrPrefix + "-1703" + titleErrSep + "A descriptor type mismatch occurred."
		_, err := parseTitleOutput(raw)
		if err == nil {
			t.Fatal("expected an error")
		}
		if errors.Is(err, ErrAccessibilityDenied) {
			t.Error("non-denial code must not be classified as ErrAccessibilityDenied")
		}
	})
}
