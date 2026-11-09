package reconciler

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/patcarter883/spine-next/internal/agent"
	"github.com/patcarter883/spine-next/internal/api"
	"github.com/patcarter883/spine-next/internal/gate"
	"github.com/patcarter883/spine-next/internal/inbox"
	"github.com/patcarter883/spine-next/internal/plan"
	"github.com/patcarter883/spine-next/internal/storage"
	"github.com/patcarter883/spine-next/internal/worktree"
)

// Reconcile performs at most one externally visible step for a task and
// persists the new status before returning (RFC §3.5). Long work (agent
// runs, gates) starts asynchronously; its completion re-enqueues the task.
func (c *Controller) Reconcile(ctx context.Context, id api.TaskID) (Result, error) {
	if err := c.applyStaged(ctx, id); err != nil {
		return Result{}, err
	}
	t, err := c.Store.GetTask(ctx, id)
	if errors.Is(err, storage.ErrNotFound) {
		return Result{}, nil
	}
	if err != nil {
		return Result{}, err
	}
	if t.Status.Phase.Terminal() {
		c.releaseSlot(id)
		return Result{}, nil
	}
	if c.isInflight(id) {
		return Result{RequeueAfter: 10 * time.Second}, nil // completion re-enqueues; this is a watchdog
	}
	b := t.Spec.Budget.WithDefaults()
	if !t.Status.StartedAt.IsZero() && time.Since(t.Status.StartedAt) > b.MaxWallClock {
		return c.finish(ctx, &t, api.PhaseFailed, "WallClockExceeded", fmt.Sprintf("ran longer than %s", b.MaxWallClock))
	}
	if b.MaxTokens > 0 && t.Status.TokensUsed > b.MaxTokens {
		return c.finish(ctx, &t, api.PhaseFailed, "TokenBudgetExceeded", fmt.Sprintf("%d tokens used, budget %d", t.Status.TokensUsed, b.MaxTokens))
	}

	switch t.Status.Phase {
	case api.PhasePending:
		return c.provision(ctx, &t, b)

	case api.PhaseProvisioned:
		if !c.acquireSlot(t.ID) { // resumed by a person after its slot was released
			return Result{RequeueAfter: 2 * time.Second}, nil
		}
		return c.startAgent(ctx, &t, b, t.Status.PatchRound, t.Status.NextFeedback)

	case api.PhaseWorking:
		if t.Status.Op != nil { // left by an operation that is no longer running
			return c.clearOp(ctx, &t)
		}
		out := t.Status.LastOutcome
		if out == nil || out.Attempt != t.Status.Attempt || out.Round != t.Status.PatchRound {
			return c.startAgent(ctx, &t, b, t.Status.PatchRound, t.Status.NextFeedback)
		}
		snap, err := c.snapshot(ctx, &t)
		if err != nil {
			return Result{}, err
		}
		if out.Reason != api.OutcomeCompleted {
			// Do not gate a half-written tree; let the supervisor choose.
			return c.decide(ctx, &t, b, snap, nil, false)
		}
		if t.Spec.Kind == api.KindPlan {
			return c.collectPlan(ctx, &t, b)
		}
		return c.startGate(ctx, &t, snap, "", "", false)

	case api.PhaseVerifying:
		if t.Status.Op != nil {
			return c.clearOp(ctx, &t)
		}
		snap := lastSnapshot(&t)
		if snap == nil {
			return c.startAgent(ctx, &t, b, t.Status.PatchRound, nil)
		}
		g := t.Status.LastGate
		if g != nil && g.OnMerge && g.Attempt == t.Status.Attempt && g.Round == t.Status.PatchRound {
			if g.Passed {
				return c.integrate(ctx, &t, b, snap)
			}
			return c.decide(ctx, &t, b, snap, nil, true)
		}
		if g == nil || g.OnMerge || g.Commit != snap.Commit {
			return c.startGate(ctx, &t, snap, "", "", false)
		}
		if g.Passed {
			return c.integrate(ctx, &t, b, snap)
		}
		return c.decide(ctx, &t, b, snap, nil, false)

	case api.PhaseVerified:
		snap := lastSnapshot(&t)
		if snap == nil {
			return Result{}, fmt.Errorf("task %s verified without a snapshot", id)
		}
		return c.integrate(ctx, &t, b, snap)
	}
	return Result{}, fmt.Errorf("reconcile: unhandled phase %q", t.Status.Phase)
}

