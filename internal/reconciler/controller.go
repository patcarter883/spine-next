// Package reconciler is the level-triggered control loop (RFC §4): a
// workqueue of task ids, a Reconcile that performs at most one externally
// visible step and persists status before returning, asynchronous agent and
// gate operations that re-enqueue the task when they finish, and a
// supervisor restricted to a closed set of typed actions.
package reconciler

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/patcarter883/spine-next/internal/agent"
	"github.com/patcarter883/spine-next/internal/api"
	"github.com/patcarter883/spine-next/internal/config"
	"github.com/patcarter883/spine-next/internal/gate"
	"github.com/patcarter883/spine-next/internal/inbox"
	"github.com/patcarter883/spine-next/internal/storage"
	"github.com/patcarter883/spine-next/internal/worktree"
)

// Deps wires a controller.
type Deps struct {
	Store      *storage.Store
	WT         *worktree.Manager
	Gate       *gate.Engine
	Runner     *agent.Runner
	Config     config.Config
	Supervisor Supervisor // nil = RuleSupervisor
	InboxDir   string     // "" = inbox.Dir(WT.CommonDir())
	Workers    int        // reconcile goroutines (default 4)
	Logf       func(format string, args ...any)
	// ParseAvailable selects the default gates (ts.parse needs CGO).
	ParseAvailable bool
}

// Controller runs the loop.
type Controller struct {
	Deps
	q        *queue
	mu       sync.Mutex
	inflight map[api.TaskID]*flight
	slots    map[api.TaskID]bool
	// staged holds approve/retry/cancel commands until the task's next
	// reconcile applies them, so commands are serialized with reconciles by
	// the per-key queue (a command never races a reconcile of its task).
	staged map[api.TaskID][]inbox.Entry
	seen   map[string]bool // staged inbox paths
	ops    sync.WaitGroup
	ctx    context.Context
}

type flight struct {
	kind   string
	cancel context.CancelFunc
	done   chan struct{}
}

// New returns a controller.
func New(d Deps) *Controller {
	if d.Supervisor == nil {
		d.Supervisor = RuleSupervisor{}
	}
	if d.InboxDir == "" {
		d.InboxDir = inbox.Dir(d.WT.CommonDir())
	}
	if d.Workers <= 0 {
		d.Workers = 4
	}
	if d.Logf == nil {
		d.Logf = func(string, ...any) {}
	}
	if d.Config.Parallel <= 0 {
		d.Config.Parallel = 4
	}
	if d.Config.Resync <= 0 {
		d.Config.Resync = 15 * time.Second
	}
	return &Controller{Deps: d, q: newQueue(), inflight: map[api.TaskID]*flight{}, slots: map[api.TaskID]bool{},
		staged: map[api.TaskID][]inbox.Entry{}, seen: map[string]bool{}}
}

// Run recovers, then reconciles until ctx ends. On return every operation
// it started has finished and every agent process group is gone.
func (c *Controller) Run(ctx context.Context) error {
	c.ctx = ctx
	// 1. Crash recovery BEFORE accepting work (RFC §4.4).
	rep, err := c.WT.Recover(ctx)
	if err != nil {
		return fmt.Errorf("controller: recover: %w", err)
	}
	if rep.ProcsReaped+len(rep.StrayProcsKilled)+rep.LeasesDiscarded+rep.LeasesTornDown+rep.MergesCompleted+rep.SnapshotsRecovered > 0 {
		c.Logf("recovered: %+v", rep)
	}
	if err := c.resumeOrphans(ctx); err != nil {
		return err
	}
	if err := c.drainInbox(ctx); err != nil {
		c.Logf("inbox: %v", err)
	}
	c.resync(ctx)

	var loops sync.WaitGroup
	loops.Add(2)
	go func() {
		defer loops.Done()
		tk := time.NewTicker(300 * time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tk.C:
				if err := c.drainInbox(ctx); err != nil && ctx.Err() == nil {
					c.Logf("inbox: %v", err)
				}
			}
		}
	}()
	go func() {
		defer loops.Done()
		tk := time.NewTicker(c.Config.Resync)
		defer tk.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tk.C:
				c.resync(ctx)
			}
		}
	}()
	var workers sync.WaitGroup
	for i := 0; i < c.Workers; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for c.processNext(ctx) {
			}
		}()
	}
	<-ctx.Done()
	c.q.ShutDown()
	workers.Wait()
	loops.Wait()
	c.mu.Lock()
	for _, f := range c.inflight {
		f.cancel()
	}
	c.mu.Unlock()
	c.ops.Wait()
	return nil
}

