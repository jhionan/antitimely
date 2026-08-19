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
