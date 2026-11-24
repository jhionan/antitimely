package syncclient

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/rian/antitimely/internal/syncapi/v1"
	"github.com/rian/antitimely/internal/syncapi/v1/syncv1connect"
)

//go:embed outbox.sql
var schema string

type DeviceCredential struct {
	DeviceID string `json:"device_id"`
	Token    string `json:"token"`
}

type Config struct {
	Endpoint, TokenFile string
}

type Worker struct {
	db     *sql.DB
	client syncv1connect.SyncServiceClient
	token  string
	device string
	mu     sync.Mutex
	status Status
}

type Status struct {
	Configured      bool
	Pending         int64
	LastSuccessUnix int64
	Error           string
}

var deviceIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
var tokenPattern = regexp.MustCompile(`^[A-Z2-7]{26}$`)

func ValidateEndpoint(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", errors.New("expected a sync server URL without credentials, query, or fragment")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && net.ParseIP(u.Hostname()).IsLoopback()) {
		return "", errors.New("sync requires HTTPS except for a literal loopback address")
	}
	return strings.TrimRight(u.String(), "/"), nil
}

func ReadCredential(filename string) (DeviceCredential, error) {
	var cred DeviceCredential
	body, err := os.ReadFile(filename)
	if err != nil {
		return cred, fmt.Errorf("read device credential: %w", err)
	}
	if err := json.Unmarshal(body, &cred); err != nil || !deviceIDPattern.MatchString(cred.DeviceID) || !tokenPattern.MatchString(cred.Token) {
		return DeviceCredential{}, errors.New("invalid device credential file")
	}
	return cred, nil
}

// Configure installs the outbox in the same database as ticks, so every queued
// change commits or rolls back with its source. The database remembers its
// original destination and device to prevent accidentally uploading to another
// account after a configuration edit.
func Configure(ctx context.Context, db *sql.DB, cfg Config, interval int) (*Worker, error) {
	if cfg.Endpoint == "" && cfg.TokenFile == "" {
		return nil, disable(ctx, db)
	}
	endpoint, err := ValidateEndpoint(cfg.Endpoint)
	if err != nil {
		return nil, errors.Join(err, disable(ctx, db))
	}
	if interval <= 0 || interval > 3600 {
		return nil, errors.Join(errors.New("sync interval must be between 1 and 3600 seconds"), disable(ctx, db))
	}
	cred, err := ReadCredential(cfg.TokenFile)
	if err != nil {
		return nil, errors.Join(err, disable(ctx, db))
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, schema); err != nil {
		return nil, err
	}
	var boundEndpoint, boundDevice string
	err = tx.QueryRowContext(ctx, `SELECT endpoint, device_id FROM sync_state WHERE singleton = 1`).Scan(&boundEndpoint, &boundDevice)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, err = tx.ExecContext(ctx, `INSERT INTO sync_state VALUES (1, ?, ?, 1, ?)`, endpoint, cred.DeviceID, interval)
	case err != nil:
		return nil, err
	case boundDevice != cred.DeviceID || boundEndpoint != endpoint:
		if _, err := tx.ExecContext(ctx, `UPDATE sync_state SET enabled = 0`); err != nil {
			return nil, err
		}
		return nil, errors.Join(errors.New("local history is already bound to another sync endpoint or device"), tx.Commit())
	default:
		_, err = tx.ExecContext(ctx, `UPDATE sync_state SET enabled = 1, interval = ?`, interval)
	}
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &Worker{db: db, client: NewClient(endpoint), token: cred.Token, device: cred.DeviceID, status: Status{Configured: true}}, nil
}

func disable(ctx context.Context, db *sql.DB) error {
	var exists bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE name = 'sync_state' AND type = 'table')`).Scan(&exists); err != nil || !exists {
		return err
	}
	_, err := db.ExecContext(ctx, `UPDATE sync_state SET enabled = 0`)
	return err
}

func NewClient(endpoint string) syncv1connect.SyncServiceClient {
	return syncv1connect.NewSyncServiceClient(HTTPClient(), endpoint, connect.WithReadMaxBytes(1024*1024))
}

func HTTPClient() *http.Client {
	return &http.Client{
		Timeout:       12 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func Authorize[T any](req *connect.Request[T], token string) *connect.Request[T] {
	req.Header().Set("Authorization", "Bearer "+token)
	return req
}

func (w *Worker) Status(ctx context.Context) (Status, error) {
	w.mu.Lock()
	status := w.status
	w.mu.Unlock()
	err := w.db.QueryRowContext(ctx, `SELECT count(*) FROM sync_outbox`).Scan(&status.Pending)
	return status, err
}

func (w *Worker) Run(ctx context.Context) {
	const idle = 10 * time.Second
	backoff := idle
	for ctx.Err() == nil {
		sent, err := w.Flush(ctx)
		if ctx.Err() != nil {
			return
		}
		delay := idle
		w.mu.Lock()
		switch {
		case err != nil:
			w.status.Error = err.Error()
			delay = backoff + time.Duration(rand.Int64N(int64(backoff/5)+1))
			backoff = min(2*backoff, 5*time.Minute)
		case sent != 0:
			w.status.LastSuccessUnix = time.Now().Unix()
			w.status.Error = ""
			delay = 0
			backoff = idle
		default:
			w.status.Error = ""
			backoff = idle
		}
		w.mu.Unlock()
		if err != nil {
			slog.Warn("sync batch retained", "rpc_code", connect.CodeOf(err), "retry_in", delay.Round(time.Second))
		}
		if delay == 0 {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}

func (w *Worker) Flush(ctx context.Context) (int, error) {
	const limit = 200
	rows, err := w.db.QueryContext(ctx, `
		SELECT seq, record_id, kind, name, project_id, ts, duration
		FROM sync_outbox ORDER BY seq LIMIT ?`, limit)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	req := &v1.PushRequest{DeviceId: w.device}
	var watermark int64
	for rows.Next() {
		r := &v1.Record{}
		var kind string
		project := &v1.Project{}
		slice := &v1.TimeSlice{}
		if err := rows.Scan(&r.Revision, &r.Id, &kind, &project.Name, &slice.ProjectId, &slice.StartUnix, &slice.DurationSeconds); err != nil {
			return 0, err
		}
		switch kind {
		case "project":
			r.Value = &v1.Record_Project{Project: project}
		case "time":
			r.Value = &v1.Record_TimeSlice{TimeSlice: slice}
		case "deleted":
			r.Value = &v1.Record_Tombstone{Tombstone: &v1.Tombstone{}}
		default:
			return 0, errors.New("invalid outbox record")
		}
		req.Records = append(req.Records, r)
		watermark = r.Revision
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if err := rows.Close(); err != nil || len(req.Records) == 0 {
		return 0, err
	}
	// Release the local database before contacting the server. New ticks can
	// continue to commit while this request is in flight or the server is down.
	if _, err := w.client.Push(ctx, Authorize(connect.NewRequest(req), w.token)); err != nil {
		return 0, err
	}
	if _, err := w.db.ExecContext(ctx, `DELETE FROM sync_outbox WHERE seq <= ?`, watermark); err != nil {
		return 0, err
	}
	return len(req.Records), nil
}
