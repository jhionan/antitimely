package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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

// copySessionFixture writes the herdr test fixture to path, so a test can
// make session.json appear or disappear under a live resolver.
func copySessionFixture(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile("../herdr/testdata/session.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write session.json: %v", err)
	}
}

// TestAgentSpaceResolutionRecoversAfterSessionFileAppears is the regression
// test for the "a failed space RESOLUTION is cached for the process's
// lifetime" bug. Only the pane id (immutable after exec) may be cached; the
// pane -> space lookup reads herdr's session.json, which fails closed while
// the file is missing, mid-write, or not yet flushed for a freshly created
// space. Caching that failure pinned a long-lived agent to SpaceID="" for
// ever, so it fell through to the cwd rule and billed the wrong client.
func TestAgentSpaceResolutionRecoversAfterSessionFileAppears(t *testing.T) {
	const cwd = "/repo/daas-back-end"
	p, br, _, db := newTestPipeline(t)
	defer db.Close()

	// The resolver points at a session.json that does not exist yet: herdr
	// has created the space but has not persisted it.
	path := filepath.Join(t.TempDir(), "session.json")
	p.herdr = herdr.NewResolver(path)

	br.Processes = []macos.ProcessSample{{PID: 300, Name: "claude", CPUTicks: 0}}
	br.CWDByPID = map[int]string{300: cwd}
	br.EnvByPID = map[int]map[string]string{300: {"HERDR_PANE_ID": "wN:p1"}}
	snap := snapWithCwdPattern(cwd)

	p.collectAgentSignals(context.Background(), snap, true) // CPU baseline
	br.Processes[0].CPUTicks = 10_000
	sigs := p.collectAgentSignals(context.Background(), snap, true)
	if len(sigs) != 1 || sigs[0].SpaceID != "" {
		t.Fatalf("while session.json is absent the space must be unresolved, got %+v", sigs)
	}

	// herdr flushes its state; the very next tick must pick the space up.
	copySessionFixture(t, path)
	br.Processes[0].CPUTicks = 20_000
	sigs = p.collectAgentSignals(context.Background(), snap, true)
	if len(sigs) != 1 {
		t.Fatalf("want 1 agent signal, got %d: %+v", len(sigs), sigs)
	}
	if sigs[0].SpaceID != "wN" {
		t.Fatalf("SpaceID = %q, want %q: a failed pane->space resolution must be retried, "+
			"not cached for the life of the process", sigs[0].SpaceID, "wN")
	}
}

// TestAgentEnvProbedOncePerPID pins the other half of the caching contract:
// a SUCCESSFUL probe that finds no HERDR_PANE_ID is a real answer (the
// process is not running under herdr) and must be cached, so a non-herdr
// process is not re-probed with `ps -Eww` every 5 seconds.
func TestAgentEnvProbedOncePerPID(t *testing.T) {
	const cwd = "/repo/daas-back-end"
	p, br, _, db := newTestPipeline(t)
	defer db.Close()
	p.herdr = herdr.NewResolver("../herdr/testdata/session.json")
	br.Processes = []macos.ProcessSample{{PID: 301, Name: "claude", CPUTicks: 0}}
	br.CWDByPID = map[int]string{301: cwd}
	// no EnvByPID entry: the probe succeeds and finds nothing.
	snap := snapWithCwdPattern(cwd)

	p.collectAgentSignals(context.Background(), snap, true)
	for i := 1; i <= 4; i++ {
		br.Processes[0].CPUTicks = uint64(10_000 * i)
		if sigs := p.collectAgentSignals(context.Background(), snap, true); len(sigs) != 1 {
			t.Fatalf("tick %d: want 1 signal, got %+v", i, sigs)
		}
	}
	if br.EnvCalls != 1 {
		t.Fatalf("ProcessEnvVar called %d times; a successful probe that finds no "+
			"variable must be cached (one probe per pid)", br.EnvCalls)
	}
}

// TestAgentSpaceBoundSurvivesTransientEnvProbeFailure is the regression test
// for "procClass.track still caches a value derived from a failed env probe".
// procSpace already skipped caching on a probe error, but track was computed
// from that same empty space and cached anyway — so for a process tracked
// ONLY through its space binding (its cwd matches no pattern), one transient
// `ps -Eww` failure lost every subsequent tick, not merely the space.
func TestAgentSpaceBoundSurvivesTransientEnvProbeFailure(t *testing.T) {
	const cwd = "/repo/daas-back-end" // matches no pattern in snapWithBoundSpace
	p, br, _, db := newTestPipeline(t)
	defer db.Close()
	p.herdr = herdr.NewResolver("../herdr/testdata/session.json")
	br.Processes = []macos.ProcessSample{{PID: 302, Name: "claude", CPUTicks: 0}}
	br.CWDByPID = map[int]string{302: cwd}
	br.EnvByPID = map[int]map[string]string{302: {"HERDR_PANE_ID": "wN:p1"}}
	snap := snapWithBoundSpace("wN")

	p.collectAgentSignals(context.Background(), snap, true) // CPU baseline

	// The classifying tick — the one where the pid first crosses the busy
	// bar — hits a failed env probe.
	br.EnvErr = errors.New("ps: signal: killed")
	br.Processes[0].CPUTicks = 10_000
	if sigs := p.collectAgentSignals(context.Background(), snap, true); len(sigs) != 0 {
		t.Fatalf("a failed probe cannot resolve a space, so no signal is expected on that tick, got %+v", sigs)
	}

	// The probe recovers: the process must be reclassified and start emitting.
	br.EnvErr = nil
	br.Processes[0].CPUTicks = 20_000
	sigs := p.collectAgentSignals(context.Background(), snap, true)
	if len(sigs) != 1 {
		t.Fatalf("want 1 agent signal after the probe recovers, got %d: %+v — a classification "+
			"derived from a failed env probe must not be cached", len(sigs), sigs)
	}
	if sigs[0].SpaceID != "wN" {
		t.Fatalf("SpaceID = %q, want %q", sigs[0].SpaceID, "wN")
	}
}
