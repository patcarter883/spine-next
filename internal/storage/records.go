package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// LeaseState is a worktree lease lifecycle state (RFC §3.2).
type LeaseState string

const (
	LeaseRequested LeaseState = "Requested"
	LeaseReady     LeaseState = "Ready"
	LeaseDraining  LeaseState = "Draining"
	LeaseReleased  LeaseState = "Released"
)

// Lease is a durable claim on a worktree path.
type Lease struct {
	TaskID    string
	Attempt   int
	Path      string
	Branch    string
	BaseOID   string
	AttemptID string
	State     LeaseState
	OwnerPID  int
}

// PutLease inserts or replaces a lease row.
func (s *Store) PutLease(ctx context.Context, l Lease) error {
	return s.tx(ctx, func(t *sql.Tx) error {
		_, err := t.ExecContext(ctx, `INSERT INTO leases(task_id,attempt,path,branch,base_oid,attempt_id,state,owner_pid,updated_at)
			VALUES(?,?,?,?,?,?,?,?,?)
			ON CONFLICT(task_id,attempt) DO UPDATE SET path=excluded.path, branch=excluded.branch,
			base_oid=excluded.base_oid, attempt_id=excluded.attempt_id, state=excluded.state,
			owner_pid=excluded.owner_pid, updated_at=excluded.updated_at`,
			l.TaskID, l.Attempt, l.Path, l.Branch, l.BaseOID, l.AttemptID, string(l.State), l.OwnerPID, now())
		return err
	})
}

// GetLease returns one lease or ErrNotFound.
func (s *Store) GetLease(ctx context.Context, task string, attempt int) (Lease, error) {
	row := s.r.QueryRowContext(ctx, `SELECT task_id,attempt,path,branch,base_oid,attempt_id,state,owner_pid
		FROM leases WHERE task_id=? AND attempt=?`, task, attempt)
	l, err := scanLease(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Lease{}, ErrNotFound
	}
	return l, err
}

// ListLeases returns leases in the given states (all states if none given).
func (s *Store) ListLeases(ctx context.Context, states ...LeaseState) ([]Lease, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT task_id,attempt,path,branch,base_oid,attempt_id,state,owner_pid
		FROM leases ORDER BY task_id, attempt`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	want := map[LeaseState]bool{}
	for _, st := range states {
		want[st] = true
	}
	var out []Lease
	for rows.Next() {
		l, err := scanLease(rows)
		if err != nil {
			return nil, err
		}
		if len(want) == 0 || want[l.State] {
			out = append(out, l)
		}
	}
	return out, rows.Err()
}

type scanner interface{ Scan(...any) error }

func scanLease(r scanner) (Lease, error) {
	var l Lease
	var st string
	err := r.Scan(&l.TaskID, &l.Attempt, &l.Path, &l.Branch, &l.BaseOID, &l.AttemptID, &st, &l.OwnerPID)
	l.State = LeaseState(st)
	return l, err
}

// ProcState is a recorded process group's state.
type ProcState string

const (
	ProcRunning ProcState = "running"
	ProcExited  ProcState = "exited"
	ProcReaped  ProcState = "reaped" // killed by Recover
)

// Proc is a recorded process group (RFC §6.1, "agent spawn" intent).
type Proc struct {
	ID        int64
	TaskID    string
	Attempt   int
	PGID      int
	StartTime string
	Cwd       string
	State     ProcState
}

// RecordProc stores a running process group and returns its row id.
func (s *Store) RecordProc(ctx context.Context, p Proc) (int64, error) {
	var id int64
	err := s.tx(ctx, func(t *sql.Tx) error {
		res, err := t.ExecContext(ctx, `INSERT INTO procs(task_id,attempt,pgid,start_time,cwd,state,updated_at)
			VALUES(?,?,?,?,?,?,?)`, p.TaskID, p.Attempt, p.PGID, p.StartTime, p.Cwd, string(ProcRunning), now())
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	return id, err
}

// SetProcState updates a process row.
func (s *Store) SetProcState(ctx context.Context, id int64, st ProcState) error {
	return s.tx(ctx, func(t *sql.Tx) error {
		_, err := t.ExecContext(ctx, `UPDATE procs SET state=?, updated_at=? WHERE id=?`, string(st), now(), id)
		return err
	})
}

// ListProcs returns process rows in a state.
func (s *Store) ListProcs(ctx context.Context, st ProcState) ([]Proc, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT id,task_id,attempt,pgid,start_time,cwd,state FROM procs WHERE state=? ORDER BY id`, string(st))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Proc
	for rows.Next() {
		var p Proc
		var state string
		if err := rows.Scan(&p.ID, &p.TaskID, &p.Attempt, &p.PGID, &p.StartTime, &p.Cwd, &state); err != nil {
			return nil, err
		}
		p.State = ProcState(state)
		out = append(out, p)
	}
	return out, rows.Err()
}

