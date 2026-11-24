package syncserver

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"connectrpc.com/connect"
	v1 "github.com/rian/antitimely/internal/syncapi/v1"
	"github.com/rian/antitimely/internal/syncapi/v1/syncv1connect"
)

const (
	MaxBatch    = 200
	maxUnix     = int64(253402300799)
	maxDuration = 3600
)

var (
	validID    = regexp.MustCompile(`^[0-9a-f]{32}$`)
	validGroup = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
)

type Service struct{ db *sql.DB }

func New(db *sql.DB) *Service { return &Service{db: db} }

func (s *Service) Handler(extensions ...*ClickUpService) http.Handler {
	path, handler := syncv1connect.NewSyncServiceHandler(s,
		connect.WithReadMaxBytes(256*1024),
	)
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	for _, extension := range extensions {
		path, handler := syncv1connect.NewClickUpServiceHandler(extension, connect.WithReadMaxBytes(16*1024))
		mux.Handle(path, handler)
		mux.HandleFunc(OAuthCallbackPath, extension.oauthCallback)
	}
	return mux
}

func (s *Service) Health(ctx context.Context, _ *connect.Request[v1.HealthRequest]) (*connect.Response[v1.HealthResponse], error) {
	if err := s.db.PingContext(ctx); err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("database unavailable"))
	}
	return connect.NewResponse(&v1.HealthResponse{}), nil
}

// CreateAccount is an operator-only provisioning operation. Network clients
// can register devices using the returned account token.
func (s *Service) CreateAccount(ctx context.Context, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 120 {
		return "", errors.New("account name must contain 1 to 120 bytes")
	}
	token := rand.Text()
	hash := sha256.Sum256([]byte(token))
	_, err := s.db.ExecContext(ctx, `INSERT INTO accounts VALUES (?, ?, ?)`, newID(), name, hash[:])
	if err != nil {
		return "", fmt.Errorf("create account: %w", err)
	}
	return token, nil
}

func newID() string {
	var id [16]byte
	rand.Read(id[:])
	return hex.EncodeToString(id[:])
}

func tokenHash(h http.Header) ([]byte, error) {
	token, ok := strings.CutPrefix(h.Get("Authorization"), "Bearer ")
	if !ok || len(token) != 26 {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("valid bearer token required"))
	}
	hash := sha256.Sum256([]byte(token))
	return hash[:], nil
}

func authenticate(ctx context.Context, db *sql.DB, header http.Header, query string) (string, error) {
	hash, err := tokenHash(header)
	if err != nil {
		return "", err
	}
	var id string
	err = db.QueryRowContext(ctx, query, hash).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", connect.NewError(connect.CodeUnauthenticated, errors.New("valid bearer token required"))
	}
	if err != nil {
		return "", storageError(err)
	}
	return id, nil
}

func (s *Service) account(ctx context.Context, h http.Header) (string, error) {
	return authenticate(ctx, s.db, h, `SELECT id FROM accounts WHERE token_hash = ?`)
}

func (s *Service) RegisterDevice(ctx context.Context, req *connect.Request[v1.RegisterDeviceRequest]) (*connect.Response[v1.RegisterDeviceResponse], error) {
	account, err := s.account(ctx, req.Header())
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.Msg.Name)
	if name == "" || len(name) > 120 {
		return nil, invalid("device name must contain 1 to 120 bytes")
	}
	id, token := newID(), rand.Text()
	hash := sha256.Sum256([]byte(token))
	if _, err := s.db.ExecContext(ctx, `INSERT INTO devices (id, account_id, name, token_hash) VALUES (?, ?, ?, ?)`,
		id, account, name, hash[:]); err != nil {
		return nil, storageError(err)
	}
	return connect.NewResponse(&v1.RegisterDeviceResponse{DeviceId: id, Token: token}), nil
}

func (s *Service) RevokeDevice(ctx context.Context, req *connect.Request[v1.RevokeDeviceRequest]) (*connect.Response[v1.RevokeDeviceResponse], error) {
	account, err := s.account(ctx, req.Header())
	if err != nil {
		return nil, err
	}
	if !validID.MatchString(req.Msg.DeviceId) {
		return nil, invalid("invalid device ID")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE devices SET revoked = 1 WHERE account_id = ? AND id = ?`, account, req.Msg.DeviceId)
	if err != nil {
		return nil, storageError(err)
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("device not found"))
	}
	return connect.NewResponse(&v1.RevokeDeviceResponse{}), nil
}

func invalid(msg string) error {
	return connect.NewError(connect.CodeInvalidArgument, errors.New(msg))
}

func storageError(err error) error {
	slog.Error("sync database operation failed", "error", err)
	return connect.NewError(connect.CodeInternal, errors.New("database operation failed"))
}
