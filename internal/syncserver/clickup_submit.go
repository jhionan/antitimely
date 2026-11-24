package syncserver

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"regexp"
	"time"

	"connectrpc.com/connect"
	"github.com/rian/antitimely/internal/clickup"
	v1 "github.com/rian/antitimely/internal/syncapi/v1"
)

var validTaskID = regexp.MustCompile(`^[0-9A-Za-z_-]{1,120}$`)

type submission struct {
	state, entryID, message string
	retryAt                 int64
}

func (s *ClickUpService) SubmitTime(ctx context.Context, req *connect.Request[v1.SubmitClickUpTimeRequest]) (*connect.Response[v1.ClickUpSubmission], error) {
	account, err := s.readAccount(ctx, req.Header())
	if err != nil {
		return nil, err
	}
	msg := req.Msg
	now := time.Now()
	if !validID.MatchString(msg.Id) || !validID.MatchString(msg.ConnectionId) ||
		!validWorkspace.MatchString(msg.WorkspaceId) || !validTaskID.MatchString(msg.TaskId) ||
		msg.StartUnix < 1 || msg.EndUnix <= msg.StartUnix || msg.EndUnix > now.Add(5*time.Minute).Unix() ||
		msg.EndUnix-msg.StartUnix > 24*60*60 {
		return nil, invalid("invalid ClickUp entry or duration")
	}
	prior, token, err := s.claimSubmission(ctx, account, msg, now)
	if err != nil {
		return nil, err
	}
	if token == "" {
		return prior.response(), nil
	}
	entry := clickup.TimeEntry{
		TaskID: msg.TaskId, Start: msg.StartUnix * 1000, Duration: (msg.EndUnix - msg.StartUnix) * 1000,
		// A persistent marker allows reconciliation if the upstream accepted the
		// time entry but the response or our local commit was lost.
		Description: "Antitimely ref " + account + "/" + msg.Id,
	}
	workspace := clickup.ID(msg.WorkspaceId)
	var next submission
	if prior.state == "queued" {
		id, err := s.provider.CreateTimeEntry(ctx, token, workspace, entry)
		next = classifySubmission(id, err, now)
	} else {
		// A timeout or server restart may have happened after ClickUp committed.
		// Only reconcile these attempts. Absence in a list response cannot
		// establish that it is safe to submit again.
		id, err := s.provider.FindTimeEntry(ctx, token, workspace, entry)
		if err == nil && id != "" {
			next = submission{state: "published", entryID: string(id)}
		} else {
			next = submission{state: "needs_review", message: "ClickUp creation is unconfirmed", retryAt: now.Add(time.Minute).Unix()}
		}
	}
	// An HTTP client can disconnect immediately after the upstream write.
	// Persist the result even if the original request context is cancelled.
	save, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if _, err := s.sync.db.ExecContext(save, `
		UPDATE clickup_submissions SET state=?, entry_id=?, message=?, retry_at=?
		WHERE account_id=? AND id=? AND state='sending'`,
		next.state, next.entryID, next.message, next.retryAt, account, msg.Id); err != nil {
		return nil, storageError(err)
	}
	return next.response(), nil
}

func (s *ClickUpService) claimSubmission(ctx context.Context, account string, msg *v1.SubmitClickUpTimeRequest, now time.Time) (submission, string, error) {
	tx, err := s.sync.db.BeginTx(ctx, nil)
	if err != nil {
		return submission{}, "", storageError(err)
	}
	defer tx.Rollback()
	var out submission
	var connection, workspace, task string
	var start, end int64
	err = tx.QueryRowContext(ctx, `
		SELECT connection_id,workspace_id,task_id,start_unix,end_unix,state,entry_id,message,retry_at
		FROM clickup_submissions WHERE account_id=? AND id=?`, account, msg.Id).Scan(
		&connection, &workspace, &task, &start, &end, &out.state, &out.entryID, &out.message, &out.retryAt)
	if err == nil {
		if connection != msg.ConnectionId || workspace != msg.WorkspaceId || task != msg.TaskId ||
			start != msg.StartUnix || end != msg.EndUnix {
			return out, "", connect.NewError(connect.CodeAlreadyExists, errors.New("submission ID belongs to a different time entry"))
		}
		if out.state == "published" || out.state == "failed" || out.retryAt > now.Unix() {
			return out, "", nil
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return out, "", storageError(err)
	}

	var sealed []byte
	err = tx.QueryRowContext(ctx, `SELECT token FROM clickup_connections WHERE account_id=? AND id=?`, account, msg.ConnectionId).Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return out, "", connect.NewError(connect.CodeFailedPrecondition, errors.New("ClickUp connection changed or was disconnected"))
	}
	if err != nil {
		return out, "", storageError(err)
	}
	secret, err := s.aead.Open(nil, nil, sealed, []byte(account+"/"+msg.ConnectionId))
	if err != nil {
		return out, "", storageError(err)
	}
	next := now.Add(35 * time.Second).Unix()
	if out.state == "" {
		var overlap bool
		err = tx.QueryRowContext(ctx, `
			SELECT EXISTS(SELECT 1 FROM clickup_submissions WHERE account_id=?
				AND start_unix < ? AND end_unix > ? AND state <> 'failed')`,
			account, msg.EndUnix, msg.StartUnix).Scan(&overlap)
		if err != nil {
			return out, "", storageError(err)
		}
		if overlap {
			return out, "", connect.NewError(connect.CodeAlreadyExists, errors.New("selected time overlaps a ClickUp submission"))
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO clickup_submissions (account_id,id,connection_id,workspace_id,task_id,start_unix,end_unix,state,retry_at)
			VALUES (?,?,?,?,?,?,?,'sending',?)`,
			account, msg.Id, msg.ConnectionId, msg.WorkspaceId, msg.TaskId, msg.StartUnix, msg.EndUnix, next)
		out.state = "queued"
	} else {
		_, err = tx.ExecContext(ctx, `
			UPDATE clickup_submissions SET state='sending',retry_at=? WHERE account_id=? AND id=?`,
			next, account, msg.Id)
	}
	if err != nil {
		return out, "", storageError(err)
	}
	if err := tx.Commit(); err != nil {
		return out, "", storageError(err)
	}
	return out, string(secret), nil
}

func classifySubmission(id clickup.ID, err error, now time.Time) submission {
	if err == nil && id != "" {
		return submission{state: "published", entryID: string(id)}
	}
	var problem *clickup.HTTPError
	if errors.As(err, &problem) {
		switch problem.Status {
		case http.StatusTooManyRequests:
			return submission{state: "queued", message: "ClickUp rate limit reached",
				retryAt: now.Add(max(problem.RetryAfter, 30*time.Second)).Unix()}
		case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity:
			return submission{state: "failed", message: problem.Error()}
		}
	}
	return submission{state: "needs_review", message: "ClickUp creation is unconfirmed", retryAt: now.Add(time.Minute).Unix()}
}

func (s submission) response() *connect.Response[v1.ClickUpSubmission] {
	state := v1.ClickUpSubmission_STATE_QUEUED
	switch s.state {
	case "published":
		state = v1.ClickUpSubmission_STATE_PUBLISHED
	case "needs_review":
		state = v1.ClickUpSubmission_STATE_NEEDS_REVIEW
	case "failed":
		state = v1.ClickUpSubmission_STATE_FAILED
	}
	return connect.NewResponse(&v1.ClickUpSubmission{State: state, ProviderEntryId: s.entryID, Message: s.message, RetryAfterUnix: s.retryAt})
}