// OpState is an operation intent's state.
type OpState string

const (
	OpPending OpState = "pending"
	OpDone    OpState = "done"
	OpAborted OpState = "aborted"
)

// Op is an intent-before-effect record (RFC §6.1).
type Op struct {
	ID     int64
	TaskID string
	Kind   string
	Intent json.RawMessage
	State  OpState
}

// BeginOp records a pending intent and returns its id.
func (s *Store) BeginOp(ctx context.Context, task, kind string, intent any) (int64, error) {
	b, err := json.Marshal(intent)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.tx(ctx, func(t *sql.Tx) error {
		res, err := t.ExecContext(ctx, `INSERT INTO ops(task_id,kind,intent_json,state,created_at,updated_at) VALUES(?,?,?,?,?,?)`,
			task, kind, string(b), string(OpPending), now(), now())
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	return id, err
}

// FinishOp marks an intent done or aborted.
func (s *Store) FinishOp(ctx context.Context, id int64, st OpState) error {
	return s.tx(ctx, func(t *sql.Tx) error {
		_, err := t.ExecContext(ctx, `UPDATE ops SET state=?, updated_at=? WHERE id=?`, string(st), now(), id)
		return err
	})
}

// PendingOps lists unfinished intents, oldest first.
func (s *Store) PendingOps(ctx context.Context) ([]Op, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT id,task_id,kind,intent_json,state FROM ops WHERE state=? ORDER BY id`, string(OpPending))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Op
	for rows.Next() {
		var o Op
		var intent, st string
		if err := rows.Scan(&o.ID, &o.TaskID, &o.Kind, &intent, &st); err != nil {
			return nil, err
		}
		o.Intent, o.State = json.RawMessage(intent), OpState(st)
		out = append(out, o)
	}
	return out, rows.Err()
}

// Snapshot is a recorded snapshot ref.
type Snapshot struct {
	TaskID    string
	Seq       int
	Ref       string
	CommitOID string
	TreeOID   string
	Label     string
	Score     *float64
}

// NextSnapshotSeq returns 1 + the highest seq for task (counting pending
// snapshot intents, so a crashed snapshot's ref name is never reused).
func (s *Store) NextSnapshotSeq(ctx context.Context, task string) (int, error) {
	var maxRow, maxOp sql.NullInt64
	if err := s.r.QueryRowContext(ctx, `SELECT MAX(seq) FROM snapshots WHERE task_id=?`, task).Scan(&maxRow); err != nil {
		return 0, err
	}
	if err := s.r.QueryRowContext(ctx, `SELECT MAX(CAST(json_extract(intent_json,'$.seq') AS INTEGER)) FROM ops WHERE task_id=? AND kind='snapshot'`, task).Scan(&maxOp); err != nil {
		return 0, err
	}
	return int(max(maxRow.Int64, maxOp.Int64)) + 1, nil
}

// CompleteSnapshot inserts the snapshot row and finishes its op atomically.
func (s *Store) CompleteSnapshot(ctx context.Context, opID int64, snap Snapshot) error {
	return s.tx(ctx, func(t *sql.Tx) error {
		if _, err := t.ExecContext(ctx, `INSERT INTO snapshots(task_id,seq,ref,commit_oid,tree_oid,label,score,created_at)
			VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(ref) DO NOTHING`,
			snap.TaskID, snap.Seq, snap.Ref, snap.CommitOID, snap.TreeOID, snap.Label, snap.Score, now()); err != nil {
			return err
		}
		if opID != 0 {
			if _, err := t.ExecContext(ctx, `UPDATE ops SET state=?, updated_at=? WHERE id=?`, string(OpDone), now(), opID); err != nil {
				return err
			}
		}
		return nil
	})
}

// ListSnapshots returns a task's snapshots in sequence order ("" = all tasks).
func (s *Store) ListSnapshots(ctx context.Context, task string) ([]Snapshot, error) {
	q := `SELECT task_id,seq,ref,commit_oid,tree_oid,label,score FROM snapshots`
	args := []any{}
	if task != "" {
		q += ` WHERE task_id=?`
		args = append(args, task)
	}
	rows, err := s.r.QueryContext(ctx, q+` ORDER BY task_id, seq`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Snapshot
	for rows.Next() {
		var sn Snapshot
		var score sql.NullFloat64
		if err := rows.Scan(&sn.TaskID, &sn.Seq, &sn.Ref, &sn.CommitOID, &sn.TreeOID, &sn.Label, &score); err != nil {
			return nil, err
		}
		if score.Valid {
			sn.Score = &score.Float64
		}
		out = append(out, sn)
	}
	return out, rows.Err()
}
