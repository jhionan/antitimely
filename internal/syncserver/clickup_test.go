package syncserver

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/rian/antitimely/internal/clickup"
	v1 "github.com/rian/antitimely/internal/syncapi/v1"
	"github.com/rian/antitimely/internal/syncapi/v1/syncv1connect"
)

type fakeClickUp struct {
	posts     int
	exchanges int
	postError error
	findID    clickup.ID
}

func (f *fakeClickUp) User(_ context.Context, token string) (clickup.User, error) {
	if token == "pk_revoked" {
		return clickup.User{}, &clickup.HTTPError{Status: 401}
	}
	return clickup.User{ID: "52", Username: "Ada"}, nil
}
func (f *fakeClickUp) Workspaces(context.Context, string) ([]clickup.Workspace, error) {
	return []clickup.Workspace{{ID: "99", Name: "Partner"}}, nil
}
func (f *fakeClickUp) AssignedTasks(_ context.Context, token string, team, user clickup.ID, page int) ([]clickup.Task, bool, error) {
	if token != "pk_test" || team != "99" || user != "52" || page != 1 {
		return nil, false, errors.New("wrong ClickUp scope")
	}
	return []clickup.Task{{ID: "abc", Name: "Task", Parent: "parent"}}, true, nil
}
func (f *fakeClickUp) Exchange(_ context.Context, id, secret, code string) (string, error) {
	f.exchanges++
	if id != "client" || secret != "secret" || code != "auth-code" {
		return "", errors.New("wrong exchange")
	}
	return "oauth-secret", nil
}
func (f *fakeClickUp) CreateTimeEntry(_ context.Context, _ string, _ clickup.ID, _ clickup.TimeEntry) (clickup.ID, error) {
	f.posts++
	if f.postError != nil {
		return "", f.postError
	}
	return "new-entry", nil
}
func (f *fakeClickUp) FindTimeEntry(context.Context, string, clickup.ID, clickup.TimeEntry) (clickup.ID, error) {
	return f.findID, nil
}

