package syncserver

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"connectrpc.com/connect"
	v1 "github.com/rian/antitimely/internal/syncapi/v1"
	"github.com/rian/antitimely/internal/syncapi/v1/syncv1connect"
)

func authorized[T any](msg *T, token string) *connect.Request[T] {
	req := connect.NewRequest(msg)
	req.Header().Set("Authorization", "Bearer "+token)
	return req
}

func TestRPCAuthenticationAndCrossDeviceTotals(t *testing.T) {
	ctx := context.Background()
	filename := filepath.Join(t.TempDir(), "sync.sqlite")
	db, err := Open(ctx, filename)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filename)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("server database permissions = %v", info.Mode().Perm())
		}
	}
	svc := New(db)
	host := httptest.NewServer(svc.Handler())
	defer host.Close()
	client := syncv1connect.NewSyncServiceClient(host.Client(), host.URL)
	if _, err := client.Health(ctx, connect.NewRequest(&v1.HealthRequest{})); err != nil {
		t.Fatal(err)
	}
	owner, err := svc.CreateAccount(ctx, "owner")
	if err != nil {
		t.Fatal(err)
	}
	stranger, err := svc.CreateAccount(ctx, "stranger")
	if err != nil {
		t.Fatal(err)
	}
	register := func(name, token string) *v1.RegisterDeviceResponse {
		t.Helper()
		res, err := client.RegisterDevice(ctx, authorized(&v1.RegisterDeviceRequest{Name: name}, token))
		if err != nil {
			t.Fatal(err)
		}
		return res.Msg
	}
	a := register("a", owner)
	b := register("b", owner)
	c := register("c", stranger)
	pid, tid := newID(), newID()
	push := func(device *v1.RegisterDeviceResponse, start int64) {
		t.Helper()
		msg := &v1.PushRequest{DeviceId: device.DeviceId, Records: []*v1.Record{
			{Id: pid, Revision: 1, Value: &v1.Record_Project{Project: &v1.Project{Name: "Project"}}},
			{Id: tid, Revision: 2, Value: &v1.Record_TimeSlice{TimeSlice: &v1.TimeSlice{ProjectId: pid, StartUnix: start, DurationSeconds: 5}}},
		}}
		for range 2 {
			if _, err := client.Push(ctx, authorized(msg, device.Token)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := client.Push(ctx, authorized(&v1.PushRequest{
		DeviceId: b.DeviceId,
		Records:  []*v1.Record{{Id: pid, Revision: 1, Value: &v1.Record_Project{Project: &v1.Project{Name: "Wrong device"}}}},
	}, a.Token)); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("device accepted a mismatched credential: %v", err)
	}
	push(a, 100)
	push(b, 102)
	push(c, 105)

	for _, device := range []*v1.RegisterDeviceResponse{a, b} {
		link := &v1.LinkProjectRequest{DeviceId: device.DeviceId, ProjectId: pid, GroupId: "shared"}
		if _, err := client.LinkProject(ctx, authorized(link, stranger)); connect.CodeOf(err) != connect.CodeNotFound {
			t.Fatalf("cross-account link: %v", err)
		}
		if _, err := client.LinkProject(ctx, authorized(link, owner)); err != nil {
			t.Fatal(err)
		}
	}
	total := &v1.TotalsRequest{StartUnix: 101, EndUnix: 110}
	if _, err := client.Totals(ctx, authorized(total, a.Token)); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("device could read totals: %v", err)
	}
	res, err := client.Totals(ctx, authorized(total, owner))
	if err != nil || res.Msg.TotalSeconds != 6 || len(res.Msg.Projects) != 1 || res.Msg.Projects[0].Key != "group:shared" || res.Msg.Projects[0].Seconds != 6 {
		t.Fatalf("unexpected totals %v (%v)", res, err)
	}

	revocation := &v1.RevokeDeviceRequest{DeviceId: a.DeviceId}
	if _, err := client.RevokeDevice(ctx, authorized(revocation, owner)); err != nil {
		t.Fatal(err)
	}
	edit := &v1.PushRequest{DeviceId: a.DeviceId, Records: []*v1.Record{{Id: tid, Revision: 3, Value: &v1.Record_Tombstone{Tombstone: &v1.Tombstone{}}}}}
	if _, err := client.Push(ctx, authorized(edit, a.Token)); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("revoked device could write: %v", err)
	}
}
