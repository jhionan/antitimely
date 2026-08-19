package cli

import (
	"testing"

	"github.com/rian/antitimely/internal/herdr"
	"github.com/rian/antitimely/internal/rpcapi"
)

// TestSpaceLabel covers the three display cases: a named workspace herdr
// still knows, an id it does not know (or that has no name), and no space at
// all. The bare id must survive as the fallback — dropping it would leave two
// review rows for the same cwd in two different clients' spaces looking
// identical, which is the whole reason the column exists.
func TestSpaceLabel(t *testing.T) {
	r := herdr.NewResolver("../herdr/testdata/session.json")

	if got, want := spaceLabel(r, "wN"), "MD-tracker (wN)"; got != want {
		t.Errorf("spaceLabel(known named space) = %q, want %q", got, want)
	}
	if got, want := spaceLabel(r, "wM"), "wM"; got != want {
		t.Errorf("spaceLabel(unnamed space) = %q, want %q", got, want)
	}
	if got, want := spaceLabel(r, "wZ"), "wZ"; got != want {
		t.Errorf("spaceLabel(unknown space) = %q, want %q", got, want)
	}
	if got := spaceLabel(r, ""); got != "" {
		t.Errorf("spaceLabel(no space) = %q, want empty", got)
	}
	if got, want := spaceLabel(nil, "wN"), "wN"; got != want {
		t.Errorf("spaceLabel with no resolver = %q, want the bare id %q", got, want)
	}
}

// TestDescribeSignatureShowsSpace pins that `atl review` renders the space
// for a signature that has one. Two observations differing only by space are
// two separate review rows, and can belong to two different clients; tagging
// the wrong one silently misbills.
func TestDescribeSignatureShowsSpace(t *testing.T) {
	r := herdr.NewResolver("../herdr/testdata/session.json")
	sig := rpcapi.Signature{
		Source:     "agent",
		BinaryName: "claude",
		CWD:        "/repo/daas-back-end",
		SpaceID:    "wN",
	}
	got := describeSignature(r, sig)
	if want := `binary="claude" cwd="/repo/daas-back-end" space=MD-tracker (wN)`; got != want {
		t.Fatalf("describeSignature = %q, want %q", got, want)
	}

	sig.SpaceID = ""
	if got, want := describeSignature(r, sig), `binary="claude" cwd="/repo/daas-back-end"`; got != want {
		t.Fatalf("a signature with no space must render unchanged: got %q, want %q", got, want)
	}
}
