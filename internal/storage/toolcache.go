package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// GetToolResult returns a cached tool result by key.
func (s *Store) GetToolResult(ctx context.Context, key []byte) ([]byte, bool, error) {
	var b []byte
	err := s.r.QueryRowContext(ctx, `SELECT result FROM tool_cache WHERE key=?`, key).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	return b, err == nil, err
}

// PutToolResult stores a tool result. Results are content-addressed, so a
// concurrent identical Put is harmless.
func (s *Store) PutToolResult(ctx context.Context, key []byte, tool string, result []byte) error {
	return s.tx(ctx, func(t *sql.Tx) error {
		_, err := t.ExecContext(ctx, `INSERT OR REPLACE INTO tool_cache(key,tool,result,created_at) VALUES(?,?,?,?)`,
			key, tool, result, time.Now().Unix())
		return err
	})
}

// PruneToolCache deletes entries older than cutoff and returns the count.
func (s *Store) PruneToolCache(ctx context.Context, cutoff time.Time) (int64, error) {
	var n int64
	err := s.tx(ctx, func(t *sql.Tx) error {
		r, err := t.ExecContext(ctx, `DELETE FROM tool_cache WHERE created_at < ?`, cutoff.Unix())
		if err != nil {
			return err
		}
		n, err = r.RowsAffected()
		return err
	})
	return n, err
}
