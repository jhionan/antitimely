-- A company groups projects.
CREATE TABLE IF NOT EXISTS companies (
    id          INTEGER PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    created_at  INTEGER NOT NULL
) STRICT;

-- A project = a billable bucket.
CREATE TABLE IF NOT EXISTS projects (
    id          INTEGER PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    company_id  INTEGER REFERENCES companies(id) ON DELETE SET NULL,
    created_at  INTEGER NOT NULL
) STRICT;

-- The allowlist: what the daemon may observe.
CREATE TABLE IF NOT EXISTS watched_programs (
    id          INTEGER PRIMARY KEY,
    kind        TEXT NOT NULL CHECK (kind IN ('bundle', 'binary')),
    identifier  TEXT NOT NULL,
    created_at  INTEGER NOT NULL,
    UNIQUE (kind, identifier)
) STRICT;

-- Mapping rules: observation → project. At least one match column required.
CREATE TABLE IF NOT EXISTS rules (
    id                  INTEGER PRIMARY KEY,
    project_id          INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    priority            INTEGER NOT NULL DEFAULT 100,
    match_bundle_id     TEXT,
    match_title_substr  TEXT,
    match_binary_name   TEXT,
    match_cwd_prefix    TEXT,
    created_at          INTEGER NOT NULL,
    CHECK (
        match_bundle_id IS NOT NULL OR
        match_title_substr IS NOT NULL OR
        match_binary_name IS NOT NULL OR
        match_cwd_prefix IS NOT NULL
    )
) STRICT;
CREATE INDEX IF NOT EXISTS idx_rules_priority ON rules(priority);

-- Distinct signal signatures. NOT NULL DEFAULT '' so UNIQUE dedup works
-- (SQLite UNIQUE treats NULL != NULL otherwise).
CREATE TABLE IF NOT EXISTS observations (
    id              INTEGER PRIMARY KEY,
    source          TEXT NOT NULL CHECK (source IN ('focus', 'agent')),
    bundle_id       TEXT NOT NULL DEFAULT '',
    window_title    TEXT NOT NULL DEFAULT '',
    binary_name     TEXT NOT NULL DEFAULT '',
    cwd             TEXT NOT NULL DEFAULT '',
    first_seen      INTEGER NOT NULL,
    UNIQUE (source, bundle_id, window_title, binary_name, cwd)
) STRICT;

CREATE TABLE IF NOT EXISTS ignored_observations (
    observation_id  INTEGER PRIMARY KEY REFERENCES observations(id) ON DELETE CASCADE,
    ignored_at      INTEGER NOT NULL
) STRICT;

CREATE TABLE IF NOT EXISTS ticks (
    ts              INTEGER NOT NULL,
    observation_id  INTEGER NOT NULL REFERENCES observations(id),
    project_id      INTEGER REFERENCES projects(id),
    PRIMARY KEY (ts, observation_id)
) STRICT;
CREATE INDEX IF NOT EXISTS idx_ticks_project_ts ON ticks(project_id, ts);
CREATE INDEX IF NOT EXISTS idx_ticks_unassigned ON ticks(observation_id) WHERE project_id IS NULL;
