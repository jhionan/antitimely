-- name: UpsertObservation :one
INSERT INTO observations (source, bundle_id, window_title, binary_name, cwd, first_seen)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (source, bundle_id, window_title, binary_name, cwd)
DO UPDATE SET id = id
RETURNING id;

-- name: IsObservationIgnored :one
SELECT EXISTS(SELECT 1 FROM ignored_observations WHERE observation_id = ?) AS ignored;

-- name: InsertTick :exec
INSERT OR IGNORE INTO ticks (ts, observation_id, project_id) VALUES (?, ?, ?);

-- name: TotalsByProject :many
SELECT p.name, COUNT(DISTINCT t.ts) AS tick_count
FROM ticks t
JOIN projects p ON p.id = t.project_id
WHERE t.ts >= ? AND t.ts < ?
GROUP BY p.id;

-- name: UnassignedTicksInRange :one
SELECT COUNT(DISTINCT ts) AS tick_count
FROM ticks
WHERE project_id IS NULL AND ts >= ? AND ts < ?;

-- name: PendingReviewSignatures :many
SELECT o.id, o.source, o.bundle_id, o.window_title, o.binary_name, o.cwd,
       COUNT(t.ts) AS ticks, COALESCE(MAX(t.ts), 0) AS last_seen
FROM observations o
JOIN ticks t ON t.observation_id = o.id
WHERE t.project_id IS NULL
  AND o.id NOT IN (SELECT observation_id FROM ignored_observations)
GROUP BY o.id
ORDER BY ticks DESC
LIMIT ?;

-- name: CountPendingReviewSignatures :one
SELECT COUNT(DISTINCT o.id) AS n
FROM observations o
JOIN ticks t ON t.observation_id = o.id
WHERE t.project_id IS NULL
  AND o.id NOT IN (SELECT observation_id FROM ignored_observations);

-- name: AddWatchedProgram :exec
INSERT INTO watched_programs (kind, identifier, created_at) VALUES (?, ?, ?);

-- name: RemoveWatchedProgram :exec
DELETE FROM watched_programs WHERE kind = ? AND identifier = ?;

-- name: ListWatchedPrograms :many
SELECT id, kind, identifier FROM watched_programs ORDER BY kind, identifier;

-- name: AddProject :one
INSERT INTO projects (name, created_at) VALUES (?, ?) RETURNING id;

-- name: GetProjectByName :one
SELECT id, name FROM projects WHERE name = ?;

-- name: ListProjects :many
SELECT id, name FROM projects ORDER BY name;

-- name: DeleteProjectByName :exec
DELETE FROM projects WHERE name = ?;

-- name: AddRule :one
INSERT INTO rules (project_id, priority, match_bundle_id, match_title_substr, match_binary_name, match_cwd_prefix, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING id;

-- name: ListRules :many
SELECT r.id, p.name AS project_name, r.priority,
       r.match_bundle_id, r.match_title_substr, r.match_binary_name, r.match_cwd_prefix
FROM rules r
JOIN projects p ON p.id = r.project_id
ORDER BY r.priority, r.id;

-- name: ListRulesForCache :many
SELECT id, project_id, priority,
       match_bundle_id, match_title_substr, match_binary_name, match_cwd_prefix
FROM rules
ORDER BY priority, id;

-- name: DeleteRule :exec
DELETE FROM rules WHERE id = ?;

-- name: IgnoreObservation :exec
INSERT INTO ignored_observations (observation_id, ignored_at)
VALUES (?, ?)
ON CONFLICT (observation_id) DO NOTHING;

-- name: GetObservation :one
SELECT id, source, bundle_id, window_title, binary_name, cwd, first_seen
FROM observations
WHERE id = ?;

-- name: ApplyRuleRetroactivelyCounted :execrows
UPDATE ticks
SET project_id = ?
WHERE project_id IS NULL
  AND observation_id IN (
      SELECT id FROM observations
      WHERE (? IS NULL OR bundle_id = ?)
        AND (? IS NULL OR window_title LIKE '%' || ? || '%')
        AND (? IS NULL OR binary_name = ?)
        AND (? IS NULL OR cwd LIKE ? || '%')
  );

-- name: RetagSingleObservation :exec
UPDATE ticks
SET project_id = ?
WHERE project_id IS NULL AND observation_id = ?;