func TestClickUpRPCConnectOAuthAndReconcile(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "clickup.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := New(db)
	owner, err := s.CreateAccount(ctx, "owner")
	if err != nil {
		t.Fatal(err)
	}
	cu, err := NewClickUp(s, GenerateClickUpKey())
	if err != nil {
		t.Fatal(err)
	}
	if err := cu.ConfigureOAuth(OAuthConfig{
		ClientID: "client", ClientSecret: "secret", RedirectURL: "https://example.test" + OAuthCallbackPath,
	}); err != nil {
		t.Fatal(err)
	}
	provider := &fakeClickUp{}
	cu.provider = provider
	host := httptest.NewServer(s.Handler(cu))
	defer host.Close()
	client := syncv1connect.NewClickUpServiceClient(host.Client(), host.URL)
	syncClient := syncv1connect.NewSyncServiceClient(host.Client(), host.URL)
	device, err := syncClient.RegisterDevice(ctx, authorized(&v1.RegisterDeviceRequest{Name: "laptop"}, owner))
	if err != nil {
		t.Fatal(err)
	}
	d := device.Msg.Token
	personal := &v1.ConnectPersonalRequest{Token: "pk_test"}
	if _, err := client.ConnectPersonal(ctx, authorized(personal, d)); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("device could connect provider: %v", err)
	}
	if _, err := client.ConnectPersonal(ctx, authorized(&v1.ConnectPersonalRequest{Token: "pk_revoked"}, owner)); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("revoked credential was accepted: %v", err)
	}
	connected, err := client.ConnectPersonal(ctx, authorized(personal, owner))
	if err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := db.QueryRow(`SELECT token FROM clickup_connections`).Scan(&raw); err != nil || bytes.Contains(raw, []byte(personal.Token)) {
		t.Fatalf("plaintext token or missing row: %v", err)
	}
	res, err := client.ListTasks(ctx, authorized(&v1.ListClickUpTasksRequest{WorkspaceId: "99", Page: 1}, d))
	if err != nil || len(res.Msg.Tasks) != 1 || res.Msg.Tasks[0].ParentId != "parent" || !res.Msg.LastPage {
		t.Fatalf("bad tasks %v, %v", res, err)
	}

	taskDetails := &v1.ClickUpTaskDetailsRequest{TaskId: "abc"}
	if _, err := client.TaskDetails(ctx, connect.NewRequest(taskDetails)); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("unauthenticated task details: %v", err)
	}
	detail, err := client.TaskDetails(ctx, authorized(taskDetails, d))
	if err != nil || detail.Msg.Description != "Sample description" || len(detail.Msg.Attachments) != 1 || len(detail.Msg.Comments) != 1 {
		t.Fatalf("wrong task details: %v (%v)", detail, err)
	}

	entry := &v1.SubmitClickUpTimeRequest{Id: newID(), ConnectionId: connected.Msg.ConnectionId,
		WorkspaceId: "99", TaskId: "abc", StartUnix: 100, EndUnix: 110}
	provider.postError = &clickup.HTTPError{Status: 502}
	submit := func(msg *v1.SubmitClickUpTimeRequest, v v1.ClickUpSubmission_State) *v1.ClickUpSubmission {
		t.Helper()
		result, err := client.SubmitTime(ctx, authorized(msg, d))
		if err != nil || result.Msg.State != v {
			t.Fatalf("want %v, got %v, %v", v, result, err)
		}
		return result.Msg
	}
	submit(entry, v1.ClickUpSubmission_STATE_NEEDS_REVIEW)
	submit(entry, v1.ClickUpSubmission_STATE_NEEDS_REVIEW)
	overlap := &v1.SubmitClickUpTimeRequest{Id: newID(), ConnectionId: entry.ConnectionId, WorkspaceId: "99", TaskId: "other", StartUnix: 105, EndUnix: 115}
	if _, err := client.SubmitTime(ctx, authorized(overlap, d)); connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Fatalf("overlap accepted: %v", err)
	}
	if _, err := db.Exec(`UPDATE clickup_submissions SET retry_at=0`); err != nil {
		t.Fatal(err)
	}
	provider.findID = "original-entry"
	if result := submit(entry, v1.ClickUpSubmission_STATE_PUBLISHED); result.ProviderEntryId != "original-entry" || provider.posts != 1 {
		t.Fatalf("ambiguous write retried or entry lost: %v, posts %d", result, provider.posts)
	}
	provider.postError = &clickup.HTTPError{Status: 429, RetryAfter: 2 * time.Minute}
	next := &v1.SubmitClickUpTimeRequest{Id: newID(), ConnectionId: entry.ConnectionId, WorkspaceId: "99", TaskId: "abc", StartUnix: 110, EndUnix: 120}
	if result := submit(next, v1.ClickUpSubmission_STATE_QUEUED); result.RetryAfterUnix < time.Now().Add(time.Minute).Unix() {
		t.Fatal("retry-after ignored")
	}
	submit(next, v1.ClickUpSubmission_STATE_QUEUED)
	if provider.posts != 2 {
		t.Fatalf("rate-limited request retried early, posts %d", provider.posts)
	}
	if _, err := db.Exec(`UPDATE clickup_submissions SET retry_at=0 WHERE id=?`, next.Id); err != nil {
		t.Fatal(err)
	}
	provider.postError = nil
	submit(next, v1.ClickUpSubmission_STATE_PUBLISHED)
	if provider.posts != 3 {
		t.Fatalf("definitively rejected request was not retried, posts %d", provider.posts)
	}

	flow, err := client.BeginOAuth(ctx, authorized(&v1.BeginClickUpOAuthRequest{}, owner))
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(flow.Msg.AuthorizationUrl)
	if err != nil {
		t.Fatal(err)
	}
	state := u.Query().Get("state")
	checkCallback := func(query string, expected int) {
		t.Helper()
		response, err := host.Client().Get(host.URL + OAuthCallbackPath + query)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != expected {
			t.Fatalf("callback = %d, want %d", response.StatusCode, expected)
		}
	}
	checkCallback("?code=auth-code&state=bad", http.StatusBadRequest)
	checkCallback("?code=auth-code&state="+url.QueryEscape(state), http.StatusOK)
	checkCallback("?code=auth-code&state="+url.QueryEscape(state), http.StatusBadRequest)
	if provider.exchanges != 1 {
		t.Fatalf("state exchanged %d times", provider.exchanges)
	}
	if _, err := client.Disconnect(ctx, authorized(&v1.DisconnectClickUpRequest{}, owner)); err != nil {
		t.Fatal(err)
	}
	stateReply, err := client.Connection(ctx, authorized(&v1.ClickUpConnectionRequest{}, d))
	if err != nil || stateReply.Msg.Connected {
		t.Fatalf("still connected %v, %v", stateReply, err)
	}
}