func lastSnapshot(t *api.Task) *api.SnapshotRef {
	if n := len(t.Status.Snapshots); n > 0 {
		s := t.Status.Snapshots[n-1]
		return &s
	}
	return nil
}

func (c *Controller) clearOp(ctx context.Context, t *api.Task) (Result, error) {
	_, err := c.Store.UpdateStatus(ctx, t.ID, 0, api.SourceController, func(s *api.Status) error {
		s.Op = nil
		return nil
	})
	return Result{Requeue: true}, err
}

// - Pending -

func (c *Controller) provision(ctx context.Context, t *api.Task, b api.Budget) (Result, error) {
	for _, dep := range t.Spec.DependsOn {
		phase, err := c.depPhase(ctx, dep)
		if errors.Is(err, storage.ErrNotFound) && time.Since(t.CreatedAt) < UnknownDependencyGrace {
			// Tasks may be submitted in any order (and across repositories):
			// wait a while for the dependency to appear before escalating.
			return Result{RequeueAfter: 5 * time.Second}, nil
		}
		if errors.Is(err, storage.ErrNotFound) || errors.Is(err, errNoCrossRepo) {
			return c.escalate(ctx, t, "UnknownDependency", string(dep)+": "+err.Error())
		}
		if err != nil {
			return Result{}, err
		}
		switch phase {
		case api.PhaseIntegrated:
		case api.PhaseFailed, api.PhaseCancelled:
			return c.escalate(ctx, t, "DependencyFailed", string(dep))
		default:
			return Result{RequeueAfter: 5 * time.Second}, nil
		}
	}
	if t.Status.Attempt >= b.MaxAttempts {
		return c.finish(ctx, t, api.PhaseFailed, "AttemptsExhausted", fmt.Sprintf("%d attempts", t.Status.Attempt))
	}
	if p, paused := inbox.Paused(c.InboxDir); paused {
		msg := "repository paused"
		if p.By != "" {
			msg += " by " + p.By
		}
		if t.Status.Message != msg {
			c.Store.UpdateStatus(ctx, t.ID, 0, api.SourceController, func(s *api.Status) error {
				s.Message = msg
				return nil
			})
		}
		return Result{RequeueAfter: 3 * time.Second}, nil
	}
	if !c.acquireSlot(t.ID) {
		return Result{RequeueAfter: 2 * time.Second}, nil
	}
	base, err := c.WT.ResolveCommit(ctx, t.Spec.BaseRef)
	if err != nil {
		return c.finish(ctx, t, api.PhaseFailed, "BadBaseRef", err.Error())
	}
	attempt := t.Status.Attempt + 1
	lease, err := c.WT.Create(ctx, string(t.ID), attempt, base)
	if errors.Is(err, worktree.ErrProvisionHook) {
		return c.finish(ctx, t, api.PhaseFailed, "ProvisionFailed", err.Error())
	}
	if err != nil {
		return Result{}, err
	}
	if from := t.Status.ResumeFrom; from != nil {
		old, err := c.Store.GetLease(ctx, string(t.ID), from.Attempt)
		if err != nil {
			return c.escalate(ctx, t, "RollbackFailed", fmt.Sprintf("snapshot #%d: base of attempt %d unknown: %v", from.Seq, from.Attempt, err))
		}
		if err := c.WT.ApplySnapshot(ctx, lease, wsnap(t.ID, from), old.BaseOID); err != nil {
			if errors.Is(err, worktree.ErrSnapshotDoesNotApply) {
				c.teardown(ctx, t.ID, attempt)
				return c.escalate(ctx, t, "RollbackConflict", trunc(err.Error(), 1000))
			}
			return Result{}, err
		}
		c.event(t.ID, "resumed_from_snapshot", map[string]any{"seq": from.Seq, "label": from.Label, "attempt": attempt})
	}
	_, err = c.Store.UpdateStatus(ctx, t.ID, 0, api.SourceWorktree, func(s *api.Status) error {
		s.ResumeFrom = nil
		s.Attempt = attempt
		s.PatchRound = 0
		s.Worktree = &api.WorktreeRef{Path: lease.Path, Branch: lease.Branch, BaseOID: lease.BaseOID, AttemptID: lease.AttemptID}
		s.BestSnapshot = nil
		if s.StartedAt.IsZero() {
			s.StartedAt = time.Now().UTC()
		}
		s.ObservedGeneration = t.Spec.Generation
		s.Phase, s.Reason, s.Message = api.PhaseProvisioned, "LeaseReady", ""
		s.SetCondition(api.CondProvisioned, true, "LeaseReady", lease.Path, api.SourceWorktree)
		return nil
	})
	return Result{Requeue: true}, err
}

