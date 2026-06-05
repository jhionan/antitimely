package macos

import "testing"

func TestParseHIDIdleTime(t *testing.T) {
	// Captured from `ioreg -r -c IOHIDSystem -d 1` — the -r form whose output
	// the old `-c IOHIDSystem -d 1` command never produced (HIDIdleTime sits
	// below the depth-1 cap), which is why idle detection failed for weeks.
	const sample = `+-o IOHIDSystem  <class IOHIDSystem, id 0x100000abc, registered, matched, active, busy 0 (0 ms), retain 12>
    {
      "IOClass" = "IOHIDSystem"
      "HIDIdleTime" = 799678625000
      "IOProviderClass" = "IOResources"
    }
`
	got, err := parseHIDIdleTime(sample)
	if err != nil {
		t.Fatalf("parseHIDIdleTime: %v", err)
	}
	if got != 799 { // 799678625000 ns / 1e9 = 799s
		t.Errorf("idle seconds = %d, want 799", got)
	}
}

func TestParseHIDIdleTime_Missing(t *testing.T) {
	// The exact failure mode the depth bug produced: output without the
	// property must error, not silently return 0 (which reads as "active").
	if _, err := parseHIDIdleTime("no relevant property here\n"); err == nil {
		t.Fatal("expected error when HIDIdleTime absent, got nil")
	}
}

func TestParseHIDIdleTime_TrailingAnnotation(t *testing.T) {
	// Newer macOS can append a unit/annotation after the number.
	got, err := parseHIDIdleTime(`      "HIDIdleTime" = 5000000000 (ns)`)
	if err != nil {
		t.Fatalf("parseHIDIdleTime: %v", err)
	}
	if got != 5 {
		t.Errorf("idle seconds = %d, want 5", got)
	}
}