func (c *Controller) processNext(ctx context.Context) bool {
	id, ok := c.q.Get()
	if !ok {
		return false
	}
	defer c.q.Done(id)
	res, err := c.Reconcile(ctx, id)
	switch {
	case ctx.Err() != nil:
	case err != nil:
		c.Logf("reconcile %s: %v", id, err)
		c.event(id, "reconcile_error", map[string]any{"error": err.Error()})
		c.q.AddRateLimited(id)
	case res.RequeueAfter > 0:
		c.q.Forget(id)
		c.q.AddAfter(id, res.RequeueAfter)
	case res.Requeue:
		c.q.Add(id)
	default:
		c.q.Forget(id)
	}
	return true
}

// Result tells the queue what to do next.
type Result struct {
	Requeue      bool
	RequeueAfter time.Duration
}

// Enqueue asks for a reconcile of id.
func (c *Controller) Enqueue(id api.TaskID) { c.q.Add(id) }

func (c *Controller) resync(ctx context.Context) {
	ts, err := c.Store.ListTasks(ctx)
	if err != nil {
		c.Logf("resync: %v", err)
		return
	}
	for _, t := range ts {
		if !t.Status.Phase.Terminal() {
			c.q.Add(t.ID)
		}
	}
}

// resumeOrphans handles operations recorded by a previous controller
// process: their goroutines died with it (and Recover killed their process
// groups), so an agent op becomes a Crashed outcome and a gate op is rerun.
func (c *Controller) resumeOrphans(ctx context.Context) error {
	ts, err := c.Store.ListTasks(ctx)
	if err != nil {
		return err
	}
	for _, t := range ts {
		switch t.Status.Phase {
		case api.PhaseProvisioned, api.PhaseWorking, api.PhaseVerifying, api.PhaseVerified:
			c.slots[t.ID] = true
		}
		if t.Status.Phase.Done() {
			// A finished task whose teardown was interrupted still holds
			// its lease: release it now.
			c.teardown(ctx, t.ID, t.Status.Attempt)
		}
		op := t.Status.Op
		if op == nil {
			continue
		}
		switch op.Kind {
		case "agent":
			_, err = c.Store.UpdateStatus(ctx, t.ID, 0, api.SourceAgent, func(s *api.Status) error {
				s.LastOutcome = &api.Outcome{Attempt: op.Attempt, Round: op.Round, Reason: api.OutcomeCrashed,
					Message: "the controller stopped while the agent was running"}
				s.Op = nil
				s.SetCondition(api.CondWorkerSucceeded, false, "ControllerRestarted", "", api.SourceAgent)
				return nil
			})
		default:
			_, err = c.Store.UpdateStatus(ctx, t.ID, 0, api.SourceController, func(s *api.Status) error {
				s.Op = nil
				return nil
			})
		}
		if err != nil {
			return err
		}
		c.event(t.ID, "orphan_op", map[string]any{"op": op})
	}
	return nil
}

func (c *Controller) event(id api.TaskID, kind string, data any) {
	if err := c.Store.AppendEvent(context.WithoutCancel(c.context()), id, kind, data); err != nil {
		c.Logf("event %s/%s: %v", id, kind, err)
	}
}

func (c *Controller) context() context.Context {
	if c.ctx != nil {
		return c.ctx
	}
	return context.Background()
}

func (c *Controller) isInflight(id api.TaskID) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.inflight[id]
	return ok
}

// start runs fn as the task's single in-flight operation.
func (c *Controller) start(id api.TaskID, kind string, fn func(ctx context.Context)) {
	ctx, cancel := context.WithCancel(c.context())
	f := &flight{kind: kind, cancel: cancel, done: make(chan struct{})}
	c.mu.Lock()
	c.inflight[id] = f
	c.mu.Unlock()
	c.ops.Add(1)
	go func() {
		defer c.ops.Done()
		defer close(f.done)
		defer cancel()
		fn(ctx)
		c.mu.Lock()
		delete(c.inflight, id)
		c.mu.Unlock()
		c.q.Add(id)
	}()
}

// stop cancels the task's in-flight operation and waits for it.
func (c *Controller) stop(id api.TaskID) {
	c.mu.Lock()
	f := c.inflight[id]
	c.mu.Unlock()
	if f != nil {
		f.cancel()
		<-f.done
	}
}

func (c *Controller) acquireSlot(id api.TaskID) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.slots[id] {
		return true
	}
	if len(c.slots) >= c.Config.Parallel {
		return false
	}
	c.slots[id] = true
	return true
}

func (c *Controller) releaseSlot(id api.TaskID) {
	c.mu.Lock()
	delete(c.slots, id)
	c.mu.Unlock()
}

// - Inbox commands -

