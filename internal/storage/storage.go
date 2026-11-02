// Package storage is the only package that opens Spine's SQLite database
// (RFC 0001 §7, §7.1). M0 carries the tables crash recovery needs: leases,
// process groups, operation intents and snapshots. Later milestones add tasks,
// events and the tool cache to the same file.
//
// Operating rules (§7.1):
//   - One process writes. Open takes an exclusive flock on <db>.flock and fails
//     after a deadline rather than blocking forever.
//   - One writer connection, every write transaction BEGIN IMMEDIATE.
//   - WAL is enabled at open and read back; Open refuses a non-WAL database.
//   - synchronous=FULL; absence is ErrNotFound, every other error propagates.
package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	_ "modernc.org/sqlite"
)

// ErrNotFound is returned only when a row is absent.
var ErrNotFound = errors.New("storage: not found")

// ErrLocked means another process holds the database's writer lock.
var ErrLocked = errors.New("storage: database is held by another spine process")

// Store is an open Spine database.
type Store struct {
	path   string
	w      *sql.DB // single writer connection
	r      *sql.DB // read pool
	lockFD *os.File
}

// Options controls Open.
type Options struct {
	// LockWait bounds how long Open waits for the process lock (default 5s).
	LockWait time.Duration
}

// Open opens (creating if needed) the database at path.
func Open(ctx context.Context, path string, opts Options) (*Store, error) {
	if opts.LockWait <= 0 {
		opts.LockWait = 5 * time.Second
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	lockFD, err := acquireFlock(path+".flock", opts.LockWait)
	if err != nil {
		return nil, err
	}
	s := &Store{path: path, lockFD: lockFD}
	if s.w, err = sql.Open("sqlite", dsn(path, true)); err != nil {
		s.Close()
		return nil, err
	}
	s.w.SetMaxOpenConns(1)
	if err := s.enableWAL(ctx); err != nil {
		s.Close()
		return nil, err
	}
	if s.r, err = sql.Open("sqlite", dsn(path, false)); err != nil {
		s.Close()
		return nil, err
	}
	s.r.SetMaxOpenConns(8)
	if err := s.migrate(ctx); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// Path returns the database file path.
func (s *Store) Path() string { return s.path }

// Close releases the database and the process lock.
func (s *Store) Close() error {
	var errs []error
	if s.r != nil {
		errs = append(errs, s.r.Close())
	}
	if s.w != nil {
		errs = append(errs, s.w.Close())
	}
	if s.lockFD != nil {
		errs = append(errs, s.lockFD.Close()) // closing releases the flock
	}
	return errors.Join(errs...)
}

func dsn(path string, writer bool) string {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "synchronous(FULL)")
	if writer {
		q.Set("_txlock", "immediate")
	}
	return "file:" + path + "?" + q.Encode()
}

func (s *Store) enableWAL(ctx context.Context) error {
	var mode string
	if err := s.w.QueryRowContext(ctx, "PRAGMA journal_mode=WAL").Scan(&mode); err != nil {
		return fmt.Errorf("storage: enabling WAL: %w", err)
	}
	if !strings.EqualFold(mode, "wal") {
		// We hold the process flock, so no other Spine connection exists; a
		// non-WAL result means the filesystem cannot support WAL (§7.1).
		return fmt.Errorf("storage: journal_mode is %q, not wal; refusing to run (network filesystem?)", mode)
	}
	return nil
}

func acquireFlock(path string, wait time.Duration) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(wait)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			f.Close()
			return nil, err
		}
		if time.Now().After(deadline) {
			f.Close()
			return nil, ErrLocked
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// tx runs fn in a BEGIN IMMEDIATE transaction on the writer connection.
func (s *Store) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	t, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(t); err != nil {
		_ = t.Rollback()
		return err
	}
	return t.Commit()
}

// Checkpoint truncates the WAL (spine gc, §7.1).
func (s *Store) Checkpoint(ctx context.Context) error {
	_, err := s.w.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
	return err
}

// QuickCheck runs PRAGMA quick_check.
func (s *Store) QuickCheck(ctx context.Context) error {
	var res string
	if err := s.r.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&res); err != nil {
		return err
	}
	if res != "ok" {
		return fmt.Errorf("storage: quick_check: %s", res)
	}
	return nil
}

// JournalMode reports the current journal mode.
func (s *Store) JournalMode(ctx context.Context) (string, error) {
	var mode string
	err := s.r.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode)
	return mode, err
}

func now() int64 { return time.Now().UnixNano() }
