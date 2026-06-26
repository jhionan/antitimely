package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/rian/antitimely/internal/rpcapi"
)

func sampleReply() rpcapi.StatusReply {
	return rpcapi.StatusReply{
		UserIdleSeconds:     65,
		TickIntervalSeconds: 5,
		PermissionState:     "accessibility_denied",
		DaemonUptimeSeconds: 3600,
		TodayTotalSeconds:   7200,
		Companies: []rpcapi.CompanyTotals{
			{
				Name:            "BClouder",
				LastInvoiceUnix: 0,
				BillableSeconds: 3600,
				Projects: []rpcapi.ProjectTotals{
					{Name: "Daas", BillableSeconds: 3600, TodaySeconds: 3600},
					{Name: "Rumo", BillableSeconds: 0, TodaySeconds: 0, Paused: true},
					{Name: "VCNA", BillableSeconds: 120, TodaySeconds: 0, Armed: true, SuppressedSeconds: 300},
				},
			},
			{
				Name:            "(no company)",
				BillableSeconds: 600,
				Projects:        []rpcapi.ProjectTotals{{Name: "Solo", BillableSeconds: 600, TodaySeconds: 60}},
			},
		},
		UnassignedBillableSeconds: 900,
		UnassignedTodaySeconds:    90,
		UnassignedSignaturesCount: 3,
	}
}

func TestRenderStatusCoversBranches(t *testing.T) {
	var buf bytes.Buffer
	renderStatus(&buf, sampleReply())
	out := buf.String()
	for _, want := range []string{
		"Idle: 1m5s", "Tick: 5s", "Uptime: 1h0m0s",
		"Today: 2h0m0s total tracked",
		"BClouder", "Daas", "(paused)", "(armed: needs focus — 5m0s NOT counted!)",
		"(no company)", "Solo",
		"(unassigned)", "3 signature(s), run `antitimely review`",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("renderStatus output missing %q\n---\n%s", want, out)
		}
	}
	// renderStatus must NOT emit the warning (that is renderWarning's job).
	if strings.Contains(out, "Window-title capture disabled") {
		t.Error("renderStatus should not contain the accessibility warning")
	}
}

func TestRenderStatusEmpty(t *testing.T) {
	var buf bytes.Buffer
	renderStatus(&buf, rpcapi.StatusReply{TickIntervalSeconds: 5})
	if !strings.Contains(buf.String(), "(no time tracked yet)") {
		t.Errorf("expected empty-state line, got:\n%s", buf.String())
	}
}

func TestRenderWarningOnlyWhenDenied(t *testing.T) {
	var denied, ok bytes.Buffer
	renderWarning(&denied, rpcapi.StatusReply{PermissionState: "accessibility_denied"})
	renderWarning(&ok, rpcapi.StatusReply{PermissionState: "ok"})
	if !strings.Contains(denied.String(), "Window-title capture disabled") {
		t.Error("expected warning when accessibility_denied")
	}
	if ok.String() != "" {
		t.Errorf("expected no warning when ok, got: %q", ok.String())
	}
}

func TestRenderFooter(t *testing.T) {
	var buf bytes.Buffer
	renderFooter(&buf, time.Date(2026, 6, 25, 20, 36, 1, 0, time.Local))
	out := buf.String()
	for _, want := range []string{"live", "every 5s", "Esc to exit", "20:36:01"} {
		if !strings.Contains(out, want) {
			t.Errorf("footer missing %q, got: %q", want, out)
		}
	}
}

func TestStatusBodyChanged(t *testing.T) {
	cases := []struct {
		name            string
		lastTs, curTs   int64
		lastDay, curDay int
		probeErr        bool
		want            bool
	}{
		{"first cycle (lastTs<0)", -1, 1782484260, 0, 20260626, false, true},
		{"unchanged: same ts, same day", 100, 100, 20260626, 20260626, false, false},
		{"new tick recorded", 100, 105, 20260626, 20260626, false, true},
		{"day rollover with same ts", 100, 100, 20260625, 20260626, false, true},
		{"probe error forces a full fetch", 100, 100, 20260626, 20260626, true, true},
	}
	for _, c := range cases {
		if got := statusBodyChanged(c.lastTs, c.curTs, c.lastDay, c.curDay, c.probeErr); got != c.want {
			t.Errorf("%s: statusBodyChanged = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestLocalDayKey(t *testing.T) {
	end := localDayKey(time.Date(2026, 6, 26, 23, 59, 59, 0, time.Local))
	start := localDayKey(time.Date(2026, 6, 26, 0, 0, 0, 0, time.Local))
	next := localDayKey(time.Date(2026, 6, 27, 0, 0, 0, 0, time.Local))
	if end != 20260626 {
		t.Errorf("localDayKey = %d, want 20260626", end)
	}
	if start != end {
		t.Errorf("same calendar day must yield same key: %d vs %d", start, end)
	}
	if next == end {
		t.Error("next day must differ")
	}
}
