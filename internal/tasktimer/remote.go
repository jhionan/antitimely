package tasktimer

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/rian/antitimely/internal/syncapi/v1"
	"github.com/rian/antitimely/internal/syncapi/v1/syncv1connect"
	"github.com/rian/antitimely/internal/syncclient"
)

type Remote struct {
	store  *Store
	client syncv1connect.ClickUpServiceClient
	token  string
}

type TaskChoice struct {
	Task
	Workspace string
	ParentID  string
}

type Catalog struct {
	Connected bool
	Username  string
	Tasks     []TaskChoice
	Stale     bool
	Error     string
}

func NewRemote(store *Store, cfg syncclient.Config) (*Remote, error) {
	endpoint, err := syncclient.ValidateEndpoint(cfg.Endpoint)
	if err != nil {
		return nil, err
	}
	cred, err := syncclient.ReadCredential(cfg.TokenFile)
	if err != nil {
		return nil, err
	}
	return &Remote{
		store: store, client: syncv1connect.NewClickUpServiceClient(syncclient.HTTPClient(), endpoint,
			connect.WithReadMaxBytes(1024*1024)), token: cred.Token,
	}, nil
}

func (r *Remote) BeginClickUpSignIn(ctx context.Context) (string, error) {
	req := connect.NewRequest(&v1.BeginClickUpOAuthRequest{})
	reply, err := r.client.BeginOAuth(ctx, syncclient.Authorize(req, r.token))
	if err != nil {
		return "", err
	}
	return reply.Msg.AuthorizationUrl, nil
}

func (r *Remote) TaskDetails(ctx context.Context, taskID string) (*v1.ClickUpTaskDetailsResponse, error) {
	req := connect.NewRequest(&v1.ClickUpTaskDetailsRequest{TaskId: taskID})
	reply, err := r.client.TaskDetails(ctx, syncclient.Authorize(req, r.token))
	if err != nil {
		return nil, err
	}
	return reply.Msg, nil
}

func (r *Remote) Refresh(ctx context.Context) (Catalog, error) {
	catalog, err := r.fetchCatalog(ctx)
	if err != nil {
		read, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		snapshot, readErr := r.store.Catalog(read)
		snapshot.Stale = true
		snapshot.Error = err.Error()
		return snapshot, readErr
	}
	data, err := json.Marshal(catalog)
	if err != nil {
		return Catalog{}, err
	}
	_, err = r.store.db.ExecContext(ctx, `
		INSERT INTO clickup_catalog VALUES (1,?)
		ON CONFLICT(singleton) DO UPDATE SET data=excluded.data`, data)
	return catalog, err
}

func (s *Store) Catalog(ctx context.Context) (Catalog, error) {
	var raw []byte
	err := s.db.QueryRowContext(ctx, `SELECT data FROM clickup_catalog WHERE singleton=1`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return Catalog{}, nil
	}
	if err != nil {
		return Catalog{}, err
	}
	var catalog Catalog
	err = json.Unmarshal(raw, &catalog)
	return catalog, err
}

func (r *Remote) fetchCatalog(ctx context.Context) (Catalog, error) {
	connection, err := r.client.Connection(ctx, syncclient.Authorize(connect.NewRequest(&v1.ClickUpConnectionRequest{}), r.token))
	if err != nil {
		return Catalog{}, err
	}
	out := Catalog{Connected: connection.Msg.Connected, Username: connection.Msg.Username}
	if !out.Connected {
		return out, nil
	}
	workspaces, err := r.client.ListWorkspaces(ctx, syncclient.Authorize(connect.NewRequest(&v1.ListClickUpWorkspacesRequest{}), r.token))
	if err != nil {
		return out, err
	}
	for _, w := range workspaces.Msg.Workspaces {
		for page := int32(0); ; page++ {
			if page == 100 {
				return Catalog{}, fmt.Errorf("ClickUp workspace %s exceeds the task cache limit", w.Id)
			}
			req := connect.NewRequest(&v1.ListClickUpTasksRequest{WorkspaceId: w.Id, Page: page})
			response, err := r.client.ListTasks(ctx, syncclient.Authorize(req, r.token))
			if err != nil {
				return out, err
			}
			for _, t := range response.Msg.Tasks {
				task := Task{ConnectionID: connection.Msg.ConnectionId, WorkspaceID: w.Id, TaskID: t.Id, Name: t.Name}
				if task.valid() {
					out.Tasks = append(out.Tasks, TaskChoice{Task: task, Workspace: w.Name, ParentID: t.ParentId})
				}
			}
			if response.Msg.LastPage {
				break
			}
		}
	}
	// Do not replace the cache with tasks fetched across two different
	// connections if the owner reauthorized during pagination.
	check, err := r.client.Connection(ctx, syncclient.Authorize(connect.NewRequest(&v1.ClickUpConnectionRequest{}), r.token))
	if err != nil {
		return Catalog{}, err
	}
	if check.Msg.ConnectionId != connection.Msg.ConnectionId {
		return Catalog{}, errors.New("ClickUp connection changed during refresh")
	}
	return out, nil
}

func (r *Remote) Run(ctx context.Context) {
	for ctx.Err() == nil {
		sent, err := r.FlushOne(ctx, time.Now())
		if err != nil && ctx.Err() == nil {
			slog.Warn("ClickUp outbox unavailable", "error", err)
		}
		if sent && err == nil {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Second):
		}
	}
}

func (r *Remote) FlushOne(ctx context.Context, now time.Time) (bool, error) {
	e, err := r.store.Due(ctx, now.Unix())
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	req := connect.NewRequest(&v1.SubmitClickUpTimeRequest{
		Id: e.ID, ConnectionId: e.ConnectionID, WorkspaceId: e.WorkspaceID, TaskId: e.TaskID,
		StartUnix: e.Start, EndUnix: e.End,
	})
	res, err := r.client.SubmitTime(ctx, syncclient.Authorize(req, r.token))
	retry := now.Add(time.Minute).Unix()
	if err != nil {
		e.Message = err.Error()
		switch connect.CodeOf(err) {
		case connect.CodeInvalidArgument, connect.CodeAlreadyExists, connect.CodePermissionDenied:
			e.State = "failed"
		case connect.CodeFailedPrecondition, connect.CodeUnauthenticated:
			// A credential or connection issue needs an operator. Keep the
			// local entry visible without replaying it on a different identity.
			retry = now.Add(5 * time.Minute).Unix()
		}
	} else {
		e.Message = res.Msg.Message
		e.ProviderEntryID = res.Msg.ProviderEntryId
		if res.Msg.RetryAfterUnix != 0 {
			retry = max(now.Add(5*time.Second).Unix(), res.Msg.RetryAfterUnix)
		}
		switch res.Msg.State {
		case v1.ClickUpSubmission_STATE_PUBLISHED:
			e.State = "published"
		case v1.ClickUpSubmission_STATE_NEEDS_REVIEW:
			e.State = "needs_review"
		case v1.ClickUpSubmission_STATE_FAILED:
			e.State = "failed"
		default:
			e.State = "queued"
		}
	}
	return true, r.store.SetResult(ctx, e, retry)
}
