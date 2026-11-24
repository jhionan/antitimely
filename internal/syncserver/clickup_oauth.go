package syncserver

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/rian/antitimely/internal/syncapi/v1"
)

const OAuthCallbackPath = "/clickup/oauth/callback"

type OAuthConfig struct {
	ClientID        string   `json:"client_id"`
	ClientSecret    string   `json:"client_secret"`
	RedirectURL     string   `json:"redirect_url"`
	SignInDeviceIDs []string `json:"sign_in_device_ids,omitempty"`
}

func (s *ClickUpService) ConfigureOAuth(config OAuthConfig) error {
	redirect, err := url.Parse(config.RedirectURL)
	if err != nil || redirect.Host == "" || redirect.Scheme != "https" || redirect.Path != OAuthCallbackPath ||
		redirect.User != nil || redirect.RawQuery != "" || redirect.Fragment != "" ||
		config.ClientID == "" || config.ClientSecret == "" {
		return errors.New("ClickUp OAuth requires a client ID, secret, and HTTPS callback ending in " + OAuthCallbackPath)
	}
	for _, device := range config.SignInDeviceIDs {
		if !validID.MatchString(device) {
			return errors.New("invalid ClickUp sign-in device ID")
		}
	}
	s.oauth = config
	return nil
}

func (s *ClickUpService) BeginOAuth(ctx context.Context, req *connect.Request[v1.BeginClickUpOAuthRequest]) (*connect.Response[v1.BeginClickUpOAuthResponse], error) {
	token, err := tokenHash(req.Header())
	if err != nil {
		return nil, err
	}
	var account string
	var device sql.NullString
	err = s.sync.db.QueryRowContext(ctx, `
		SELECT id, NULL FROM accounts WHERE token_hash = ?1
		UNION ALL SELECT account_id, id FROM devices WHERE token_hash = ?1 AND revoked = 0`, token).Scan(&account, &device)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("valid bearer token required"))
	}
	if err != nil {
		return nil, storageError(err)
	}
	if s.oauth.ClientID == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("ClickUp browser sign-in needs an OAuth app configured on the sync server"))
	}
	if device.Valid && !slices.Contains(s.oauth.SignInDeviceIDs, device.String) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("this device is not enabled for ClickUp sign-in"))
	}
	state := rand.Text()
	hash := sha256.Sum256([]byte(state))
	_, err = s.sync.db.ExecContext(ctx, `
		INSERT INTO clickup_oauth (account_id, hash, expires_unix, device_id) VALUES (?, ?, ?, ?)
		ON CONFLICT (account_id) DO UPDATE SET hash=excluded.hash,
			expires_unix=excluded.expires_unix, device_id=excluded.device_id`,
		account, hash[:], time.Now().Add(10*time.Minute).Unix(), device)
	if err != nil {
		return nil, storageError(err)
	}
	query := url.Values{"client_id": {s.oauth.ClientID}, "redirect_uri": {s.oauth.RedirectURL}, "state": {state}}
	return connect.NewResponse(&v1.BeginClickUpOAuthResponse{AuthorizationUrl: "https://app.clickup.com/api?" + query.Encode()}), nil
}

// The browser redirect is part of ClickUp's OAuth protocol. All tracker
// operations, including initiating OAuth, use the versioned RPC service.
func (s *ClickUpService) oauthCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if r.Method != http.MethodGet || s.oauth.ClientID == "" {
		http.NotFound(w, r)
		return
	}
	state, code := r.URL.Query().Get("state"), r.URL.Query().Get("code")
	if len(state) != 26 || code == "" || len(code) > 2048 {
		http.Error(w, "Invalid or denied ClickUp authorization.", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	hash := sha256.Sum256([]byte(state))
	var account string
	var device sql.NullString
	err := s.sync.db.QueryRowContext(ctx, `
		SELECT account_id, device_id FROM clickup_oauth WHERE hash = ? AND expires_unix > ?
		AND (device_id IS NULL OR EXISTS (SELECT 1 FROM devices WHERE id = device_id AND revoked = 0))`,
		hash[:], time.Now().Unix()).Scan(&account, &device)
	if err == nil && device.Valid && !slices.Contains(s.oauth.SignInDeviceIDs, device.String) {
		err = sql.ErrNoRows
	}
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "ClickUp authorization expired or was already used.", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, "Could not verify authorization.", http.StatusInternalServerError)
		return
	}
	token, err := s.provider.Exchange(ctx, s.oauth.ClientID, s.oauth.ClientSecret, code)
	if err != nil {
		http.Error(w, "ClickUp did not accept the authorization code.", http.StatusBadGateway)
		return
	}
	user, err := s.provider.User(ctx, token)
	if err != nil {
		http.Error(w, "Could not resolve the authorized ClickUp user.", http.StatusBadGateway)
		return
	}
	tx, err := s.sync.db.BeginTx(ctx, nil)
	if err != nil {
		http.Error(w, "Could not save the ClickUp connection.", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `DELETE FROM clickup_oauth WHERE hash = ? AND expires_unix > ?
		AND (device_id IS NULL OR EXISTS (SELECT 1 FROM devices WHERE id = device_id AND revoked = 0))`,
		hash[:], time.Now().Unix())
	if err == nil {
		var n int64
		n, err = result.RowsAffected()
		if err == nil && n != 1 {
			http.Error(w, "ClickUp authorization expired or was already used.", http.StatusBadRequest)
			return
		}
	}
	if err == nil {
		_, err = s.saveConnection(ctx, tx, account, user, token)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		http.Error(w, "Could not save the ClickUp connection.", http.StatusInternalServerError)
		return
	}
	fmt.Fprintln(w, "ClickUp is connected. You can close this page.")
}
