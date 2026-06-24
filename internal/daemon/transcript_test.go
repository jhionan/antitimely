package daemon

import "testing"

func TestDecodeProjectDir(t *testing.T) {
	got := decodeProjectDir("-Users-rian-focaApp-bclouder-daas-daas--back--end")
	want := "/Users/rian/focaApp/bclouder/daas/daas-back-end"
	if got != want {
		t.Fatalf("decodeProjectDir = %q, want %q", got, want)
	}
}

func TestParseTranscriptTail(t *testing.T) {
	// Two real-ish entries + one junk line that must be skipped.
	data := []byte(`{"type":"user","cwd":"/Users/rian/daas","timestamp":"2026-06-24T01:55:35.000Z"}
not json — must be skipped
{"type":"assistant","cwd":"/Users/rian/daas","timestamp":"2026-06-24T02:30:29.000Z"}
`)
	cwd, newest, ok := parseTranscriptTail(data)
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if cwd != "/Users/rian/daas" {
		t.Fatalf("cwd = %q", cwd)
	}
	// 2026-06-24T02:30:29Z == 1782268229 unix.
	if newest != 1782268229 {
		t.Fatalf("newest = %d, want 1782268229", newest)
	}
}

func TestParseTranscriptTail_NoTimestamp(t *testing.T) {
	_, _, ok := parseTranscriptTail([]byte(`{"type":"summary"}` + "\n"))
	if ok {
		t.Fatal("ok = true, want false for entries without a timestamp")
	}
}
