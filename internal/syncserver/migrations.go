package syncserver

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

var migrations = []string{
	`
	CREATE TABLE accounts (
		id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, token_hash BLOB NOT NULL UNIQUE
	) STRICT;
	CREATE TABLE devices (
		id TEXT PRIMARY KEY, account_id TEXT NOT NULL REFERENCES accounts(id),
		name TEXT NOT NULL, token_hash BLOB NOT NULL UNIQUE, revoked INTEGER NOT NULL DEFAULT 0
	) STRICT;
	CREATE INDEX devices_account ON devices(account_id);
	CREATE TABLE records (
		device_id TEXT NOT NULL REFERENCES devices(id), id TEXT NOT NULL, revision INTEGER NOT NULL,
		kind TEXT NOT NULL CHECK (kind IN ('project', 'time', 'deleted')),
		name TEXT NOT NULL DEFAULT '', project_id TEXT NOT NULL DEFAULT '',
		start_unix INTEGER NOT NULL DEFAULT 0, duration INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (device_id, id)
	) STRICT;
	CREATE INDEX records_time ON records(start_unix, device_id) WHERE kind = 'time';
	CREATE TABLE project_links (
		device_id TEXT NOT NULL, project_id TEXT NOT NULL, group_id TEXT NOT NULL,
		PRIMARY KEY (device_id, project_id),
		FOREIGN KEY (device_id, project_id) REFERENCES records(device_id, id)
	) STRICT;
	`,
	`
	CREATE TABLE clickup_connections (
		account_id TEXT PRIMARY KEY REFERENCES accounts(id),
		id TEXT NOT NULL UNIQUE, user_id TEXT NOT NULL,
		username TEXT NOT NULL, token BLOB NOT NULL
	) STRICT;
	`,
	`
	CREATE TABLE clickup_oauth (
		account_id TEXT PRIMARY KEY REFERENCES accounts(id),
		hash BLOB NOT NULL UNIQUE,
		expires_unix INTEGER NOT NULL
	) STRICT;
	`,
	`
	CREATE TABLE clickup_submissions (
		account_id TEXT NOT NULL REFERENCES accounts(id),
		id TEXT NOT NULL, connection_id TEXT NOT NULL, workspace_id TEXT NOT NULL,
		task_id TEXT NOT NULL, start_unix INTEGER NOT NULL, end_unix INTEGER NOT NULL,
		state TEXT NOT NULL CHECK (state IN ('queued','sending','published','needs_review','failed')),
		entry_id TEXT NOT NULL DEFAULT '', message TEXT NOT NULL DEFAULT '',
		retry_at INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (account_id,id)
	) STRICT;
	CREATE INDEX clickup_submission_overlap ON clickup_submissions(account_id,start_unix,end_unix);
	`,
	`
	ALTER TABLE clickup_oauth ADD COLUMN device_id TEXT REFERENCES devices(id);
	`,
}

func Open(ctx context.Context, filename string) (*sql.DB, error) {
	if filename == "" {
		return nil, fmt.Errorf("database filename is required")
	}
	path, err := filepath.Abs(filename)
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	path = filepath.ToSlash(path)
	if strings.HasPrefix(path, "//") {
		path = "//" + path
	}
	u := url.URL{Path: path}
	db, err := sql.Open("sqlite", "file:"+u.EscapedPath()+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := migrate(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// A single write transaction serializes schema version checks and migrations,
// including when the provisioning command and server start together.
func migrate(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY) STRICT`); err != nil {
		return err
	}
	var current int
	if err := tx.QueryRowContext(ctx, `SELECT coalesce(max(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return err
	}
	if current > len(migrations) {
		return fmt.Errorf("sync database schema %d is newer than this server", current)
	}
	for i, migration := range migrations[current:] {
		if _, err := tx.ExecContext(ctx, migration); err != nil {
			return fmt.Errorf("sync migration %d: %w", current+i+1, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations VALUES (?)`, current+i+1); err != nil {
			return err
		}
	}
	return tx.Commit()
}