func TestClickUpBrowserSignInRequiresEnabledDevice(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "oauth.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := New(db)
	owner, err := s.CreateAccount(ctx, "owner")
	if err != nil {
		t.Fatal(err)
	}
	register := func(name string) *v1.RegisterDeviceResponse {
		t.Helper()
		res, err := s.RegisterDevice(ctx, authorized(&v1.RegisterDeviceRequest{Name: name}, owner))
		if err != nil {
			t.Fatal(err)
		}
		return res.Msg
	}
	enabled, ordinary := register("enabled"), register("ordinary")
	cu, err := NewClickUp(s, GenerateClickUpKey())
	if err != nil {
		t.Fatal(err)
	}
	if err := cu.ConfigureOAuth(OAuthConfig{
		ClientID: "client", ClientSecret: "secret", RedirectURL: "https://example.test" + OAuthCallbackPath,
		SignInDeviceIDs: []string{enabled.DeviceId},
	}); err != nil {
		t.Fatal(err)
	}
	provider := &fakeClickUp{}
	cu.provider = provider
	begin := func(token string) (*connect.Response[v1.BeginClickUpOAuthResponse], error) {
		return cu.BeginOAuth(ctx, authorized(&v1.BeginClickUpOAuthRequest{}, token))
	}
	if _, err := begin(ordinary.Token); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("ordinary device could initiate browser sign-in: %v", err)
	}
	flow, err := begin(enabled.Token)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(flow.Msg.AuthorizationUrl)
	if err != nil || u.Host != "app.clickup.com" || u.Query().Get("state") == "" {
		t.Fatalf("invalid sign-in link: %v", err)
	}
	if _, err := s.RevokeDevice(ctx, authorized(&v1.RevokeDeviceRequest{DeviceId: enabled.DeviceId}, owner)); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, OAuthCallbackPath+"?code=auth-code&state="+url.QueryEscape(u.Query().Get("state")), nil)
	reply := httptest.NewRecorder()
	cu.oauthCallback(reply, req)
	if reply.Code != http.StatusBadRequest || provider.exchanges != 0 {
		t.Fatalf("revoked device authorization reached ClickUp: HTTP %d, exchanges %d", reply.Code, provider.exchanges)
	}
	if _, err := begin(owner); err != nil {
		t.Fatalf("account owner lost sign-in access: %v", err)
	}
}

func (f *fakeClickUp) TaskDetails(_ context.Context, token string, user clickup.ID, id string) (clickup.TaskDetails, error) {
	if token != "pk_test" || user != "52" || id != "abc" {
		return clickup.TaskDetails{}, errors.New("wrong task details scope")
	}
	return clickup.TaskDetails{
		Name: "Task", Description: "Sample description",
		Attachments: []clickup.Attachment{{Name: "sample.png", URL: "https://attachments.clickup.com/sample.png", Image: true}},
		Comments:    []clickup.Comment{{Author: "Ada", Text: "Sample comment"}},
	}, nil
}
