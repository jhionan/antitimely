package daemon

import (
	"context"
	"testing"

	"github.com/rian/antitimely/internal/herdr"
	"github.com/rian/antitimely/internal/macos"
)

// snapWithCwdPattern returns a CacheSnapshot tracking cwd via a plain cwd
// pattern (no space binding), with otherwise-empty maps so collectAgentSignals
// can be exercised directly without a full RunTick/ReloadCache round trip.
func snapWithCwdPattern(cwd string) *CacheSnapshot {
	return &CacheSnapshot{
		AllowedBundles:   map[string]bool{},
		AllowedBinaries:  map[string]bool{},
		PausedProjectIDs: map[int64]bool{},
		ArmedProjects:    map[int64]bool{},
		CwdPatterns:      []string{cwd},
		BoundSpaceIDs:    map[string]bool{},
	}
}

// snapWithBoundSpace returns a CacheSnapshot with NO cwd pattern that could
// match anything (so the cwd-match clause of track can never fire) and
// spaceID registered in BoundSpaceIDs, isolating the third "space-bound"
// clause of collectAgentSignals'/collectTranscriptSignals' track decision.
func snapWithBoundSpace(spaceID string) *CacheSnapshot {
	return &CacheSnapshot{
		AllowedBundles:   map[string]bool{},
		AllowedBinaries:  map[string]bool{},
		PausedProjectIDs: map[int64]bool{},
		ArmedProjects:    map[int64]bool{},
		CwdPatterns:      []string{"/no/such/tracked/dir"},
		BoundSpaceIDs:    map[string]bool{spaceID: true},
	}
}

func TestAgentSignalCarriesHerdrSpace(t *testing.T) {
	const cwd = "/repo/daas-back-end"
	p, br, _, db := newTestPipeline(t)
	defer db.Close()
	br.Processes = []macos.ProcessSample{{PID: 100, Name: "claude", CPUTicks: 0}}
	br.CWDByPID = map[int]string{100: cwd}
	br.EnvByPID = map[int]map[string]string{100: {"HERDR_PANE_ID": "wN:p1"}}
	p.herdr = herdr.NewResolver("../herdr/testdata/session.json")

	// First tick establishes the CPU baseline; second crosses the busy bar.
	p.collectAgentSignals(context.Background(), snapWithCwdPattern(cwd), true)
	br.Processes[0].CPUTicks = 10_000
	sigs := p.collectAgentSignals(context.Background(), snapWithCwdPattern(cwd), true)

	if len(sigs) != 1 {
		t.Fatalf("want 1 agent signal, got %d", len(sigs))
	}
	if sigs[0].SpaceID != "wN" {
		t.Fatalf("SpaceID = %q, want %q", sigs[0].SpaceID, "wN")
	}
}

func TestAgentSignalWithoutHerdrHasEmptySpace(t *testing.T) {
	const cwd = "/repo/daas-back-end"
	p, br, _, db := newTestPipeline(t)
	defer db.Close()
	br.Processes = []macos.ProcessSample{{PID: 101, Name: "claude", CPUTicks: 0}}
	br.CWDByPID = map[int]string{101: cwd}
	// no EnvByPID entry: process is not running under herdr
	p.herdr = herdr.NewResolver("../herdr/testdata/session.json")

	p.collectAgentSignals(context.Background(), snapWithCwdPattern(cwd), true)
	br.Processes[0].CPUTicks = 10_000
	sigs := p.collectAgentSignals(context.Background(), snapWithCwdPattern(cwd), true)

	if len(sigs) != 1 || sigs[0].SpaceID != "" {
		t.Fatalf("absent HERDR_PANE_ID must yield an empty SpaceID, got %+v", sigs)
	}
}

// TestAgentSignalSpaceBoundTracksWithoutCwdMatch is the regression test for
// the third track: clause in collectAgentSignals — a process whose cwd
// matches no pattern at all must still be tracked when its herdr space is
// rule-bound. That clause has no reason to exist if a matching CwdPatterns
// entry is always present, so this test deliberately supplies a
// CwdPatterns that cannot match the process's actual cwd.
func TestAgentSignalSpaceBoundTracksWithoutCwdMatch(t *testing.T) {
	const cwd = "/repo/daas-back-end" // does not match snapWithBoundSpace's CwdPatterns
	p, br, _, db := newTestPipeline(t)
	defer db.Close()
	br.Processes = []macos.ProcessSample{{PID: 102, Name: "claude", CPUTicks: 0}}
	br.CWDByPID = map[int]string{102: cwd}
	br.EnvByPID = map[int]map[string]string{102: {"HERDR_PANE_ID": "wN:p1"}}
	p.herdr = herdr.NewResolver("../herdr/testdata/session.json")

	snap := snapWithBoundSpace("wN")
	p.collectAgentSignals(context.Background(), snap, true)
	br.Processes[0].CPUTicks = 10_000
	sigs := p.collectAgentSignals(context.Background(), snap, true)

	if len(sigs) != 1 {
		t.Fatalf("want 1 agent signal from the space-bound clause (cwd matches nothing), got %d: %+v", len(sigs), sigs)
	}
	if sigs[0].SpaceID != "wN" {
		t.Fatalf("SpaceID = %q, want %q", sigs[0].SpaceID, "wN")
	}
}

// TestAgentSignalSpaceInvalidatedOnPIDReuse is the regression test for
// finding 1: PID reuse must clear the cached procSpace entry, not just
// procClass/procActivity, or a recycled pid keeps billing the old process's
// herdr space.
func TestAgentSignalSpaceInvalidatedOnPIDReuse(t *testing.T) {
	const cwd = "/repo/daas-back-end"
	p, br, _, db := newTestPipeline(t)
	defer db.Close()
	p.herdr = herdr.NewResolver("../herdr/testdata/session.json")
	br.CWDByPID = map[int]string{200: cwd}

	// Original process: claude in space wN.
	br.Processes = []macos.ProcessSample{{PID: 200, Name: "claude", CPUTicks: 0}}
	br.EnvByPID = map[int]map[string]string{200: {"HERDR_PANE_ID": "wN:p1"}}
	p.collectAgentSignals(context.Background(), snapWithCwdPattern(cwd), true)
	br.Processes[0].CPUTicks = 10_000
	sigs := p.collectAgentSignals(context.Background(), snapWithCwdPattern(cwd), true)
	if len(sigs) != 1 || sigs[0].SpaceID != "wN" {
		t.Fatalf("original process: want 1 signal with SpaceID wN, got %+v", sigs)
	}

	// Same pid reused by an unrelated process in a different space.
	br.Processes[0].Name = "node"
	br.Processes[0].CPUTicks = 20_000
	br.EnvByPID[200]["HERDR_PANE_ID"] = "wM:p1"
	sigs = p.collectAgentSignals(context.Background(), snapWithCwdPattern(cwd), true)
	if len(sigs) != 1 || sigs[0].SpaceID != "wM" {
		t.Fatalf("reused pid: want 1 signal with SpaceID wM (not stale wN), got %+v", sigs)
	}
}
