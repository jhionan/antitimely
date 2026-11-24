package syncserver

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"connectrpc.com/connect"
	v1 "github.com/rian/antitimely/internal/syncapi/v1"
)

type recordValue struct {
	kind, name, project string
	start               int64
	duration            int32
}

func decodeRecord(r *v1.Record) (recordValue, error) {
	var out recordValue
	if r == nil || !validID.MatchString(r.GetId()) || r.GetRevision() <= 0 {
		return out, invalid("each record requires a valid ID and positive revision")
	}
	switch v := r.Value.(type) {
	case *v1.Record_Project:
		name := strings.TrimSpace(v.Project.GetName())
		if name == "" || len(name) > 1024 {
			return out, invalid("invalid project name")
		}
		out.kind, out.name = "project", name
	case *v1.Record_TimeSlice:
		ts := v.TimeSlice
		if !validID.MatchString(ts.GetProjectId()) || ts.GetStartUnix() <= 0 || ts.GetStartUnix() > maxUnix ||
			ts.GetDurationSeconds() < 1 || ts.GetDurationSeconds() > maxDuration {
			return out, invalid("invalid time slice")
		}
		out.kind, out.project = "time", ts.ProjectId
		out.start, out.duration = ts.StartUnix, ts.DurationSeconds
	case *v1.Record_Tombstone:
		out.kind = "deleted"
	default:
		return out, invalid("each record requires a project, time slice, or tombstone")
	}
	return out, nil
}

func (s *Service) Push(ctx context.Context, req *connect.Request[v1.PushRequest]) (*connect.Response[v1.PushResponse], error) {
	hash, err := tokenHash(req.Header())
	if err != nil {
		return nil, err
	}
	if !validID.MatchString(req.Msg.DeviceId) {
		return nil, invalid("invalid device ID")
	}
	if len(req.Msg.Records) == 0 || len(req.Msg.Records) > MaxBatch {
		return nil, invalid("invalid batch size")
	}
	values := make([]recordValue, len(req.Msg.Records))
	for i, record := range req.Msg.Records {
		values[i], err = decodeRecord(record)
		if err != nil {
			return nil, err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, storageError(err)
	}
	defer tx.Rollback()
	var device string
	err = tx.QueryRowContext(ctx, `SELECT id FROM devices WHERE token_hash = ? AND id = ? AND revoked = 0`, hash, req.Msg.DeviceId).Scan(&device)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("valid device token required"))
	}
	if err != nil {
		return nil, storageError(err)
	}
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO records (device_id, id, revision, kind, name, project_id, start_unix, duration)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (device_id, id) DO UPDATE SET
			revision = excluded.revision, kind = excluded.kind, name = excluded.name,
			project_id = excluded.project_id, start_unix = excluded.start_unix, duration = excluded.duration
		WHERE excluded.revision > records.revision`)
	if err != nil {
		return nil, storageError(err)
	}
	defer stmt.Close()
	for i, record := range req.Msg.Records {
		value := values[i]
		if _, err := stmt.ExecContext(ctx, device, record.Id, record.Revision, value.kind, value.name, value.project, value.start, value.duration); err != nil {
			return nil, storageError(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, storageError(err)
	}
	return connect.NewResponse(&v1.PushResponse{}), nil
}
