package reconciler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/patcarter883/spine-next/internal/agent"
	"github.com/patcarter883/spine-next/internal/api"
	"github.com/patcarter883/spine-next/internal/modmap"
	"github.com/patcarter883/spine-next/internal/plan"
)

// collectPlan reads a planner run's plan from its worktree, validates it
// and hands it to a person (Escalated/PlanReady). A plan that does not
// parse or has errors goes back to the planner while rounds remain.
func (c *Controller) collectPlan(ctx context.Context, t *api.Task, b api.Budget) (Result, error) {
	lease, err := c.Store.GetLease(ctx, string(t.ID), t.Status.Attempt)
	if err != nil {
		return Result{}, err
	}
	var p api.Plan
	var problems []string
	raw, err := os.ReadFile(filepath.Join(lease.Path, agent.PlanFile))
	switch {
	case err != nil:
		problems = append(problems, fmt.Sprintf("%s was not written", agent.PlanFile))
	case len(raw) > 1<<20:
		problems = append(problems, fmt.Sprintf("%s is larger than 1 MiB", agent.PlanFile))
	default:
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&p); err != nil {
			problems = append(problems, fmt.Sprintf("%s is not valid plan JSON: %v", agent.PlanFile, err))
		}
	}
	var issues []plan.Issue
	if len(problems) == 0 {
		var mods *modmap.Map
		if m, ok, err := modmap.Load(ctx, c.WT.Primary(), lease.BaseOID); ok && err == nil {
			mods = m
		}
		var queued []plan.Queued
		if ts, err := c.Store.ListTasks(ctx); err == nil {
			queued = plan.QueuedFrom(ts, t.ID)
		}
		if links := plan.AutoLink(&p, queued, t.CreatedAt); len(links) > 0 {
			p.Notes = strings.TrimSpace(p.Notes + "\n\n" + plan.LinkNotes(links))
			c.event(t.ID, "plan_linked", map[string]any{"links": links})
		}
		issues = plan.Validate(p, plan.Env{
			Repo:    c.Repo,
			Modules: mods,
			Agents:  c.Config.AdapterNames(),
			Queued:  queued,
			TaskPhase: func(id string) (api.Phase, bool) {
				x, err := c.Store.GetTask(ctx, api.TaskID(id))
				return x.Status.Phase, err == nil
			},
			RepoTask: c.planRepoTask(ctx),
		})
		for _, i := range issues {
			if i.Level == plan.Error {
				problems = append(problems, i.String())
			}
		}
	}
	if len(problems) > 0 && t.Status.PatchRound < b.MaxPatchRounds {
		ds := make([]api.Diagnostic, 0, len(problems))
		for _, m := range problems {
			ds = append(ds, api.Diagnostic{Severity: api.SeverityError, Code: "plan", Message: m})
		}
		c.event(t.ID, "plan_rejected", map[string]any{"problems": problems, "round": t.Status.PatchRound})
		return c.startAgent(ctx, t, b, t.Status.PatchRound+1, []api.Feedback{{Source: "spec.validate", Hint: "Fix these problems and rewrite " + agent.PlanFile + ".", Diagnostics: ds}})
	}
	reason, msg := "PlanReady", fmt.Sprintf("%d tasks proposed; review and submit them", len(p.Tasks))
	if len(problems) > 0 {
		reason, msg = "PlanInvalid", trunc(strings.Join(problems, "; "), 1000)
	}
	if _, err := c.Store.UpdateStatus(ctx, t.ID, 0, api.SourceController, func(s *api.Status) error {
		if len(problems) == 0 || len(p.Tasks) > 0 {
			s.Plan = &p
		}
		return nil
	}); err != nil {
		return Result{}, err
	}
	c.event(t.ID, "plan", map[string]any{"tasks": len(p.Tasks), "issues": issues})
	c.teardown(ctx, t.ID, t.Status.Attempt) // a plan needs no worktree
	return c.escalate(ctx, t, reason, msg)
}

func (c *Controller) planRepoTask(ctx context.Context) func(repo, id string) (api.Phase, bool, bool) {
	if c.ResolveDep == nil {
		return nil
	}
	return func(repo, id string) (api.Phase, bool, bool) {
		ph, err := c.ResolveDep(ctx, repo, api.TaskID(id))
		if err == nil {
			return ph, true, true
		}
		if strings.Contains(err.Error(), "no registered repository") {
			return "", false, false
		}
		return "", true, false
	}
}

// messageBuffer coalesces an agent's streamed text into events of at most
// a few KiB, at most about once a second, so the live view updates without
// writing an event per token.
type messageBuffer struct {
	mu    sync.Mutex
	buf   strings.Builder
	last  time.Time
	flush func(string)
}

func (m *messageBuffer) add(s string) {
	m.mu.Lock()
	m.buf.WriteString(s)
	due := m.buf.Len() >= 4096 || time.Since(m.last) > time.Second
	m.mu.Unlock()
	if due {
		m.drain()
	}
}

func (m *messageBuffer) drain() {
	m.mu.Lock()
	s := m.buf.String()
	m.buf.Reset()
	m.last = time.Now()
	m.mu.Unlock()
	for len(s) > 0 {
		n := len(s)
		if n > 8192 {
			n = 8192
		}
		m.flush(s[:n])
		s = s[n:]
	}
}