// UnknownDependencyGrace is how long a task waits for a dependency that
// does not exist yet before escalating.
var UnknownDependencyGrace = 10 * time.Minute

var errNoCrossRepo = errors.New("cross-repository dependencies need `spine supervisor`")

// depPhase resolves a dependency: a local task id, or "repo:task" for a
// task in another repository under the same supervisor.
func (c *Controller) depPhase(ctx context.Context, dep api.TaskID) (api.Phase, error) {
	repo, id, cross := strings.Cut(string(dep), ":")
	if !cross || repo == c.Repo {
		if cross {
			dep = api.TaskID(id)
		}
		d, err := c.Store.GetTask(ctx, dep)
		return d.Status.Phase, err
	}
	if c.ResolveDep == nil {
		return "", errNoCrossRepo
	}
	return c.ResolveDep(ctx, repo, api.TaskID(id))
}

// - Agent -

func (c *Controller) adapter(t *api.Task) (agent.Adapter, bool) {
	name := t.Spec.Adapter
	if name == "" {
		name = c.Config.Agent
	}
	a, ok := c.Config.Adapters[name]
	return a, ok
}

func (c *Controller) gateConfig(ctx context.Context, t *api.Task, base string) (gate.Config, error) {
	if len(t.Spec.Acceptance) > 0 {
		return gate.FromRefs(t.Spec.Acceptance)
	}
	cfg, ok, err := gate.LoadConfig(ctx, c.WT.Primary(), base)
	if err != nil {
		return gate.Config{}, err
	}
	if !ok {
		return gate.DefaultConfig(c.ParseAvailable), nil
	}
	return cfg, nil
}

func (c *Controller) startAgent(ctx context.Context, t *api.Task, b api.Budget, round int, fb []api.Feedback) (Result, error) {
	a, ok := c.adapter(t)
	if !ok {
		return c.finish(ctx, t, api.PhaseFailed, "UnknownAdapter", t.Spec.Adapter)
	}
	lease, err := c.Store.GetLease(ctx, string(t.ID), t.Status.Attempt)
	if err != nil {
		return Result{}, fmt.Errorf("lease %s/a%d: %w", t.ID, t.Status.Attempt, err)
	}
	var acceptance []string
	if cfg, err := c.gateConfig(ctx, t, lease.BaseOID); err == nil {
		for _, n := range cfg.Order {
			g := cfg.Gates[n]
			g.Name = n
			acceptance = append(acceptance, agent.DescribeGate(g))
		}
	}
	for _, h := range t.Status.Hints {
		src := "a person"
		if h.By != "" {
			src = h.By
		}
		fb = append(fb, api.Feedback{Source: "hint from " + src, Hint: h.Text})
	}
	attempt := t.Status.Attempt
	if _, err := c.Store.UpdateStatus(ctx, t.ID, 0, api.SourceController, func(s *api.Status) error {
		s.Phase, s.Reason, s.Message = api.PhaseWorking, "AgentRunning", ""
		s.PatchRound = round
		s.NextFeedback = nil
		s.Hints = nil
		s.Op = &api.Op{Kind: "agent", Attempt: attempt, Round: round, StartedAt: time.Now().UTC()}
		return nil
	}); err != nil {
		return Result{}, err
	}
	var maxTokens int64
	if b.MaxTokens > 0 {
		maxTokens = b.MaxTokens - t.Status.TokensUsed
		if maxTokens < 1 {
			maxTokens = 1
		}
	}
	prompt := agent.Prompt{Task: t.ID, Attempt: attempt, Round: round, Intent: t.Spec.Intent, Scope: t.Spec.Scope,
		Acceptance: acceptance, Feedback: fb}
	if t.Spec.Kind == api.KindPlan {
		prompt.Plan, prompt.Scope, prompt.Agents, prompt.Gates = true, nil, c.Config.AdapterNames(), acceptance
		prompt.Acceptance = nil
		if ts, err := c.Store.ListTasks(ctx); err == nil {
			prompt.Queued = plan.QueuedFrom(ts, t.ID)
		}
	}
	if t.Spec.Kind == api.KindSurvey && round == 0 {
		if err := c.prepareSurvey(ctx, t, lease.Path, lease.BaseOID); err != nil {
			return c.escalate(ctx, t, "SurveyFailed", "deriving the module map: "+trunc(err.Error(), 500))
		}
	}
	ctxInfo := c.addContext(ctx, t, a, lease.BaseOID, &prompt)
	c.event(t.ID, "agent_start", map[string]any{"attempt": attempt, "round": round, "adapter": a.Name, "feedback": fb,
		"protocol": a.Protocol, "context": ctxInfo})
	id := t.ID
	c.start(id, "agent", func(opCtx context.Context) {
		lastEvent := time.Time{}
		msgs := &messageBuffer{flush: func(text string) { c.event(id, "agent", agent.Event{Kind: "message", Detail: text}) }}
		out, err := c.Runner.Run(opCtx, agent.RunRequest{
			Lease: &lease, Adapter: a, Prompt: prompt, Timeout: b.PerRunTimeout, MaxTokens: maxTokens,
			Events: func(ev agent.Event) {
				if ev.Kind == "message" {
					msgs.add(ev.Detail)
					return
				}
				msgs.drain()
				if ev.Kind == "usage" && time.Since(lastEvent) < 5*time.Second {
					return
				}
				lastEvent = time.Now()
				c.event(id, "agent", ev)
			},
		})
		msgs.drain()
		if err != nil {
			out = api.Outcome{Attempt: attempt, Round: round, Reason: api.OutcomeCrashed, Message: err.Error()}
		}
		wctx := context.WithoutCancel(opCtx)
		if _, err := c.Store.UpdateStatus(wctx, id, 0, api.SourceAgent, func(s *api.Status) error {
			s.LastOutcome = &out
			s.TokensUsed += out.TokensUsed
			if s.Op != nil && s.Op.Kind == "agent" {
				s.Op = nil
			}
			s.SetCondition(api.CondWorkerSucceeded, out.Reason == api.OutcomeCompleted, string(out.Reason), "", api.SourceAgent)
			return nil
		}); err != nil {
			c.Logf("record outcome %s: %v", id, err)
		}
		c.event(id, "outcome", out)
	})
	return Result{}, nil
}

