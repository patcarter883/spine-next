package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/patcarter883/spine-next/internal/api"
)

var (
	// ErrConflict is a stale resource version: re-read and retry.
	ErrConflict = errors.New("storage: resource version conflict")
	// ErrExists is returned when creating a task whose id is taken.
	ErrExists = errors.New("storage: task already exists")
	// ErrNotOwner is a status write that changes a condition the writer does
	// not own (RFC §4.3).
	ErrNotOwner = errors.New("storage: condition written by a non-owner")
	// ErrPhaseEvidence is a phase change without the condition that must
	// back it (Verified needs Verified, Integrated needs Integrated).
	ErrPhaseEvidence = errors.New("storage: phase change without its condition")
)

// CreateTask inserts a new task in phase Pending.
func (s *Store) CreateTask(ctx context.Context, id api.TaskID, spec api.Spec) (api.Task, error) {
	if err := api.ValidateSpec(spec); err != nil {
		return api.Task{}, err
	}
	now := time.Now().UTC()
	t := api.Task{ID: id, Spec: spec, Status: api.Status{Phase: api.PhasePending}, ResourceVersion: 1, CreatedAt: now, UpdatedAt: now}
	sb, _ := json.Marshal(t.Spec)
	stb, _ := json.Marshal(t.Status)
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM tasks WHERE id=?`, string(id)).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrExists
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO tasks(id,spec,status,phase,rv,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`,
			string(id), sb, stb, string(t.Status.Phase), t.ResourceVersion, now.UnixNano(), now.UnixNano()); err != nil {
			return err
		}
		return appendEvent(ctx, tx, id, "submitted", map[string]any{"spec": spec})
	})
	return t, err
}

type rowScanner interface{ Scan(...any) error }

func scanTask(r rowScanner) (api.Task, error) {
	var t api.Task
	var id, sb, stb string
	var c, u int64
	if err := r.Scan(&id, &sb, &stb, &t.ResourceVersion, &c, &u); err != nil {
		return api.Task{}, err
	}
	t.ID = api.TaskID(id)
	if err := json.Unmarshal([]byte(sb), &t.Spec); err != nil {
		return api.Task{}, fmt.Errorf("storage: task %s spec: %w", id, err)
	}
	if err := json.Unmarshal([]byte(stb), &t.Status); err != nil {
		return api.Task{}, fmt.Errorf("storage: task %s status: %w", id, err)
	}
	t.CreatedAt, t.UpdatedAt = time.Unix(0, c).UTC(), time.Unix(0, u).UTC()
	return t, nil
}

const taskCols = `id,spec,status,rv,created_at,updated_at`

