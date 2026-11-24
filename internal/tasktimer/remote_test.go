package tasktimer

import (
	"context"
	"database/sql"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/rian/antitimely/internal/syncapi/v1"
	"github.com/rian/antitimely/internal/syncapi/v1/syncv1connect"
)

type submissionServer struct {
	syncv1connect.UnimplementedClickUpServiceHandler
	online  atomic.Bool
	firstID atomic.Pointer[string]
}

func (s *submissionServer) SubmitTime(_ context.Context, req *connect.Request[v1.SubmitClickUpTimeRequest]) (*connect.Response[v1.ClickUpSubmission], error) {
	id := req.Msg.Id
	s.firstID.CompareAndSwap(nil, &id)
	if *s.firstID.Load() != id {
		return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("submission ID changed on retry"))
	}
	if !s.online.Load() {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("offline"))
	}
	return connect.NewResponse(&v1.ClickUpSubmission{
		State: v1.ClickUpSubmission_STATE_PUBLISHED, ProviderEntryId: "provider-entry",
	}), nil
}

func TestClickUpOutboxRetainsEntriesWhenOffline(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "tracker.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := Open(ctx, db, 5)
	if err != nil {
		t.Fatal(err)
	}
	task := Task{ConnectionID: strings.Repeat("a", 32), WorkspaceID: "1", TaskID: "task", Name: "Task"}
	if _, err := store.Switch(ctx, 100, &task); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Switch(ctx, 120, nil); err != nil {
		t.Fatal(err)
	}
	upstream := &submissionServer{}
	_, handler := syncv1connect.NewClickUpServiceHandler(upstream)
	server := httptest.NewServer(handler)
	defer server.Close()
	remote := &Remote{store: store, client: syncv1connect.NewClickUpServiceClient(server.Client(), server.URL), token: "test-token"}
	now := time.Unix(120, 0)
	if attempted, err := remote.FlushOne(ctx, now); err != nil || !attempted {
		t.Fatalf("first attempt: %v, %v", attempted, err)
	}
	entries, err := store.Entries(ctx, 1)
	if err != nil || len(entries) != 1 || entries[0].State != "queued" || !strings.Contains(entries[0].Message, "offline") {
		t.Fatalf("offline attempt lost entry: %+v, %v", entries, err)
	}
	if attempted, err := remote.FlushOne(ctx, now); attempted || err != nil {
		t.Fatalf("retried before backoff: %v, %v", attempted, err)
	}
	upstream.online.Store(true)
	if attempted, err := remote.FlushOne(ctx, now.Add(time.Minute)); err != nil || !attempted {
		t.Fatalf("retry: %v, %v", attempted, err)
	}
	entries, err = store.Entries(ctx, 1)
	if err != nil || len(entries) != 1 || entries[0].State != "published" || entries[0].ProviderEntryID != "provider-entry" {
		t.Fatalf("publishing result was not stored: %+v, %v", entries, err)
	}
}