func (c *Controller) snapshot(ctx context.Context, t *api.Task) (*api.SnapshotRef, error) {
	lease, err := c.Store.GetLease(ctx, string(t.ID), t.Status.Attempt)
	if err != nil {
		return nil, err
	}
	label := fmt.Sprintf("a%d-r%d", t.Status.Attempt, t.Status.PatchRound)
	if last := lastSnapshot(t); last != nil && last.Label == label && last.Attempt == t.Status.Attempt {
		// Already taken for this round (a crash after Snapshot, before the
		// gate started). Re-snapshot only if the worktree moved since.
		return c.resnapshotIfChanged(ctx, t, &lease, last, label)
	}
	s, err := c.WT.Snapshot(ctx, &lease, label)
	if err != nil {
		return nil, err
	}
	ref := &api.SnapshotRef{Seq: s.Seq, Ref: s.Ref, Commit: s.CommitOID, Tree: s.TreeOID, Label: label, Attempt: t.Status.Attempt}
	t2, err := c.Store.UpdateStatus(ctx, t.ID, 0, api.SourceWorktree, func(st *api.Status) error {
		st.Snapshots = append(st.Snapshots, *ref)
		return nil
	})
	if err == nil {
		*t = t2
	}
	return ref, err
}

func (c *Controller) resnapshotIfChanged(ctx context.Context, t *api.Task, l *worktree.Lease, last *api.SnapshotRef, label string) (*api.SnapshotRef, error) {
	s, err := c.WT.Snapshot(ctx, l, label)
	if err != nil {
		return nil, err
	}
	if s.TreeOID == last.Tree {
		return last, nil
	}
	ref := &api.SnapshotRef{Seq: s.Seq, Ref: s.Ref, Commit: s.CommitOID, Tree: s.TreeOID, Label: label, Attempt: t.Status.Attempt}
	t2, err := c.Store.UpdateStatus(ctx, t.ID, 0, api.SourceWorktree, func(st *api.Status) error {
		st.Snapshots = append(st.Snapshots, *ref)
		return nil
	})
	if err == nil {
		*t = t2
	}
	return ref, err
}

// - Gate -

