package syncserver

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"connectrpc.com/connect"
	"github.com/rian/antitimely/internal/clickup"
	v1 "github.com/rian/antitimely/internal/syncapi/v1"
)

var validWorkspace = regexp.MustCompile(`^[0-9]{1,20}$`)

type clickUpProvider interface {
	CreateTimeEntry(context.Context, string, clickup.ID, clickup.TimeEntry) (clickup.ID, error)
	FindTimeEntry(context.Context, string, clickup.ID, clickup.TimeEntry) (clickup.ID, error)
	Exchange(context.Context, string, string, string) (string, error)
	User(context.Context, string) (clickup.User, error)
	Workspaces(context.Context, string) ([]clickup.Workspace, error)
	AssignedTasks(context.Context, string, clickup.ID, clickup.ID, int) ([]clickup.Task, bool, error)
	TaskDetails(context.Context, string, clickup.ID, string) (clickup.TaskDetails, error)
}

type ClickUpService struct {
	sync     *Service
	provider clickUpProvider
	aead     cipher.AEAD
	oauth    OAuthConfig
}

func NewClickUp(service *Service, key []byte) (*ClickUpService, error) {
	if len(key) != 32 {
		return nil, errors.New("ClickUp encryption key must contain 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, err
	}
	return &ClickUpService{sync: service, provider: clickup.New(), aead: aead}, nil
}

func (s *ClickUpService) ConnectPersonal(ctx context.Context, req *connect.Request[v1.ConnectPersonalRequest]) (*connect.Response[v1.ClickUpConnection], error) {
	account, err := s.sync.account(ctx, req.Header())
	if err != nil {
		return nil, err
	}
	token := strings.TrimSpace(req.Msg.Token)
	if !strings.HasPrefix(token, "pk_") || len(token) > 1024 || len(token) <= 3 {
		return nil, invalid("a ClickUp personal token is required")
	}
	user, err := s.provider.User(ctx, token)
	if err != nil {
		return nil, providerError(err)
	}
	out, err := s.saveConnection(ctx, s.sync.db, account, user, token)
	if err != nil {
		return nil, storageError(err)
	}
	return connect.NewResponse(out), nil
}

type sqlExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func (s *ClickUpService) saveConnection(ctx context.Context, db sqlExecer, account string, user clickup.User, token string) (*v1.ClickUpConnection, error) {
	id := newID()
	encrypted := s.aead.Seal(nil, nil, []byte(token), []byte(account+"/"+id))
	_, err := db.ExecContext(ctx, `
		INSERT INTO clickup_connections (account_id, id, user_id, username, token)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (account_id) DO UPDATE SET id=excluded.id, user_id=excluded.user_id,
			username=excluded.username, token=excluded.token`,
		account, id, user.ID, user.Username, encrypted)
	return &v1.ClickUpConnection{Connected: true, ConnectionId: id, Username: user.Username}, err
}

func (s *ClickUpService) Disconnect(ctx context.Context, req *connect.Request[v1.DisconnectClickUpRequest]) (*connect.Response[v1.DisconnectClickUpResponse], error) {
	account, err := s.sync.account(ctx, req.Header())
	if err != nil {
		return nil, err
	}
	tx, err := s.sync.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, storageError(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM clickup_oauth WHERE account_id = ?`, account); err != nil {
		return nil, storageError(err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM clickup_connections WHERE account_id = ?`, account); err != nil {
		return nil, storageError(err)
	}
	if err := tx.Commit(); err != nil {
		return nil, storageError(err)
	}
	return connect.NewResponse(&v1.DisconnectClickUpResponse{}), nil
}

func (s *ClickUpService) readAccount(ctx context.Context, h http.Header) (string, error) {
	// Either account or active device credentials can read the authorized
	// ClickUp task list. Only the account can change the connection.
	return authenticate(ctx, s.sync.db, h, `
		SELECT id FROM accounts WHERE token_hash = ?1
		UNION ALL SELECT account_id FROM devices WHERE token_hash = ?1 AND revoked = 0`)
}

func (s *ClickUpService) Connection(ctx context.Context, req *connect.Request[v1.ClickUpConnectionRequest]) (*connect.Response[v1.ClickUpConnection], error) {
	account, err := s.readAccount(ctx, req.Header())
	if err != nil {
		return nil, err
	}
	out := &v1.ClickUpConnection{}
	err = s.sync.db.QueryRowContext(ctx, `SELECT id, username FROM clickup_connections WHERE account_id = ?`, account).Scan(&out.ConnectionId, &out.Username)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, storageError(err)
	}
	out.Connected = err == nil
	return connect.NewResponse(out), nil
}