// GetTask returns a task or ErrNotFound.
func (s *Store) GetTask(ctx context.Context, id api.TaskID) (api.Task, error) {
	t, err := scanTask(s.r.QueryRowContext(ctx, `SELECT `+taskCols+` FROM tasks WHERE id=?`, string(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return api.Task{}, ErrNotFound
	}
	return t, err
}

// ListTasks returns tasks, newest first, optionally filtered by phase.
func (s *Store) ListTasks(ctx context.Context, phases ...api.Phase) ([]api.Task, error) {
	q := `SELECT ` + taskCols + ` FROM tasks`
	var args []any
	if len(phases) > 0 {
		q += ` WHERE phase IN (?` + repeat(",?", len(phases)-1) + `)`
		for _, p := range phases {
			args = append(args, string(p))
		}
	}
	q += ` ORDER BY created_at DESC, id`
	rows, err := s.r.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []api.Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func repeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}

// UpdateStatus applies fn to the stored status inside one write transaction.
// It fails with ErrConflict if the stored resource version is not rv (pass
// 0 to skip the check), ErrNotOwner if fn changed a condition that source
// does not own, and ErrPhaseEvidence if fn moved to Verified/Integrated
// without the backing condition. Spec changes go through UpdateSpec.
func (s *Store) UpdateStatus(ctx context.Context, id api.TaskID, rv int64, source string, fn func(*api.Status) error) (api.Task, error) {
	err := s.tx(ctx, func(tx *sql.Tx) error {
		t, err := scanTask(tx.QueryRowContext(ctx, `SELECT `+taskCols+` FROM tasks WHERE id=?`, string(id)))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if rv != 0 && t.ResourceVersion != rv {
			return fmt.Errorf("%w: %s at %d, expected %d", ErrConflict, id, t.ResourceVersion, rv)
		}
		before := cloneStatus(t.Status)
		if err := fn(&t.Status); err != nil {
			return err
		}
		if err := checkOwnership(before, t.Status, source); err != nil {
			return err
		}
		if err := checkEvidence(before, t.Status); err != nil {
			return err
		}
		if before.Phase != t.Status.Phase {
			if err := appendEvent(ctx, tx, id, "transition", map[string]any{
				"from": before.Phase, "to": t.Status.Phase, "reason": t.Status.Reason, "message": t.Status.Message, "source": source,
			}); err != nil {
				return err
			}
		}
		return writeTask(ctx, tx, &t)
	})
	if err != nil {
		return api.Task{}, err
	}
	return s.GetTask(ctx, id)
}

// UpdateSpec replaces the spec (a new generation) and applies fn to the
// status. Used for retries and operator edits.
func (s *Store) UpdateSpec(ctx context.Context, id api.TaskID, rv int64, spec api.Spec, fn func(*api.Status)) (api.Task, error) {
	if err := api.ValidateSpec(spec); err != nil {
		return api.Task{}, err
	}
	err := s.tx(ctx, func(tx *sql.Tx) error {
		t, err := scanTask(tx.QueryRowContext(ctx, `SELECT `+taskCols+` FROM tasks WHERE id=?`, string(id)))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if rv != 0 && t.ResourceVersion != rv {
			return fmt.Errorf("%w: %s", ErrConflict, id)
		}
		spec.Generation = t.Spec.Generation + 1
		before := t.Status.Phase
		t.Spec = spec
		if fn != nil {
			fn(&t.Status)
		}
		if err := appendEvent(ctx, tx, id, "spec", map[string]any{"generation": spec.Generation, "spec": spec}); err != nil {
			return err
		}
		if before != t.Status.Phase {
			if err := appendEvent(ctx, tx, id, "transition", map[string]any{"from": before, "to": t.Status.Phase, "reason": t.Status.Reason, "source": api.SourceHuman}); err != nil {
				return err
			}
		}
		return writeTask(ctx, tx, &t)
	})
	if err != nil {
		return api.Task{}, err
	}
	return s.GetTask(ctx, id)
}

func writeTask(ctx context.Context, tx *sql.Tx, t *api.Task) error {
	sb, _ := json.Marshal(t.Spec)
	stb, err := json.Marshal(t.Status)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE tasks SET spec=?, status=?, phase=?, rv=rv+1, updated_at=? WHERE id=?`,
		sb, stb, string(t.Status.Phase), time.Now().UTC().UnixNano(), string(t.ID))
	return err
}

func cloneStatus(s api.Status) api.Status {
	b, _ := json.Marshal(s)
	var c api.Status
	json.Unmarshal(b, &c)
	return c
}

func checkOwnership(before, after api.Status, source string) error {
	types := map[string]bool{}
	for _, c := range before.Conditions {
		types[c.Type] = true
	}
	for _, c := range after.Conditions {
		types[c.Type] = true
	}
	for t := range types {
		b, bok := before.Cond(t)
		a, aok := after.Cond(t)
		same := bok == aok && b.Status == a.Status && b.Reason == a.Reason && b.Message == a.Message
		if same {
			continue
		}
		owner, known := api.ConditionOwner[t]
		if !known || owner != source {
			return fmt.Errorf("%w: %s changed by %q (owner %q)", ErrNotOwner, t, source, owner)
		}
		if aok && a.Source != source {
			return fmt.Errorf("%w: %s stamped with source %q by %q", ErrNotOwner, t, a.Source, source)
		}
	}
	return nil
}

func checkEvidence(before, after api.Status) error {
	if before.Phase == after.Phase {
		return nil
	}
	switch after.Phase {
	case api.PhaseVerified:
		if !after.IsTrue(api.CondVerified) {
			return fmt.Errorf("%w: Verified", ErrPhaseEvidence)
		}
	case api.PhaseIntegrated:
		if !after.IsTrue(api.CondIntegrated) {
			return fmt.Errorf("%w: Integrated", ErrPhaseEvidence)
		}
	}
	return nil
}

// Event is one entry of a task's log.
type Event struct {
	ID     int64           `json:"id"`
	TaskID api.TaskID      `json:"task_id"`
	At     time.Time       `json:"at"`
	Kind   string          `json:"kind"`
	Data   json.RawMessage `json:"data"`
}

func appendEvent(ctx context.Context, tx *sql.Tx, id api.TaskID, kind string, data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO events(task_id,at,kind,data) VALUES(?,?,?,?)`, string(id), time.Now().UTC().UnixNano(), kind, b)
	return err
}

// AppendEvent records an event.
func (s *Store) AppendEvent(ctx context.Context, id api.TaskID, kind string, data any) error {
	return s.tx(ctx, func(tx *sql.Tx) error { return appendEvent(ctx, tx, id, kind, data) })
}

// ListEvents returns a task's events after the given id, oldest first.
func (s *Store) ListEvents(ctx context.Context, id api.TaskID, after int64, limit int) ([]Event, error) {
	if limit <= 0 {
		limit = 1000
	}
	rows, err := s.r.QueryContext(ctx, `SELECT id,task_id,at,kind,data FROM events WHERE task_id=? AND id>? ORDER BY id LIMIT ?`, string(id), after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var tid, data string
		var at int64
		if err := rows.Scan(&e.ID, &tid, &at, &e.Kind, &data); err != nil {
			return nil, err
		}
		e.TaskID, e.At, e.Data = api.TaskID(tid), time.Unix(0, at).UTC(), json.RawMessage(data)
		out = append(out, e)
	}
	return out, rows.Err()
}

// ListEventsByKind returns events of the given kinds at or after since,
// oldest first, across all tasks (dashboard aggregates).
func (s *Store) ListEventsByKind(ctx context.Context, since time.Time, limit int, kinds ...string) ([]Event, error) {
	if limit <= 0 {
		limit = 100000
	}
	q := `SELECT id,task_id,at,kind,data FROM events WHERE at >= ?`
	args := []any{since.UnixNano()}
	if len(kinds) > 0 {
		q += ` AND kind IN (?` + strings.Repeat(",?", len(kinds)-1) + `)`
		for _, k := range kinds {
			args = append(args, k)
		}
	}
	q += ` ORDER BY id LIMIT ?`
	args = append(args, limit)
	rows, err := s.r.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var tid, data string
		var at int64
		if err := rows.Scan(&e.ID, &tid, &at, &e.Kind, &data); err != nil {
			return nil, err
		}
		e.TaskID, e.At, e.Data = api.TaskID(tid), time.Unix(0, at).UTC(), json.RawMessage(data)
		out = append(out, e)
	}
	return out, rows.Err()
}

// Sizes reports the database and WAL file sizes in bytes.
func (s *Store) Sizes() (db, wal int64) {
	if fi, err := os.Stat(s.path); err == nil {
		db = fi.Size()
	}
	if fi, err := os.Stat(s.path + "-wal"); err == nil {
		wal = fi.Size()
	}
	return
}