// startGate evaluates snap (onMerge=false, against the lease base) or a
// proposed merge commit (onMerge=true, against tip).
func (c *Controller) startGate(ctx context.Context, t *api.Task, snap *api.SnapshotRef, commit, tip string, onMerge bool) (Result, error) {
	lease, err := c.Store.GetLease(ctx, string(t.ID), t.Status.Attempt)
	if err != nil {
		return Result{}, err
	}
	base := lease.BaseOID
	if onMerge {
		base = tip
	} else {
		commit = snap.Commit
	}
	cfg, err := c.gateConfig(ctx, t, base)
	if err != nil {
		return c.escalate(ctx, t, "BadGateConfig", err.Error())
	}
	attempt, round := t.Status.Attempt, t.Status.PatchRound
	if _, err := c.Store.UpdateStatus(ctx, t.ID, 0, api.SourceController, func(s *api.Status) error {
		s.Phase, s.Reason, s.Message = api.PhaseVerifying, "GateRunning", ""
		if onMerge {
			s.Reason = "GateRunningOnMerge"
		}
		s.Op = &api.Op{Kind: "gate", Attempt: attempt, Round: round, StartedAt: time.Now().UTC(), Snapshot: snap,
			Commit: commit, BaseTip: tip, OnMerge: onMerge}
		return nil
	}); err != nil {
		return Result{}, err
	}
	id := t.ID
	scope := t.Spec.Scope
	c.startOp(id, "gate", onMerge, func(opCtx context.Context) {
		v, err := c.Gate.Evaluate(opCtx, gate.Request{Repo: c.WT.Primary(), Commit: commit, Base: base,
			Worktree: lease.Path, Scope: scope, Config: &cfg})
		wctx := context.WithoutCancel(opCtx)
		if err != nil {
			c.event(id, "gate_error", map[string]any{"error": err.Error(), "on_merge": onMerge})
			c.Store.UpdateStatus(wctx, id, 0, api.SourceController, func(s *api.Status) error {
				s.Op, s.Message = nil, "gate infrastructure error: "+trunc(err.Error(), 500)
				return nil
			})
			if opCtx.Err() == nil {
				c.q.AddRateLimited(id)
			}
			return
		}
		sum := summarize(v, attempt, round, commit, onMerge)
		if _, err := c.Store.UpdateStatus(wctx, id, 0, api.SourceGate, func(s *api.Status) error {
			s.LastGate = &sum
			short := sum
			short.Gates = nil
			s.GateHistory = append(s.GateHistory, short)
			if len(s.GateHistory) > 50 {
				s.GateHistory = s.GateHistory[len(s.GateHistory)-50:]
			}
			if s.Op != nil && s.Op.Kind == "gate" {
				s.Op = nil
			}
			if onMerge {
				if v.Passed && s.Phase == api.PhaseVerifying {
					s.SetCondition(api.CondVerified, true, "MergeResultPassed", v.Tree, api.SourceGate)
					s.Phase, s.Reason = api.PhaseVerified, "Verified"
				} else {
					s.SetCondition(api.CondVerified, false, "MergeResultFailed:"+v.FailedGate, v.Tree, api.SourceGate)
				}
				return nil
			}
			s.SetCondition(api.CondGatePassed, v.Passed, gateReason(v), v.Tree, api.SourceGate)
			for i := range s.Snapshots {
				if s.Snapshots[i].Commit == snap.Commit {
					s.Snapshots[i].Score = sum.Score
					if s.BestSnapshot == nil || sum.Score > s.BestSnapshot.Score || s.BestSnapshot.Attempt != attempt {
						b := s.Snapshots[i]
						s.BestSnapshot = &b
					}
				}
			}
			return nil
		}); err != nil {
			c.Logf("record verdict %s: %v", id, err)
		}
		c.event(id, "verdict", map[string]any{"on_merge": onMerge, "attempt": attempt, "round": round, "verdict": v})
	})
	return Result{}, nil
}

func gateReason(v gate.Verdict) string {
	if v.Passed {
		return "AllGatesPassed"
	}
	return "Failed:" + v.FailedGate
}

func summarize(v gate.Verdict, attempt, round int, commit string, onMerge bool) api.GateSummary {
	s := api.GateSummary{Attempt: attempt, Round: round, Tree: v.Tree, Commit: commit, OnMerge: onMerge,
		Passed: v.Passed, FailedGate: v.FailedGate, Gates: v.Gates}
	passed := 0
	for _, g := range v.Gates {
		if g.Passed() {
			passed++
		}
		for _, d := range g.New {
			s.Fingerprints = append(s.Fingerprints, d.Fingerprint)
		}
		s.NewCount += len(g.New)
	}
	sort.Strings(s.Fingerprints)
	s.Score = float64(passed*1000 - s.NewCount)
	if v.Passed {
		s.Score = 1e9
	}
	return s
}

