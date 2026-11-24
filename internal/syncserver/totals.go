package syncserver

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"

	"connectrpc.com/connect"
	v1 "github.com/rian/antitimely/internal/syncapi/v1"
)

func (s *Service) ListProjects(ctx context.Context, req *connect.Request[v1.ListProjectsRequest]) (*connect.Response[v1.ListProjectsResponse], error) {
	account, err := s.account(ctx, req.Header())
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.device_id, r.id, r.name, coalesce(l.group_id, '')
		FROM records r JOIN devices d ON d.id = r.device_id
		LEFT JOIN project_links l ON l.device_id = r.device_id AND l.project_id = r.id
		WHERE d.account_id = ? AND r.kind = 'project' ORDER BY r.name, r.device_id, r.id`, account)
	if err != nil {
		return nil, storageError(err)
	}
	defer rows.Close()
	out := &v1.ListProjectsResponse{}
	for rows.Next() {
		p := &v1.ProjectRef{}
		if err := rows.Scan(&p.DeviceId, &p.ProjectId, &p.Name, &p.GroupId); err != nil {
			return nil, storageError(err)
		}
		out.Projects = append(out.Projects, p)
	}
	if err := rows.Err(); err != nil {
		return nil, storageError(err)
	}
	return connect.NewResponse(out), nil
}

func (s *Service) LinkProject(ctx context.Context, req *connect.Request[v1.LinkProjectRequest]) (*connect.Response[v1.LinkProjectResponse], error) {
	account, err := s.account(ctx, req.Header())
	if err != nil {
		return nil, err
	}
	msg := req.Msg
	group := strings.TrimSpace(msg.GroupId)
	if !validID.MatchString(msg.DeviceId) || !validID.MatchString(msg.ProjectId) || (group != "" && !validGroup.MatchString(group)) {
		return nil, invalid("invalid project link")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, storageError(err)
	}
	defer tx.Rollback()
	var exists int
	err = tx.QueryRowContext(ctx, `
		SELECT 1 FROM records r JOIN devices d ON d.id = r.device_id
		WHERE r.device_id = ? AND r.id = ? AND r.kind = 'project' AND d.account_id = ?`,
		msg.DeviceId, msg.ProjectId, account).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("project not found"))
	}
	if err != nil {
		return nil, storageError(err)
	}
	if group == "" {
		_, err = tx.ExecContext(ctx, `DELETE FROM project_links WHERE device_id = ? AND project_id = ?`, msg.DeviceId, msg.ProjectId)
	} else {
		_, err = tx.ExecContext(ctx, `
			INSERT INTO project_links VALUES (?, ?, ?)
			ON CONFLICT (device_id, project_id) DO UPDATE SET group_id = excluded.group_id`,
			msg.DeviceId, msg.ProjectId, group)
	}
	if err != nil {
		return nil, storageError(err)
	}
	if err := tx.Commit(); err != nil {
		return nil, storageError(err)
	}
	return connect.NewResponse(&v1.LinkProjectResponse{}), nil
}

type union struct{ end, seconds int64 }

// add counts just the portion of this interval not already covered. Input
// must arrive in ascending start order for both account and project unions.
func (u *union) add(start, end int64) {
	if end > u.end {
		u.seconds += end - max(start, u.end)
		u.end = end
	}
}

func (s *Service) Totals(ctx context.Context, req *connect.Request[v1.TotalsRequest]) (*connect.Response[v1.TotalsResponse], error) {
	account, err := s.account(ctx, req.Header())
	if err != nil {
		return nil, err
	}
	start, end := req.Msg.StartUnix, req.Msg.EndUnix
	if start < 1 || end <= start || end > maxUnix || end-start > 31*24*60*60 {
		return nil, invalid("select a positive time range of at most 31 days")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT coalesce('group:' || l.group_id, t.device_id || '/' || t.project_id),
			t.start_unix, t.duration
		FROM records t
		JOIN devices d ON d.id = t.device_id
		JOIN records p ON p.device_id = t.device_id AND p.id = t.project_id AND p.kind = 'project'
		LEFT JOIN project_links l ON l.device_id = t.device_id AND l.project_id = t.project_id
		WHERE d.account_id = ? AND t.kind = 'time' AND t.start_unix >= ?
			AND t.start_unix < ? AND t.start_unix + t.duration > ?
		ORDER BY t.start_unix`, account, start-maxDuration, end, start)
	if err != nil {
		return nil, storageError(err)
	}
	defer rows.Close()
	projects := make(map[string]*union)
	var accountTime union
	for rows.Next() {
		var key string
		var ts, duration int64
		if err := rows.Scan(&key, &ts, &duration); err != nil {
			return nil, storageError(err)
		}
		first, last := max(ts, start), min(ts+duration, end)
		accountTime.add(first, last)
		if projects[key] == nil {
			projects[key] = &union{}
		}
		projects[key].add(first, last)
	}
	if err := rows.Err(); err != nil {
		return nil, storageError(err)
	}
	out := &v1.TotalsResponse{TotalSeconds: accountTime.seconds}
	for key, u := range projects {
		out.Projects = append(out.Projects, &v1.ProjectTotal{Key: key, Seconds: u.seconds})
	}
	slices.SortFunc(out.Projects, func(a, b *v1.ProjectTotal) int { return strings.Compare(a.Key, b.Key) })
	return connect.NewResponse(out), nil
}