// drainInbox applies submits directly and stages the other commands for
// their task's next reconcile. Files are removed only once applied.
func (c *Controller) drainInbox(ctx context.Context) error {
	es, err := inbox.List(c.InboxDir)
	if err != nil {
		return err
	}
	for _, e := range es {
		if e.Kind == inbox.Submit {
			err := c.apply(ctx, e.Command)
			var pe inbox.Permanent
			switch {
			case errors.As(err, &pe):
				inbox.Reject(e.Path, pe.Err)
			case err != nil:
				return err
			default:
				inbox.Done(e.Path)
			}
			continue
		}
		c.mu.Lock()
		if !c.seen[e.Path] {
			c.seen[e.Path] = true
			c.staged[e.Task] = append(c.staged[e.Task], e)
		}
		c.mu.Unlock()
		c.q.Add(e.Task)
	}
	return nil
}

// applyStaged runs staged commands for id; called at the start of its
// reconcile, so it never interleaves with another step for the same task.
func (c *Controller) applyStaged(ctx context.Context, id api.TaskID) error {
	c.mu.Lock()
	es := c.staged[id]
	delete(c.staged, id)
	c.mu.Unlock()
	for i, e := range es {
		err := c.apply(ctx, e.Command)
		var pe inbox.Permanent
		switch {
		case errors.As(err, &pe):
			inbox.Reject(e.Path, pe.Err)
			c.event(id, "command_rejected", map[string]any{"command": e.Command, "error": pe.Err.Error()})
		case err != nil:
			c.mu.Lock() // keep this and later commands for the next try
			c.staged[id] = append(es[i:], c.staged[id]...)
			c.mu.Unlock()
			return err
		default:
			inbox.Done(e.Path)
		}
		c.mu.Lock()
		delete(c.seen, e.Path)
		c.mu.Unlock()
	}
	return nil
}

// ErrBadCommand is a command that cannot apply to the task's state.
var ErrBadCommand = errors.New("controller: command not applicable")

func (c *Controller) apply(ctx context.Context, cmd inbox.Command) error {
	perm := func(err error) error { return inbox.Permanent{Err: err} }
	switch cmd.Kind {
	case inbox.Submit:
		if cmd.Spec == nil {
			return perm(fmt.Errorf("submit without spec"))
		}
		_, err := c.Store.CreateTask(ctx, cmd.Task, *cmd.Spec)
		if errors.Is(err, storage.ErrExists) {
			return nil // replayed command
		}
		if err != nil {
			return perm(err)
		}
		c.q.Add(cmd.Task)
		return nil
	}
	t, err := c.Store.GetTask(ctx, cmd.Task)
	if errors.Is(err, storage.ErrNotFound) {
		return perm(fmt.Errorf("unknown task %s", cmd.Task))
	}
	if err != nil {
		return err
	}
	switch cmd.Kind {
	case inbox.Approve:
		_, err = c.Store.UpdateStatus(ctx, t.ID, 0, api.SourceHuman, func(s *api.Status) error {
			s.SetCondition(api.CondApproved, true, "Approved", cmd.By, api.SourceHuman)
			if s.Phase == api.PhaseEscalated && s.Reason == "AwaitingApproval" {
				if s.IsTrue(api.CondVerified) {
					s.Phase = api.PhaseVerified
				} else {
					s.Phase = api.PhaseVerifying
				}
				s.Reason, s.Message = "Approved", ""
			}
			return nil
		})
	case inbox.Cancel:
		if t.Status.Phase.Done() {
			return nil
		}
		c.stop(t.ID)
		if _, err = c.Store.UpdateStatus(ctx, t.ID, 0, api.SourceHuman, func(s *api.Status) error {
			s.Phase, s.Reason, s.Message, s.Op = api.PhaseCancelled, "Cancelled", cmd.By, nil
			return nil
		}); err == nil {
			c.teardown(ctx, t.ID, t.Status.Attempt)
			c.releaseSlot(t.ID)
		}
	case inbox.Retry:
		if !t.Status.Phase.Terminal() || t.Status.Phase == api.PhaseIntegrated {
			return perm(fmt.Errorf("%w: retry %s in phase %s", ErrBadCommand, t.ID, t.Status.Phase))
		}
		c.teardown(ctx, t.ID, t.Status.Attempt)
		spec := t.Spec
		b := spec.Budget.WithDefaults()
		if b.MaxAttempts <= t.Status.Attempt {
			b.MaxAttempts = t.Status.Attempt + 1
		}
		spec.Budget = b
		_, err = c.Store.UpdateSpec(ctx, t.ID, 0, spec, func(s *api.Status) {
			s.Phase, s.Reason, s.Message = api.PhasePending, "Retry", ""
			s.PatchRound, s.Worktree, s.Op, s.NextFeedback = 0, nil, nil, nil
			s.StartedAt = time.Now().UTC()
		})
	default:
		return perm(fmt.Errorf("unknown command %q", cmd.Kind))
	}
	if err != nil {
		return err
	}
	c.event(t.ID, "command", cmd)
	return nil
}