// - Integration (RFC §4.7) -

func commitMessage(t *api.Task) string {
	title := t.Spec.Title
	if title == "" {
		title = strings.TrimSpace(strings.SplitN(t.Spec.Intent, "\n", 2)[0])
	}
	if len(title) > 72 {
		title = title[:72]
	}
	return fmt.Sprintf("%s\n\n%s\n\nSpine-Task: %s\nSpine-Attempt: %d\n", title, strings.TrimSpace(t.Spec.Intent), t.ID, t.Status.Attempt)
}

func (c *Controller) integrate(ctx context.Context, t *api.Task, b api.Budget, snap *api.SnapshotRef) (Result, error) {
	first, ahead, err := c.mergeTurn(ctx, t)
	if err != nil {
		return Result{}, err
	}
	if !first {
		msg := fmt.Sprintf("waiting to merge into %s: %d ahead", strings.TrimPrefix(t.Spec.BaseRef, "refs/heads/"), ahead)
		if t.Status.Reason != "WaitingToMerge" || t.Status.Message != msg {
			if _, err := c.Store.UpdateStatus(ctx, t.ID, 0, api.SourceController, func(s *api.Status) error {
				s.Reason, s.Message = "WaitingToMerge", msg
				return nil
			}); err != nil {
				return Result{}, err
			}
		}
		return Result{RequeueAfter: 3 * time.Second}, nil
	}
	tip, err := c.WT.ResolveCommit(ctx, t.Spec.BaseRef)
	if err != nil {
		return Result{}, err
	}
	ws := &worktree.Snapshot{TaskID: string(t.ID), Seq: snap.Seq, Ref: snap.Ref, CommitOID: snap.Commit, TreeOID: snap.Tree, Label: snap.Label}
	msg := commitMessage(t)
	proposed, pre, err := c.WT.ProposedCommit(ctx, ws, tip, msg)
	if errors.Is(err, worktree.ErrConflict) {
		c.mergeDone(t)
		return c.decide(ctx, t, b, snap, pre.Conflicts, false)
	}
	if err != nil {
		return Result{}, err
	}
	// Only a passing verdict on exactly the tree that would land counts
	// (Verified is GatePassed on the merge result, RFC §4.3).
	g := t.Status.LastGate
	if g == nil || !g.Passed || g.Tree != pre.ResultTree {
		return c.startGate(ctx, t, snap, proposed, tip, true)
	}
	if !t.Status.IsTrue(api.CondVerified) || t.Status.Phase != api.PhaseVerified {
		if _, err := c.Store.UpdateStatus(ctx, t.ID, 0, api.SourceGate, func(s *api.Status) error {
			s.SetCondition(api.CondVerified, true, "ResultTreePassed", pre.ResultTree, api.SourceGate)
			s.Phase, s.Reason = api.PhaseVerified, "Verified"
			return nil
		}); err != nil {
			return Result{}, err
		}
	}
	// Approval comes after the merge result passed its gates, so a person
	// reviews exactly the tree that would land (the dashboard shows its
	// diff). Approval covers the reviewed change (its snapshot tree): if
	// only the base moves, the result is re-gated and approval holds; a new
	// round or attempt changes the snapshot and asks again. Approval is
	// additive: it never excuses a gate.
	approved := t.Status.IsTrue(api.CondApproved) && (t.Status.ApprovedTree == "" || t.Status.ApprovedTree == snap.Tree)
	if t.Spec.Escalation.RequireApprovalBeforeMerge && !approved {
		msg := "verified on the merge result; review the diff and run `spine approve " + string(t.ID) + "` to integrate"
		if t.Status.IsTrue(api.CondApproved) {
			msg = "the change differs from the one you approved (a later round or attempt); review it again"
		}
		if _, err := c.Store.UpdateStatus(ctx, t.ID, 0, api.SourceController, func(s *api.Status) error {
			s.ProposedCommit = proposed
			return nil
		}); err != nil {
			return Result{}, err
		}
		c.mergeDone(t)
		return c.escalate(ctx, t, "AwaitingApproval", msg)
	}
	res, err := c.WT.MergeAt(ctx, ws, t.Spec.BaseRef, tip, msg)
	switch {
	case errors.Is(err, worktree.ErrBaseMoved):
		return Result{Requeue: true}, nil // someone landed first: re-preflight and re-gate
	case errors.Is(err, worktree.ErrConflict):
		c.mergeDone(t)
		return c.decide(ctx, t, b, snap, res.Conflicts, false)
	case errors.Is(err, worktree.ErrCheckoutDirty):
		c.Store.UpdateStatus(ctx, t.ID, 0, api.SourceController, func(s *api.Status) error {
			s.Message = "waiting: " + err.Error()
			return nil
		})
		return Result{RequeueAfter: 15 * time.Second}, nil
	case err != nil:
		return Result{}, err
	}
	if res.ResultTree != pre.ResultTree {
		// Cannot happen with MergeAt at the same tip; refuse to claim success.
		return Result{}, fmt.Errorf("integrated tree %s differs from verified tree %s", res.ResultTree, pre.ResultTree)
	}
	if _, err := c.Store.UpdateStatus(ctx, t.ID, 0, api.SourceWorktree, func(s *api.Status) error {
		s.SetCondition(api.CondIntegrated, true, "CompareAndSwap", res.NewHead, api.SourceWorktree)
		s.IntegratedCommit = res.NewHead
		s.Phase, s.Reason, s.Message = api.PhaseIntegrated, "Integrated", res.NewHead
		return nil
	}); err != nil {
		return Result{}, err
	}
	c.mergeDone(t)
	c.event(t.ID, "integrated", map[string]any{"commit": res.NewHead, "base_ref": t.Spec.BaseRef, "tree": res.ResultTree, "rescued": res.Rescued})
	c.teardown(ctx, t.ID, t.Status.Attempt)
	c.releaseSlot(t.ID)
	return Result{}, nil
}

