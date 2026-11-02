package worktree

import (
	"context"
	"errors"

	"github.com/patcarter883/spine-next/internal/exec"
	"github.com/patcarter883/spine-next/internal/storage"
)

// Run executes c inside a lease and records its process group durably before
// waiting on it (RFC §6.1 "agent spawn" intent), so Recover can kill it after
// a supervisor crash. Hooks, tools and agents all run through here.
func (m *Manager) Run(ctx context.Context, l *Lease, c exec.Cmd) (exec.Result, error) {
	if c.Dir == "" {
		c.Dir = l.Path
	}
	var id int64
	userStart := c.OnStart
	c.OnStart = func(s exec.Started) error {
		var err error
		id, err = m.cfg.Store.RecordProc(ctx, storage.Proc{
			TaskID: l.TaskID, Attempt: l.Attempt, PGID: s.PGID, StartTime: s.StartTime, Cwd: c.Dir,
		})
		if err == nil && userStart != nil {
			err = userStart(s)
		}
		return err
	}
	res, err := exec.Run(ctx, c)
	if id != 0 {
		// Use a fresh context: the run's ctx may be cancelled, and the row
		// must still be closed. Run has already ensured the group is gone.
		err = errors.Join(err, m.cfg.Store.SetProcState(context.WithoutCancel(ctx), id, storage.ProcExited))
	}
	return res, err
}

func opState(s string) storage.OpState { return storage.OpState(s) }
