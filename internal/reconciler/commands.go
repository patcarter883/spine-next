package reconciler

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/patcarter883/spine-next/internal/api"
	"github.com/patcarter883/spine-next/internal/inbox"
	"github.com/patcarter883/spine-next/internal/storage"
	"github.com/patcarter883/spine-next/internal/worktree"
)

// MaxHint bounds a person's hint.
const MaxHint = 2000

// restart puts a stopped task back to Pending under spec (a new
// generation), with at least one more attempt allowed.
func (c *Controller) restart(ctx context.Context, t *api.Task, spec api.Spec, reason string, fn func(*api.Status)) error {
	c.teardown(ctx, t.ID, t.Status.Attempt)
	b := spec.Budget.WithDefaults()
	if b.MaxAttempts <= t.Status.Attempt {
		b.MaxAttempts = t.Status.Attempt + 1
	}
	spec.Budget = b
	_, err := c.Store.UpdateSpec(ctx, t.ID, 0, spec, func(s *api.Status) {
		s.Phase, s.Reason, s.Message = api.PhasePending, reason, ""
		s.PatchRound, s.Worktree, s.Op, s.NextFeedback, s.ResumeFrom, s.Proposal = 0, nil, nil, nil, nil, nil
		s.StartedAt = time.Now().UTC()
		if fn != nil {
			fn(s)
		}
	})
	return err
}

// liveLease returns the task's current lease if it still holds a worktree.
func (c *Controller) liveLease(ctx context.Context, t *api.Task) (storage.Lease, bool) {
	if t.Status.Worktree == nil || t.Status.Attempt < 1 {
		return storage.Lease{}, false
	}
	l, err := c.Store.GetLease(ctx, string(t.ID), t.Status.Attempt)
	return l, err == nil && l.State == storage.LeaseReady
}

// resumeInPlace starts another repair round in the task's kept worktree.
func (c *Controller) resumeInPlace(ctx context.Context, t *api.Task, reason string, fb []api.Feedback, fn func(*api.Status)) error {
	_, err := c.Store.UpdateStatus(ctx, t.ID, 0, api.SourceHuman, func(s *api.Status) error {
		s.Phase, s.Reason, s.Message, s.Op, s.Proposal = api.PhaseProvisioned, reason, "", nil, nil
		s.PatchRound++
		s.NextFeedback = fb
		if fn != nil {
			fn(s)
		}
		return nil
	})
	return err
}

// lastGateFeedback is the failing-gate feedback of the task's last round,
// so a resumed round still sees what failed.
func lastGateFeedback(t *api.Task) []api.Feedback {
	g := t.Status.LastGate
	if g == nil || g.Passed || g.Attempt != t.Status.Attempt {
		return nil
	}
	return feedback(Observation{Gate: g, FailsOnBase: g.OnMerge}, api.Decision{})
}