// - Decisions (RFC §4.6) -

func (c *Controller) decide(ctx context.Context, t *api.Task, b api.Budget, snap *api.SnapshotRef, conflicts []string, failsOnBase bool) (Result, error) {
	obs := Observation{Spec: t.Spec, Status: t.Status, Outcome: t.Status.LastOutcome, Conflicts: conflicts, Budget: b,
		Legal: legalActions(t, b), FailsOnBase: failsOnBase}
	if g := t.Status.LastGate; g != nil && g.Attempt == t.Status.Attempt && g.Round == t.Status.PatchRound && (failsOnBase || g.Commit == snap.Commit) {
		obs.Gate = g
	}
	for _, h := range t.Status.GateHistory {
		if h.Attempt == t.Status.Attempt && !h.OnMerge {
			obs.History = append(obs.History, h)
		}
	}
	if best := t.Status.BestSnapshot; best != nil && best.Attempt == t.Status.Attempt {
		obs.HasBest = true
		obs.BestIsLast = best.Commit == snap.Commit
	}
	by := c.Supervisor.Name()
	d, err := c.Supervisor.Decide(ctx, obs)
	var fallback string
	switch {
	case err != nil:
		fallback = err.Error()
	case !slices.Contains(obs.Legal, d.Action):
		fallback = fmt.Sprintf("illegal action %q (legal: %v)", d.Action, obs.Legal)
	case d.Action == api.ActRollback && !obs.HasBest:
		fallback = "rollback without a best snapshot"
	}
	if fallback != "" {
		d = RuleSupervisor{}.decide(obs)
		by = "rules (fallback)"
	}
	if len(d.Hint) > 500 {
		d.Hint = d.Hint[:500]
	}
	ev := map[string]any{"decision": d, "legal": obs.Legal, "round": t.Status.PatchRound, "attempt": t.Status.Attempt,
		"conflicts": conflicts, "fails_on_base": failsOnBase, "supervisor": by}
	if fallback != "" {
		ev["fallback_reason"] = trunc(fallback, 500)
	}
	c.event(t.ID, "decision", ev)

	fb := feedback(obs, d)
	switch d.Action {
	case api.ActPatchWorker:
		return c.startAgent(ctx, t, b, t.Status.PatchRound+1, fb)
	case api.ActRollback:
		lease, err := c.Store.GetLease(ctx, string(t.ID), t.Status.Attempt)
		if err != nil {
			return Result{}, err
		}
		best := t.Status.BestSnapshot
		if err := c.WT.Rollback(ctx, &lease, &worktree.Snapshot{TaskID: string(t.ID), Seq: best.Seq, Ref: best.Ref,
			CommitOID: best.Commit, TreeOID: best.Tree, Label: best.Label}); err != nil {
			return Result{}, err
		}
		return c.startAgent(ctx, t, b, t.Status.PatchRound+1, fb)
	case api.ActRunWorker:
		c.teardown(ctx, t.ID, t.Status.Attempt)
		_, err := c.Store.UpdateStatus(ctx, t.ID, 0, api.SourceController, func(s *api.Status) error {
			s.Phase, s.Reason, s.Message = api.PhasePending, "FreshAttempt:"+d.Reason, ""
			s.Worktree, s.PatchRound, s.Op = nil, 0, nil
			s.NextFeedback = fb
			return nil
		})
		return Result{Requeue: true}, err
	case api.ActEscalate:
		return c.escalate(ctx, t, d.Reason, d.Hint)
	default:
		return c.finish(ctx, t, api.PhaseFailed, d.Reason, d.Hint)
	}
}

