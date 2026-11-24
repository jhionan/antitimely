CREATE TABLE IF NOT EXISTS sync_state (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    endpoint TEXT NOT NULL, device_id TEXT NOT NULL,
    enabled INTEGER NOT NULL, interval INTEGER NOT NULL
) STRICT;

CREATE TABLE IF NOT EXISTS sync_projects (
    local_id INTEGER PRIMARY KEY, record_id TEXT NOT NULL UNIQUE
) STRICT;

CREATE TABLE IF NOT EXISTS sync_ticks (
    ts INTEGER NOT NULL, observation_id INTEGER NOT NULL,
    record_id TEXT NOT NULL UNIQUE, duration INTEGER NOT NULL,
    PRIMARY KEY (ts, observation_id)
) STRICT;

CREATE TABLE IF NOT EXISTS sync_outbox (
    seq INTEGER PRIMARY KEY AUTOINCREMENT, record_id TEXT NOT NULL,
    kind TEXT NOT NULL, name TEXT NOT NULL DEFAULT '',
    project_id TEXT NOT NULL DEFAULT '', ts INTEGER NOT NULL DEFAULT 0,
    duration INTEGER NOT NULL DEFAULT 0
) STRICT;

CREATE TRIGGER IF NOT EXISTS sync_project_create AFTER INSERT ON sync_projects
BEGIN
    INSERT INTO sync_outbox (record_id, kind, name)
    SELECT NEW.record_id, 'project', substr(name, 1, 256) FROM projects WHERE id = NEW.local_id;
END;

CREATE TRIGGER IF NOT EXISTS sync_project_rename AFTER UPDATE OF name ON projects
WHEN NEW.name <> OLD.name
BEGIN
    INSERT INTO sync_outbox (record_id, kind, name)
    SELECT record_id, 'project', substr(NEW.name, 1, 256)
    FROM sync_projects WHERE local_id = NEW.id;
END;

CREATE TRIGGER IF NOT EXISTS sync_project_delete AFTER DELETE ON projects
BEGIN
    INSERT INTO sync_outbox (record_id, kind)
    SELECT record_id, 'deleted' FROM sync_projects WHERE local_id = OLD.id;
    DELETE FROM sync_projects WHERE local_id = OLD.id;
END;

CREATE TRIGGER IF NOT EXISTS sync_tick_insert AFTER INSERT ON ticks
WHEN NEW.ts BETWEEN 1 AND 253402300799
    AND (SELECT enabled FROM sync_state WHERE singleton = 1) = 1
BEGIN
    INSERT INTO sync_ticks
    SELECT NEW.ts, NEW.observation_id, lower(hex(randomblob(16))), interval
    FROM sync_state WHERE singleton = 1;

    INSERT INTO sync_projects
    SELECT NEW.project_id, lower(hex(randomblob(16))) WHERE NEW.project_id IS NOT NULL
    ON CONFLICT (local_id) DO NOTHING;

    INSERT INTO sync_outbox (record_id, kind, project_id, ts, duration)
    SELECT t.record_id, 'time', p.record_id, t.ts, t.duration
    FROM sync_ticks t JOIN sync_projects p ON p.local_id = NEW.project_id
    WHERE t.ts = NEW.ts AND t.observation_id = NEW.observation_id;
END;

CREATE TRIGGER IF NOT EXISTS sync_tick_retag AFTER UPDATE OF project_id ON ticks
WHEN NEW.project_id IS NOT OLD.project_id
BEGIN
    INSERT INTO sync_projects
    SELECT NEW.project_id, lower(hex(randomblob(16)))
    WHERE NEW.project_id IS NOT NULL AND EXISTS (
        SELECT 1 FROM sync_ticks WHERE ts = NEW.ts AND observation_id = NEW.observation_id
    )
    ON CONFLICT (local_id) DO NOTHING;

    INSERT INTO sync_outbox (record_id, kind, project_id, ts, duration)
    SELECT t.record_id, CASE WHEN p.record_id IS NULL THEN 'deleted' ELSE 'time' END,
           coalesce(p.record_id, ''), t.ts, t.duration
    FROM sync_ticks t LEFT JOIN sync_projects p ON p.local_id = NEW.project_id
    WHERE t.ts = NEW.ts AND t.observation_id = NEW.observation_id;
END;

CREATE TRIGGER IF NOT EXISTS sync_tick_delete AFTER DELETE ON ticks
BEGIN
    INSERT INTO sync_outbox (record_id, kind)
    SELECT record_id, 'deleted' FROM sync_ticks WHERE ts = OLD.ts
        AND observation_id = OLD.observation_id AND OLD.project_id IS NOT NULL;
    DELETE FROM sync_ticks WHERE ts = OLD.ts AND observation_id = OLD.observation_id;
END;
