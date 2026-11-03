package ui

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strings"

	"github.com/patcarter883/spine-next/internal/agent"
	"github.com/patcarter883/spine-next/internal/api"
	"github.com/patcarter883/spine-next/internal/config"
	"github.com/patcarter883/spine-next/internal/gate"
	"github.com/patcarter883/spine-next/internal/gitro"
	"github.com/patcarter883/spine-next/internal/inbox"
	"github.com/patcarter883/spine-next/internal/plan"
)

// - Gates -

type gateView struct {
	Name        string         `json:"name"`
	Tool        string         `json:"tool"`
	Policy      api.GatePolicy `json:"policy"`
	Timeout     string         `json:"timeout,omitempty"`
	Args        map[string]any `json:"args,omitempty"`
	Description string         `json:"description"`
}

// gates shows what "done" means for a base: its .spine/gates.yaml (always
// read from the base commit, never a worktree) or the defaults.
func (s *Server) gates(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.repo(r)
	if !ok {
		httpError(w, http.StatusNotFound, "unknown repository")
		return
	}
	cfg, err := config.Load(repo.Path)
	if err != nil {
		httpError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	base := cfg.Base
	if b := strings.TrimSpace(r.URL.Query().Get("base")); b != "" {
		base = config.FullRef(b)
	}
	ctx := r.Context()
	git := gitro.Repo{Dir: repo.Path}
	commit, err := git.Line(ctx, "rev-parse", "--verify", "-q", base+"^{commit}")
	if err != nil {
		httpError(w, http.StatusNotFound, fmt.Sprintf("%s does not exist", strings.TrimPrefix(base, "refs/heads/")))
		return
	}
	raw, exists, err := git.Show(ctx, commit, gate.ConfigPath)
	if err != nil {
		httpError(w, 500, err.Error())
		return
	}
	gc, err := gatesAt(ctx, repo.Path, commit)
	out := map[string]any{"base": strings.TrimPrefix(base, "refs/heads/"), "commit": commit, "path": gate.ConfigPath,
		"exists": exists, "raw": string(raw), "gates": []gateView{}}
	if err != nil {
		out["error"] = err.Error()
		writeJSON(w, out)
		return
	}
	var vs []gateView
	for _, n := range gc.Order {
		g := gc.Gates[n]
		g.Name = n
		v := gateView{Name: n, Tool: g.Tool, Policy: g.Policy, Args: g.Args, Description: agent.DescribeGate(g)}
		if v.Policy == "" {
			v.Policy = api.Absolute
		}
		if g.Timeout > 0 {
			v.Timeout = g.Timeout.String()
		}
		vs = append(vs, v)
	}
	out["gates"] = vs
	out["baseline_link"] = gc.Baseline.Link
	writeJSON(w, out)
}

// - Templates -

func (s *Server) userTemplates() (config.UserTemplates, bool) {
	if s.Home == "" {
		return config.UserTemplates{}, false
	}
	return config.UserTemplates{Path: filepath.Join(s.Home, "templates.yaml")}, true
}

// templates lists a repository's presets (.spine/templates.yaml) and the
// dashboard's own.
func (s *Server) templates(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.repo(r)
	if !ok {
		httpError(w, http.StatusNotFound, "unknown repository")
		return
	}
	out := map[string]any{"templates": []config.Template{}, "editable": false}
	var all []config.Template
	var errs []string
	if ts, err := config.LoadTemplates(repo.Path); err != nil {
		errs = append(errs, err.Error())
	} else {
		all = append(all, ts...)
	}
	if u, ok := s.userTemplates(); ok {
		out["editable"] = true
		if ts, err := u.Load(); err != nil {
			errs = append(errs, err.Error())
		} else {
			all = append(all, ts...)
		}
	}
	if all != nil {
		out["templates"] = all
	}
	if errs != nil {
		out["errors"] = errs
	}
	writeJSON(w, out)
}

func (s *Server) putTemplate(w http.ResponseWriter, r *http.Request) {
	u, ok := s.userTemplates()
	if !ok {
		httpError(w, http.StatusNotImplemented, "this dashboard has no home directory for templates")
		return
	}
	var t config.Template
	if !decode(w, r, &t) {
		return
	}
	t.Source = ""
	note(r, "", "", "template "+t.Name)
	if err := u.Put(t); err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) deleteTemplate(w http.ResponseWriter, r *http.Request) {
	u, ok := s.userTemplates()
	if !ok {
		httpError(w, http.StatusNotImplemented, "this dashboard has no home directory for templates")
		return
	}
	note(r, "", "", "delete template "+r.PathValue("name"))
	if err := u.Delete(r.PathValue("name")); err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// - Plans -

type planRequest struct {
	Goal            string `json:"goal"`
	Agent           string `json:"agent,omitempty"`
	Base            string `json:"base,omitempty"`
	MaxRounds       int    `json:"max_rounds,omitempty"`
	RequireApproval bool   `json:"require_approval,omitempty"` // default for the planned tasks
}

// startPlan submits a planner task: an agent reads the repository and
// proposes a task breakdown for the goal, which comes back for review.
func (s *Server) startPlan(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.repo(r)
	if !ok {
		httpError(w, http.StatusNotFound, "unknown repository")
		return
	}
	var req planRequest
	if !decode(w, r, &req) {
		return
	}
	cfg, err := config.Load(repo.Path)
	if err != nil {
		httpError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	spec, err := cfg.NewSpec(config.TaskInput{Intent: req.Goal, Title: "Plan: " + firstLine(req.Goal, 60), Agent: req.Agent, Base: req.Base,
		MaxAttempts: 1, MaxRounds: max(req.MaxRounds, 2), CreatedBy: by(r)})
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	spec.Kind = api.KindPlan
	id := api.TaskID("plan-" + string(api.NewTaskID(req.Goal)))
	if len(id) > 64 {
		id = id[:64]
	}
	note(r, repo.Name, string(id), "plan")
	if _, err := inbox.Write(repo.InboxDir, inbox.Command{Kind: inbox.Submit, Task: id, Spec: &spec, By: by(r)}); err != nil {
		httpError(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]any{"id": id, "repo": repo.Name, "controller": repo.State == "running"})
}

func firstLine(s string, n int) string {
	s = strings.TrimSpace(strings.SplitN(strings.TrimSpace(s), "\n", 2)[0])
	if len(s) > n {
		s = s[:n] + "…"
	}
	return s
}

type planBody struct {
	Plan     api.Plan `json:"plan"`
	PlanTask string   `json:"plan_task,omitempty"` // the planner task the plan came from
	Base     string   `json:"base,omitempty"`
}

// planEnv gathers what validation consults for a repository.
func (s *Server) planEnv(ctx context.Context, repo Repo, base string) (plan.Env, config.Config, error) {
	cfg, err := config.Load(repo.Path)
	if err != nil {
		return plan.Env{}, cfg, err
	}
	if base == "" {
		base = cfg.Base
	} else {
		base = config.FullRef(base)
	}
	env := plan.Env{Repo: repo.Name, Agents: cfg.AdapterNames()}
	env.TaskPhase = func(id string) (api.Phase, bool) {
		t, err := repo.Store.GetTask(ctx, api.TaskID(id))
		return t.Status.Phase, err == nil
	}
	if s.Multi {
		repos := s.repos(ctx)
		env.RepoTask = func(name, id string) (api.Phase, bool, bool) {
			for _, x := range repos {
				if x.Name == name && x.Store != nil {
					t, err := x.Store.GetTask(ctx, api.TaskID(id))
					return t.Status.Phase, true, err == nil
				}
			}
			return "", false, false
		}
	}
	if gc, err := gatesAt(ctx, repo.Path, base); err == nil {
		env.Gates = gc.Order
	}
	if out, err := (gitro.Repo{Dir: repo.Path}).Run(ctx, "ls-tree", "-r", "-z", "--name-only", base); err == nil {
		for _, f := range strings.Split(string(out), "\x00") {
			if f != "" {
				env.Files = append(env.Files, f)
			}
		}
	}
	return env, cfg, nil
}

func (s *Server) validatePlan(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.repo(r)
	if !ok {
		httpError(w, http.StatusNotFound, "unknown repository")
		return
	}
	var req planBody
	if !decode(w, r, &req) {
		return
	}
	env, _, err := s.planEnv(r.Context(), repo, req.Base)
	if err != nil {
		httpError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	is := plan.Validate(req.Plan, env)
	if is == nil {
		is = []plan.Issue{}
	}
	writeJSON(w, map[string]any{"issues": is, "ok": !plan.HasErrors(is), "order": topo(req.Plan)})
}

// topo orders plan task ids so dependencies come first (cycles last).
func topo(p api.Plan) []string {
	deps := map[string][]string{}
	for _, t := range p.Tasks {
		deps[t.ID] = t.After
	}
	var out []string
	done := map[string]bool{}
	var visit func(string, int)
	visit = func(id string, depth int) {
		if done[id] || depth > len(p.Tasks) {
			return
		}
		for _, d := range deps[id] {
			if _, ok := deps[d]; ok {
				visit(d, depth+1)
			}
		}
		if !done[id] {
			done[id] = true
			out = append(out, id)
		}
	}
	for _, t := range p.Tasks {
		visit(t.ID, 0)
	}
	return out
}

// submitPlan validates the (edited) plan and submits every task through
// the inbox; with plan_task, it also closes the planner task.
func (s *Server) submitPlan(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.repo(r)
	if !ok {
		httpError(w, http.StatusNotFound, "unknown repository")
		return
	}
	var req planBody
	if !decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	env, _, err := s.planEnv(ctx, repo, req.Base)
	if err != nil {
		httpError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	is := plan.Validate(req.Plan, env)
	if plan.HasErrors(is) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		writeJSON(w, map[string]any{"error": "the plan has errors", "issues": is})
		return
	}
	if req.PlanTask != "" {
		pt, err := repo.Store.GetTask(ctx, api.TaskID(req.PlanTask))
		if err != nil || pt.Spec.Kind != api.KindPlan || pt.Status.Phase != api.PhaseEscalated {
			httpError(w, http.StatusConflict, "the planner task is not waiting for review")
			return
		}
	}
	byID := map[string]api.PlanTask{}
	for _, t := range req.Plan.Tasks {
		byID[t.ID] = t
	}
	var cmds []inbox.Command
	for _, id := range topo(req.Plan) {
		t := byID[id]
		in := config.TaskInput{Title: t.Title, Intent: t.Intent, Base: req.Base, Scope: t.Scope, After: t.After, Agent: t.Agent,
			RequireApproval: t.RequireApproval, MaxAttempts: t.MaxAttempts, MaxRounds: t.MaxRounds, MaxTokens: t.MaxTokens,
			Gates: t.Gates, Template: t.Template}
		spec, err := s.buildSpec(ctx, repo, in, by(r))
		if err != nil {
			httpError(w, http.StatusBadRequest, id+": "+err.Error())
			return
		}
		cmds = append(cmds, inbox.Command{Kind: inbox.Submit, Task: api.TaskID(id), Spec: &spec, By: by(r)})
	}
	var ids []string
	for _, c := range cmds {
		if _, err := inbox.Write(repo.InboxDir, c); err != nil {
			httpError(w, 500, fmt.Sprintf("submitted %v, then: %v", ids, err))
			return
		}
		ids = append(ids, string(c.Task))
	}
	if req.PlanTask != "" {
		inbox.Write(repo.InboxDir, inbox.Command{Kind: inbox.PlanAccept, Task: api.TaskID(req.PlanTask), By: by(r),
			Text: fmt.Sprintf("submitted %d tasks: %s", len(ids), strings.Join(ids, ", "))})
	}
	note(r, repo.Name, req.PlanTask, fmt.Sprintf("submit plan (%d tasks)", len(ids)))
	writeJSON(w, map[string]any{"submitted": ids, "repo": repo.Name, "issues": is, "controller": repo.State == "running"})
}

// - Dependency graph -

type graphNode struct {
	taskRow
	Key      string   `json:"key"` // repo/id
	Deps     []string `json:"deps"`
	State    string   `json:"state"` // done, running, ready, blocked, waiting, failed, needs
	Critical bool     `json:"critical"`
	Missing  bool     `json:"missing,omitempty"` // a dependency that does not exist
}

// graph returns tasks and their `after` links across repositories, with
// each task's state and the critical path: the longest chain of unfinished
// work, weighted by the median duration of finished tasks.
func (s *Server) graph(w http.ResponseWriter, r *http.Request) {
	ov := s.overview(r.Context())
	scope := r.URL.Query().Get("repo")
	showDone := r.URL.Query().Get("done") != "0"
	nodes := map[string]*graphNode{}
	for _, t := range ov.Tasks {
		k := t.Repo + "/" + string(t.ID)
		nodes[k] = &graphNode{taskRow: t, Key: k, Deps: []string{}}
	}
	for _, n := range nodes {
		for _, d := range n.After {
			repo, id, cross := strings.Cut(d, ":")
			dk := n.Repo + "/" + d
			if cross {
				dk = repo + "/" + id
			}
			if _, ok := nodes[dk]; !ok {
				rn, idn, _ := strings.Cut(dk, "/")
				nodes[dk] = &graphNode{taskRow: taskRow{Repo: rn, ID: api.TaskID(idn), Title: idn}, Key: dk, Deps: []string{}, Missing: true}
			}
			n.Deps = append(n.Deps, dk)
		}
	}
	for _, n := range nodes {
		n.State = graphState(n, nodes)
	}
	// Durations of finished tasks estimate the remaining work.
	var durs []float64
	for _, n := range nodes {
		if n.Phase == api.PhaseIntegrated && !n.Started.IsZero() {
			durs = append(durs, n.Updated.Sub(n.Started).Minutes())
		}
	}
	sort.Float64s(durs)
	est := 30.0
	if len(durs) > 0 {
		est = durs[len(durs)/2]
	}
	cost := map[string]float64{}
	next := map[string]string{}
	var longest func(k string, depth int) float64
	longest = func(k string, depth int) float64 {
		if v, ok := cost[k]; ok {
			return v
		}
		n := nodes[k]
		if n == nil || depth > len(nodes) {
			return 0
		}
		self := 0.0
		switch n.State {
		case "done":
		case "running":
			self = est / 2
		default:
			self = est
		}
		best, via := 0.0, ""
		for _, d := range n.Deps {
			if c := longest(d, depth+1); c > best {
				best, via = c, d
			}
		}
		cost[k], next[k] = self+best, via
		return cost[k]
	}
	var top string
	for k, n := range nodes {
		if n.State == "done" {
			continue
		}
		if c := longest(k, 0); c > 0 && (top == "" || c > cost[top]) {
			top = k
		}
	}
	for k := top; k != ""; k = next[k] {
		if n := nodes[k]; n != nil && n.State != "done" {
			n.Critical = true
		}
	}
	out := []*graphNode{}
	keep := map[string]bool{}
	for k, n := range nodes {
		if scope != "" && n.Repo != scope {
			continue
		}
		if !showDone && n.State == "done" && !hasDependent(k, nodes) {
			continue
		}
		keep[k] = true
	}
	// Keep cross-repository dependencies of shown tasks visible.
	for k := range keep {
		for _, d := range nodes[k].Deps {
			keep[d] = true
		}
	}
	for k := range keep {
		out = append(out, nodes[k])
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	writeJSON(w, map[string]any{"nodes": out, "critical_minutes": cost[top], "estimate_minutes": est})
}

func hasDependent(k string, nodes map[string]*graphNode) bool {
	for _, n := range nodes {
		if n.State == "done" {
			continue
		}
		for _, d := range n.Deps {
			if d == k {
				return true
			}
		}
	}
	return false
}

func graphState(n *graphNode, nodes map[string]*graphNode) string {
	if n.Missing {
		return "missing"
	}
	switch n.Phase {
	case api.PhaseIntegrated, api.PhasePlanned:
		return "done"
	case api.PhaseFailed, api.PhaseCancelled:
		return "failed"
	case api.PhaseEscalated:
		return "needs"
	case api.PhaseProvisioned, api.PhaseWorking, api.PhaseVerifying, api.PhaseVerified:
		return "running"
	}
	for _, d := range n.Deps {
		if dn := nodes[d]; dn == nil || dn.Missing || (dn.Phase != api.PhaseIntegrated) {
			return "blocked"
		}
	}
	return "ready"
}