// feedback renders the typed feedback for the next round: new failures only
// (baseline-subtracted), focused fingerprints first.
func feedback(o Observation, d api.Decision) []api.Feedback {
	var out []api.Feedback
	if len(o.Conflicts) > 0 {
		out = append(out, api.Feedback{Source: "merge", Conflicts: o.Conflicts,
			Hint: "The base branch changed underneath this task and these files conflict. Start from the current base and redo the change."})
	}
	if o.Gate != nil && !o.Gate.Passed {
		focus := map[string]bool{}
		for _, f := range d.Focus {
			focus[f] = true
		}
		for _, g := range o.Gate.Gates {
			if g.Passed() || g.Skipped {
				continue
			}
			ds := append([]api.Diagnostic{}, g.New...)
			sort.SliceStable(ds, func(i, j int) bool { return focus[ds[i].Fingerprint] && !focus[ds[j].Fingerprint] })
			src := g.Gate + " (" + g.Tool + ")"
			if o.FailsOnBase {
				src += " on the current base"
			}
			fb := api.Feedback{Source: src, Diagnostics: ds}
			if len(ds) == 0 {
				fb.Hint = "The check failed without structured diagnostics (" + g.Reason + ")."
			}
			out = append(out, fb)
		}
	}
	if d.Hint != "" {
		h := d.Hint
		if len(h) > 500 {
			h = h[:500]
		}
		out = append(out, api.Feedback{Source: "supervisor", Hint: h})
	}
	return out
}

// - Terminal transitions -

func (c *Controller) escalate(ctx context.Context, t *api.Task, reason, msg string) (Result, error) {
	_, err := c.Store.UpdateStatus(ctx, t.ID, 0, api.SourceController, func(s *api.Status) error {
		s.Phase, s.Reason, s.Message, s.Op = api.PhaseEscalated, reason, msg, nil
		return nil
	})
	c.releaseSlot(t.ID) // the worktree is kept for a human; the slot is not
	return Result{}, err
}

func (c *Controller) finish(ctx context.Context, t *api.Task, phase api.Phase, reason, msg string) (Result, error) {
	_, err := c.Store.UpdateStatus(ctx, t.ID, 0, api.SourceController, func(s *api.Status) error {
		s.Phase, s.Reason, s.Message, s.Op = phase, reason, msg, nil
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	c.teardown(ctx, t.ID, t.Status.Attempt)
	c.releaseSlot(t.ID)
	return Result{}, nil
}

// teardown releases a lease if it is still held. Snapshot refs are kept.
// It runs to completion even during shutdown (it is bounded by git
// timeouts), and a teardown that still fails is retried at the next start
// (resumeOrphans).
func (c *Controller) teardown(ctx context.Context, id api.TaskID, attempt int) {
	ctx = context.WithoutCancel(ctx)
	if attempt <= 0 {
		return
	}
	l, err := c.Store.GetLease(ctx, string(id), attempt)
	if errors.Is(err, storage.ErrNotFound) || (err == nil && l.State == storage.LeaseReleased) {
		return
	}
	if err == nil {
		err = c.WT.Teardown(ctx, &l)
	}
	if err != nil && !errors.Is(err, worktree.ErrLeaseReleased) {
		c.Logf("teardown %s/a%d: %v", id, attempt, err)
		c.event(id, "teardown_error", map[string]any{"attempt": attempt, "error": err.Error()})
	}
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