func (c *Controller) applyMore(ctx context.Context, t *api.Task, cmd inbox.Command) error {
	perm := func(format string, args ...any) error {
		return inbox.Permanent{Err: fmt.Errorf("%w: "+format, append([]any{ErrBadCommand}, args...)...)}
	}
	ph := t.Status.Phase
	stopped := ph == api.PhaseEscalated || ph == api.PhaseFailed || ph == api.PhaseCancelled
	switch cmd.Kind {
	case inbox.Hint:
		txt := strings.TrimSpace(cmd.Text)
		if txt == "" {
			return perm("empty hint")
		}
		h := api.Hint{Text: trunc(txt, MaxHint), By: cmd.By, At: cmd.At}
		switch {
		case ph == api.PhaseIntegrated || ph == api.PhasePlanned:
			return perm("%s is %s", t.ID, ph)
		case !ph.Terminal():
			// Running or waiting: the next agent round gets it.
			_, err := c.Store.UpdateStatus(ctx, t.ID, 0, api.SourceHuman, func(s *api.Status) error {
				s.Hints = append(s.Hints, h)
				return nil
			})
			return err
		case ph == api.PhaseEscalated && t.Spec.Kind != api.KindPlan:
			if _, ok := c.liveLease(ctx, t); ok {
				return c.resumeInPlace(ctx, t, "HintGiven", lastGateFeedback(t), func(s *api.Status) { s.Hints = append(s.Hints, h) })
			}
		}
		return c.restart(ctx, t, t.Spec, "RetryWithHint", func(s *api.Status) { s.Hints = append(s.Hints, h) })

	case inbox.Edit:
		if cmd.Spec == nil {
			return perm("edit without a spec")
		}
		spec := *cmd.Spec
		spec.Kind, spec.CreatedBy = t.Spec.Kind, t.Spec.CreatedBy
		if err := api.ValidateSpec(spec); err != nil {
			return inbox.Permanent{Err: err}
		}
		switch {
		case ph == api.PhasePending:
			_, err := c.Store.UpdateSpec(ctx, t.ID, 0, spec, func(s *api.Status) { s.Reason, s.Message = "Edited", "" })
			return err
		case stopped:
			var hint func(*api.Status)
			if txt := strings.TrimSpace(cmd.Text); txt != "" {
				h := api.Hint{Text: trunc(txt, MaxHint), By: cmd.By, At: cmd.At}
				hint = func(s *api.Status) { s.Hints = append(s.Hints, h) }
			}
			return c.restart(ctx, t, spec, "EditedAndRetried", hint)
		}
		return perm("%s is %s: only a pending or stopped task can be edited", t.ID, ph)

	case inbox.Rollback:
		if !stopped {
			return perm("%s is %s: roll back an escalated, failed or cancelled task", t.ID, ph)
		}
		var snap *api.SnapshotRef
		for i := range t.Status.Snapshots {
			if t.Status.Snapshots[i].Seq == cmd.Seq {
				snap = &t.Status.Snapshots[i]
			}
		}
		if snap == nil {
			return perm("%s has no snapshot #%d", t.ID, cmd.Seq)
		}
		msg := fmt.Sprintf("A person rolled your work back to snapshot #%d (%s); the worktree now holds that state. Continue the task from there.", snap.Seq, snap.Label)
		fb := []api.Feedback{{Source: "operator", Hint: msg}}
		if l, ok := c.liveLease(ctx, t); ok && ph == api.PhaseEscalated && snap.Attempt == t.Status.Attempt {
			wl := worktree.Lease{TaskID: l.TaskID, Attempt: l.Attempt, Path: l.Path, Branch: l.Branch, BaseOID: l.BaseOID, AttemptID: l.AttemptID}
			if err := c.WT.Rollback(ctx, &wl, wsnap(t.ID, snap)); err != nil {
				return err
			}
			return c.resumeInPlace(ctx, t, "RolledBack", fb, nil)
		}
		s := *snap
		return c.restart(ctx, t, t.Spec, "RollbackRetry", func(st *api.Status) {
			st.ResumeFrom = &s
			st.NextFeedback = fb
		})

	case inbox.PlanUpdate:
		if t.Spec.Kind != api.KindPlan || ph != api.PhaseEscalated || cmd.Plan == nil {
			return perm("%s is not a plan waiting for review", t.ID)
		}
		p := *cmd.Plan
		_, err := c.Store.UpdateStatus(ctx, t.ID, 0, api.SourceHuman, func(s *api.Status) error {
			s.Plan = &p
			return nil
		})
		if err == nil {
			c.event(t.ID, "plan_edited", map[string]any{"tasks": len(p.Tasks), "by": cmd.By})
		}
		return err

	case inbox.PlanAccept:
		if t.Spec.Kind != api.KindPlan || ph != api.PhaseEscalated {
			return perm("%s is not a plan waiting for review", t.ID)
		}
		_, err := c.Store.UpdateStatus(ctx, t.ID, 0, api.SourceHuman, func(s *api.Status) error {
			s.Phase, s.Reason, s.Message = api.PhasePlanned, "PlanAccepted", trunc(cmd.Text, 2000)
			return nil
		})
		return err
	}
	return inbox.Permanent{Err: fmt.Errorf("unknown command %q", cmd.Kind)}
}

func wsnap(id api.TaskID, s *api.SnapshotRef) *worktree.Snapshot {
	return &worktree.Snapshot{TaskID: string(id), Seq: s.Seq, Ref: s.Ref, CommitOID: s.Commit, TreeOID: s.Tree, Label: s.Label}
}

// applyRepoCommand runs a repository-wide command (no task).
func (c *Controller) applyRepoCommand(ctx context.Context, e inbox.Entry) {
	switch e.Kind {
	case inbox.GC:
		rep, err := c.WT.GC(ctx, e.Days)
		if err != nil {
			c.Logf("gc: %v", err)
			if ctx.Err() != nil {
				return // retried at the next start
			}
			inbox.Reject(e.Path, err)
			return
		}
		c.Logf("gc: removed %d snapshot refs, %d stray worktrees, %d cache entries (%d errors)",
			len(rep.SnapshotRefs), len(rep.StrayWorktrees), rep.OldCacheRows, len(rep.Errors))
		c.event("", "gc", map[string]any{"by": e.By, "report": rep})
		inbox.Done(e.Path)
	default:
		inbox.Reject(e.Path, fmt.Errorf("unknown repository command %q", e.Kind))
	}
}