func (s *ClickUpService) connectionToken(ctx context.Context, h http.Header) (clickup.ID, string, error) {
	account, err := s.readAccount(ctx, h)
	if err != nil {
		return "", "", err
	}
	var id string
	var user clickup.ID
	var encrypted []byte
	err = s.sync.db.QueryRowContext(ctx, `
		SELECT id, user_id, token FROM clickup_connections WHERE account_id = ?`, account).Scan(&id, &user, &encrypted)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", connect.NewError(connect.CodeFailedPrecondition, errors.New("connect a ClickUp account first"))
	}
	if err != nil {
		return "", "", storageError(err)
	}
	plain, err := s.aead.Open(nil, nil, encrypted, []byte(account+"/"+id))
	if err != nil {
		return "", "", storageError(err)
	}
	return user, string(plain), nil
}

func (s *ClickUpService) ListWorkspaces(ctx context.Context, req *connect.Request[v1.ListClickUpWorkspacesRequest]) (*connect.Response[v1.ListClickUpWorkspacesResponse], error) {
	_, token, err := s.connectionToken(ctx, req.Header())
	if err != nil {
		return nil, err
	}
	workspaces, err := s.provider.Workspaces(ctx, token)
	if err != nil {
		return nil, providerError(err)
	}
	out := &v1.ListClickUpWorkspacesResponse{}
	for _, w := range workspaces {
		out.Workspaces = append(out.Workspaces, &v1.ClickUpWorkspace{Id: string(w.ID), Name: w.Name})
	}
	return connect.NewResponse(out), nil
}

func (s *ClickUpService) ListTasks(ctx context.Context, req *connect.Request[v1.ListClickUpTasksRequest]) (*connect.Response[v1.ListClickUpTasksResponse], error) {
	if req.Msg.Page < 0 || req.Msg.Page > 10_000 || !validWorkspace.MatchString(req.Msg.WorkspaceId) {
		return nil, invalid("invalid workspace or page")
	}
	user, token, err := s.connectionToken(ctx, req.Header())
	if err != nil {
		return nil, err
	}
	tasks, lastPage, err := s.provider.AssignedTasks(ctx, token, clickup.ID(req.Msg.WorkspaceId), user, int(req.Msg.Page))
	if err != nil {
		return nil, providerError(err)
	}
	out := &v1.ListClickUpTasksResponse{LastPage: lastPage}
	for _, t := range tasks {
		out.Tasks = append(out.Tasks, &v1.ClickUpTask{
			Id: string(t.ID), Name: t.Name, Url: t.URL, ParentId: string(t.Parent),
		})
	}
	return connect.NewResponse(out), nil
}

func providerError(err error) error {
	var problem *clickup.HTTPError
	if errors.As(err, &problem) {
		switch problem.Status {
		case http.StatusUnauthorized:
			return connect.NewError(connect.CodeFailedPrecondition, errors.New("ClickUp authorization expired or was revoked"))
		case http.StatusForbidden:
			return connect.NewError(connect.CodePermissionDenied, errors.New("ClickUp denied access"))
		case http.StatusNotFound:
			return connect.NewError(connect.CodeNotFound, errors.New("ClickUp resource unavailable"))
		case http.StatusTooManyRequests:
			return connect.NewError(connect.CodeResourceExhausted, errors.New("ClickUp rate limit reached"))
		}
	}
	return connect.NewError(connect.CodeUnavailable, errors.New("ClickUp did not return a usable response"))
}

// GenerateClickUpKey returns random key material for the server operator.
func GenerateClickUpKey() []byte {
	key := make([]byte, 32)
	rand.Read(key)
	return key
}

func (s *ClickUpService) TaskDetails(ctx context.Context, req *connect.Request[v1.ClickUpTaskDetailsRequest]) (*connect.Response[v1.ClickUpTaskDetailsResponse], error) {
	if !validTaskID.MatchString(req.Msg.TaskId) {
		return nil, invalid("invalid ClickUp task ID")
	}
	user, token, err := s.connectionToken(ctx, req.Header())
	if err != nil {
		return nil, err
	}
	task, err := s.provider.TaskDetails(ctx, token, user, req.Msg.TaskId)
	if err != nil {
		return nil, providerError(err)
	}
	out := &v1.ClickUpTaskDetailsResponse{Name: task.Name, Description: task.Description, Status: task.Status, Priority: task.Priority, DueUnixMillis: task.DueUnixMillis, Url: task.URL}
	for _, a := range task.Attachments {
		out.Attachments = append(out.Attachments, &v1.ClickUpAttachment{Name: a.Name, Url: a.URL, Image: a.Image})
	}
	for _, c := range task.Comments {
		out.Comments = append(out.Comments, &v1.ClickUpComment{Author: c.Author, Text: c.Text})
	}
	return connect.NewResponse(out), nil
}
