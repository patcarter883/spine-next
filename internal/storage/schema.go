package storage

import (
	"context"
	"database/sql"
	"fmt"
)

// migrations are append-only; index i upgrades schema version i to i+1.
var migrations = []string{
	`CREATE TABLE leases (
		task_id    TEXT    NOT NULL,
		attempt    INTEGER NOT NULL,
		path       TEXT    NOT NULL UNIQUE,
		branch     TEXT    NOT NULL,
		base_oid   TEXT    NOT NULL,
		attempt_id TEXT    NOT NULL DEFAULT '',
		state      TEXT    NOT NULL,
		owner_pid  INTEGER NOT NULL,
		updated_at INTEGER NOT NULL,
		PRIMARY KEY (task_id, attempt)
	);
	CREATE TABLE procs (
		id         INTEGER PRIMARY KEY,
		task_id    TEXT    NOT NULL,
		attempt    INTEGER NOT NULL,
		pgid       INTEGER NOT NULL,
		start_time TEXT    NOT NULL,
		cwd        TEXT    NOT NULL,
		state      TEXT    NOT NULL,
		updated_at INTEGER NOT NULL
	);
	CREATE INDEX procs_state ON procs(state);
	CREATE TABLE ops (
		id          INTEGER PRIMARY KEY,
		task_id     TEXT    NOT NULL,
		kind        TEXT    NOT NULL,
		intent_json TEXT    NOT NULL,
		state       TEXT    NOT NULL,
		created_at  INTEGER NOT NULL,
		updated_at  INTEGER NOT NULL
	);
	CREATE INDEX ops_state ON ops(state);
	CREATE TABLE snapshots (
		task_id    TEXT    NOT NULL,
		seq        INTEGER NOT NULL,
		ref        TEXT    NOT NULL UNIQUE,
		commit_oid TEXT    NOT NULL,
		tree_oid   TEXT    NOT NULL,
		label      TEXT    NOT NULL,
		score      REAL,
		created_at INTEGER NOT NULL,
		PRIMARY KEY (task_id, seq)
	);`,
	// 2: content-addressed tool result cache (RFC §3.3).
	`CREATE TABLE tool_cache (
		key        BLOB    PRIMARY KEY,
		tool       TEXT    NOT NULL,
		result     BLOB    NOT NULL,
		created_at INTEGER NOT NULL
	) WITHOUT ROWID;`,
}

func (s *Store) migrate(ctx context.Context) error {
	return s.tx(ctx, func(t *sql.Tx) error {
		var v int
		if err := t.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
			return err
		}
		if v > len(migrations) {
			return fmt.Errorf("storage: schema version %d is newer than this binary (%d)", v, len(migrations))
		}
		for ; v < len(migrations); v++ {
			if _, err := t.ExecContext(ctx, migrations[v]); err != nil {
				return fmt.Errorf("storage: migration %d: %w", v+1, err)
			}
		}
		_, err := t.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", v))
		return err
	})
}
