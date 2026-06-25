package cli

import (
	"bytes"
	"strings"
	"testing"

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
