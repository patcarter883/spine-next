// Package plan validates proposed task sets (the RFC's spec.validate): a
// planner task's output, a plan edited in the dashboard, or a plan file
// given to `spine plan`. It checks each task on its own (id, intent, scope,
// agent, gates) and the set as a whole (dependencies resolve, no cycles,
// tasks that may run in parallel do not claim the same paths).
package plan

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/patcarter883/spine-next/internal/api"
	"github.com/patcarter883/spine-next/internal/modmap"
	"github.com/patcarter883/spine-next/internal/tools/diff"
)

// Level of an issue. Errors block submission; warnings do not.
type Level string

const (
	Error   Level = "error"
	Warning Level = "warning"
)

// Issue is one finding.
type Issue struct {
	Level   Level  `json:"level"`
	Task    string `json:"task,omitempty"` // "" = the plan as a whole
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
}

func (i Issue) String() string {
	where := i.Task
	if i.Field != "" {
		where += "." + i.Field
	}
	if where != "" {
		return fmt.Sprintf("%s: %s: %s", i.Level, where, i.Message)
	}
	return fmt.Sprintf("%s: %s", i.Level, i.Message)
}

// Env is what validation may consult. Nil funcs and empty lists skip the
// corresponding check.
type Env struct {
	// TaskPhase looks up an existing task in this repository.
	TaskPhase func(id string) (api.Phase, bool)
	// RepoTask looks up "repo:task" in another repository; ok=false if the
	// repository is unknown. nil = cross-repository dependencies are an error.
	RepoTask func(repo, id string) (phase api.Phase, repoKnown, taskKnown bool)
	// Repo is this repository's name (a "repo:task" naming it is local).
	Repo string
	// Agents are the configured adapter names.
	Agents []string
	// Gates are the gate names at the base.
	Gates []string
	// Files are the paths at the base (scope globs that match none warn).
	Files []string
	// Modules is the base's module map (.spine/modules.yaml), if any: the
	// overlap check then also compares modules and co-change history, and
	// a scope that changes a module without its tests is flagged.
	Modules *modmap.Map
	// AllowExisting accepts ids that already exist (re-validating a plan
	// after part of it was submitted).
	AllowExisting bool
	// Queued is the work already in the repository: unfinished tasks, and
	// tasks other plans propose that are still waiting for review. Plan
	// tasks may depend on them, and are checked for overlapping scopes.
	Queued []Queued
	// Submitting makes a dependency on a task that is only proposed an
	// error: it would never appear.
	Submitting bool
}

// Queued is a task that is not finished, or that a plan awaiting review
// proposes (Plan is then that planner task's id).
type Queued struct {
	ID    string   `json:"id"`
	Title string   `json:"title,omitempty"`
	State string   `json:"state"`
	Scope []string `json:"scope,omitempty"`
	After []string `json:"after,omitempty"`
	Plan  string   `json:"plan,omitempty"`
}

// QueuedFrom lists the unfinished tasks and the tasks proposed by plans
// awaiting review, leaving out the planner task self.
func QueuedFrom(ts []api.Task, self api.TaskID) []Queued {
	var out []Queued
	for _, t := range ts {
		if t.ID == self || t.Status.Phase.Done() {
			continue
		}
		switch t.Spec.Kind {
		case api.KindPlan:
			if t.Status.Phase != api.PhaseEscalated || t.Status.Plan == nil {
				continue
			}
			for _, pt := range t.Status.Plan.Tasks {
				out = append(out, Queued{ID: strings.TrimSpace(pt.ID), Title: pt.Title, State: "proposed", Scope: pt.Scope, After: pt.After, Plan: string(t.ID)})
			}
		case api.KindSurvey:
		default:
			q := Queued{ID: string(t.ID), Title: t.Spec.Title, State: string(t.Status.Phase), Scope: t.Spec.Scope}
			for _, d := range t.Spec.DependsOn {
				q.After = append(q.After, string(d))
			}
			out = append(out, q)
		}
	}
	return out
}

// MaxTasks bounds a plan.
const MaxTasks = 50

// Validate checks a plan. Issues are sorted errors first.
func Validate(p api.Plan, env Env) []Issue {
	var out []Issue
	add := func(l Level, task, field, format string, args ...any) {
		out = append(out, Issue{Level: l, Task: task, Field: field, Message: fmt.Sprintf(format, args...)})
	}
	if len(p.Tasks) == 0 {
		add(Error, "", "", "the plan has no tasks")
	}
	if len(p.Tasks) > MaxTasks {
		add(Error, "", "", "the plan has %d tasks; the limit is %d", len(p.Tasks), MaxTasks)
	}
	proposed := map[string]Queued{}
	for _, q := range env.Queued {
		if q.Plan != "" {
			proposed[q.ID] = q
		}
	}
	ids := map[string]int{}
	for i, t := range p.Tasks {
		id := strings.TrimSpace(t.ID)
		label := id
		if label == "" {
			label = fmt.Sprintf("#%d", i+1)
		}
		switch {
		case id == "":
			add(Error, label, "id", "missing id")
		case !api.ValidTaskID(api.TaskID(id)):
			add(Error, label, "id", "must match [A-Za-z0-9][A-Za-z0-9._-]{0,63}")
		case ids[id] > 0:
			add(Error, label, "id", "duplicate id")
		}
		ids[id]++
		if env.TaskPhase != nil && id != "" && !env.AllowExisting {
			if ph, ok := env.TaskPhase(id); ok {
				add(Error, label, "id", "a task with this id already exists (%s)", ph)
			}
		}
		if q, ok := proposed[id]; ok && id != "" {
			add(Warning, label, "id", "plan %s also proposes %q: whichever is submitted second will be refused", q.Plan, id)
		}
		intent := strings.TrimSpace(t.Intent)
		switch {
		case intent == "":
			add(Error, label, "intent", "missing intent")
		case len(intent) > 64<<10:
			add(Error, label, "intent", "longer than 64 KiB")
		case len(intent) < 20:
			add(Warning, label, "intent", "very short; the agent sees only this text")
		}
		if len(t.Title) > 72 {
			add(Warning, label, "title", "longer than 72 characters; the commit subject is cut")
		}
		if len(t.Scope) == 0 {
			add(Warning, label, "scope", "no scope: the task may change any file, and cannot safely run beside other tasks")
		}
		for _, g := range t.Scope {
			g = strings.TrimSpace(g)
			if g == "" {
				continue
			}
			if strings.HasPrefix(g, "/") || strings.Contains("/"+g+"/", "/../") {
				add(Error, label, "scope", "%q must be a relative path inside the repository", g)
				continue
			}
			if _, err := path.Match(strings.ReplaceAll(g, "**", "*"), ""); err != nil {
				add(Error, label, "scope", "%q is not a valid glob: %v", g, err)
				continue
			}
			if len(env.Files) > 0 && !matchesAny(g, env.Files) && !strings.ContainsAny(g, "*?[") {
				// A literal path that does not exist is a new file: fine.
				continue
			}
			if len(env.Files) > 0 && strings.ContainsAny(g, "*?[") && !matchesAny(g, env.Files) && !dirExists(g, env.Files) {
				add(Warning, label, "scope", "%q is under a directory that does not exist at the base (fine if the task creates it; check for a typo)", g)
			}
		}
		if t.Agent != "" && len(env.Agents) > 0 && !contains(env.Agents, t.Agent) {
			add(Error, label, "agent", "unknown agent %q (configured: %s)", t.Agent, strings.Join(env.Agents, ", "))
		}
		for _, g := range t.Gates {
			if len(env.Gates) > 0 && !contains(env.Gates, g) {
				add(Error, label, "gates", "unknown gate %q (at the base: %s)", g, strings.Join(env.Gates, ", "))
			}
		}
		if t.MaxAttempts < 0 || t.MaxRounds < 0 || t.MaxTokens < 0 {
			add(Error, label, "budget", "budgets must not be negative")
		}
	}
	// Dependencies.
	inPlan := map[string]bool{}
	for _, t := range p.Tasks {
		inPlan[strings.TrimSpace(t.ID)] = true
	}
	for _, t := range p.Tasks {
		id := strings.TrimSpace(t.ID)
		for _, d := range t.After {
			d = strings.TrimSpace(d)
			if d == "" {
				continue
			}
			if d == id {
				add(Error, id, "after", "depends on itself")
				continue
			}
			if !api.ValidDependency(api.TaskID(d)) {
				add(Error, id, "after", "%q is not a task id or repo:task", d)
				continue
			}
			repo, rid, cross := strings.Cut(d, ":")
			if cross && repo == env.Repo {
				d, cross = rid, false
			}
			if !cross {
				if inPlan[d] {
					continue
				}
				if env.TaskPhase != nil {
					ph, ok := env.TaskPhase(d)
					q, isProposed := proposed[d]
					switch {
					case !ok && isProposed && env.Submitting:
						add(Error, id, "after", "%q is only proposed, by plan %s: submit that plan first", d, q.Plan)
					case !ok && isProposed:
						add(Warning, id, "after", "%q is proposed by plan %s: submit that plan before this one", d, q.Plan)
					case !ok:
						add(Error, id, "after", "%q is neither in this plan nor an existing task", d)
					case ph == api.PhaseFailed || ph == api.PhaseCancelled:
						add(Warning, id, "after", "%q is %s: this task will escalate", d, ph)
					}
				}
				continue
			}
			if env.RepoTask == nil {
				add(Error, id, "after", "%q: cross-repository dependencies need `spine supervisor`", d)
				continue
			}
			ph, repoKnown, taskKnown := env.RepoTask(repo, rid)
			switch {
			case !repoKnown:
				add(Error, id, "after", "unknown repository %q", repo)
			case !taskKnown:
				add(Warning, id, "after", "%q does not exist yet; the task waits 10 minutes for it, then escalates", d)
			case ph == api.PhaseFailed || ph == api.PhaseCancelled:
				add(Warning, id, "after", "%q is %s: this task will escalate", d, ph)
			}
		}
	}
	if cyc := Cycle(p); len(cyc) > 0 {
		add(Error, "", "after", "dependency cycle: %s", strings.Join(cyc, " → "))
	}
	{
		// Tasks that may run at the same time should not claim the same paths.
		reach := Reach(p)
		for i, a := range p.Tasks {
			for _, b := range p.Tasks[i+1:] {
				if reach[a.ID][b.ID] || reach[b.ID][a.ID] {
					continue
				}
				if why, ok := clash(env.Modules, a.Scope, b.Scope); ok {
					add(Warning, b.ID, "scope", "may run beside %q%s", a.ID, why)
				}
			}
		}
		before := queuedReach(p, env.Queued)
		for _, t := range p.Tasks {
			for _, q := range env.Queued {
				if q.ID == t.ID || before[t.ID][q.ID] {
					continue
				}
				what := fmt.Sprintf("task %q (%s)", q.ID, q.State)
				if q.Plan != "" {
					what = fmt.Sprintf("%q, proposed by plan %s", q.ID, q.Plan)
				}
				if why, ok := clash(env.Modules, q.Scope, t.Scope); ok {
					add(Warning, t.ID, "scope", "may run beside %s%s", what, why)
				}
			}
		}
	}
	if env.Modules != nil {
		for _, t := range p.Tasks {
			for _, m := range missedTests(env.Modules, t.Scope) {
				add(Warning, strings.TrimSpace(t.ID), "scope", "changes module %s but not its tests (e.g. %s): add them to the scope so the agent can update them", m.Name, m.Tests[0])
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Level == Error && out[j].Level != Error })
	return out
}

// touched lists the modules a scope may change.
func touched(m *modmap.Map, scope []string) map[string]bool {
	out := map[string]bool{}
	for _, g := range scope {
		for _, mod := range m.Touched(strings.TrimSpace(g)) {
			out[mod.Name] = true
		}
	}
	return out
}

func sharedModule(a, b map[string]bool) string {
	var shared []string
	for m := range a {
		if b[m] && m != "." {
			shared = append(shared, m)
		}
	}
	sort.Strings(shared)
	if len(shared) == 0 {
		return ""
	}
	return shared[0]
}

func coChanged(m *modmap.Map, a, b map[string]bool) (modmap.Pair, bool) {
	var best modmap.Pair
	found := false
	for x := range a {
		for y := range b {
			if p, ok := m.Pair(x, y); ok && (!found || p.Commits > best.Commits) {
				best, found = p, true
			}
		}
	}
	return best, found
}

// missedTests are modules whose source the scope covers but none of whose
// own tests it does.
func missedTests(m *modmap.Map, scope []string) []*modmap.Module {
	if len(scope) == 0 {
		return nil
	}
	var out []*modmap.Module
	seen := map[string]bool{}
	for _, g := range scope {
		for _, mod := range m.Touched(strings.TrimSpace(g)) {
			if seen[mod.Name] || len(mod.Tests) == 0 {
				continue
			}
			seen[mod.Name] = true
			covered := false
			for _, t := range mod.Tests {
				for _, h := range scope {
					covered = covered || diff.MatchGlob(strings.TrimSpace(h), t)
				}
			}
			if !covered {
				out = append(out, mod)
			}
		}
	}
	return out
}

// HasErrors reports whether any issue is an error.
func HasErrors(is []Issue) bool {
	for _, i := range is {
		if i.Level == Error {
			return true
		}
	}
	return false
}

// Cycle returns one dependency cycle among the plan's own tasks, if any.
func Cycle(p api.Plan) []string {
	deps := map[string][]string{}
	for _, t := range p.Tasks {
		for _, d := range t.After {
			deps[t.ID] = append(deps[t.ID], strings.TrimSpace(d))
		}
	}
	const (
		white = iota
		grey
		black
	)
	color := map[string]int{}
	var stack []string
	var found []string
	var visit func(string) bool
	visit = func(n string) bool {
		color[n] = grey
		stack = append(stack, n)
		for _, d := range deps[n] {
			if _, ok := deps[d]; !ok && !inPlanIDs(p, d) {
				continue
			}
			switch color[d] {
			case grey:
				for i, s := range stack {
					if s == d {
						found = append(append([]string{}, stack[i:]...), d)
					}
				}
				return true
			case white:
				if visit(d) {
					return true
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[n] = black
		return false
	}
	for _, t := range p.Tasks {
		if color[t.ID] == white && visit(t.ID) {
			return found
		}
	}
	return nil
}

func inPlanIDs(p api.Plan, id string) bool {
	for _, t := range p.Tasks {
		if t.ID == id {
			return true
		}
	}
	return false
}

// Reach maps each task to the plan tasks it (transitively) depends on.
func Reach(p api.Plan) map[string]map[string]bool {
	deps := map[string][]string{}
	for _, t := range p.Tasks {
		deps[t.ID] = nil
		for _, d := range t.After {
			deps[t.ID] = append(deps[t.ID], strings.TrimSpace(d))
		}
	}
	out := map[string]map[string]bool{}
	var walk func(root, n string, depth int)
	walk = func(root, n string, depth int) {
		if depth > len(p.Tasks) {
			return
		}
		for _, d := range deps[n] {
			if _, ok := deps[d]; !ok || out[root][d] {
				continue
			}
			out[root][d] = true
			walk(root, d, depth+1)
		}
	}
	for _, t := range p.Tasks {
		out[t.ID] = map[string]bool{}
		walk(t.ID, t.ID, 0)
	}
	return out
}

// clash says why two tasks' scopes may conflict when they run side by side.
func clash(m *modmap.Map, a, b []string) (string, bool) {
	if g, h, ok := overlap(a, b); ok {
		return fmt.Sprintf(" and both may change %s (%s / %s): add an `after` link or narrow the scopes", g, g, h), true
	}
	if m == nil || len(a) == 0 || len(b) == 0 {
		return "", false
	}
	ma, mb := touched(m, a), touched(m, b)
	if mod := sharedModule(ma, mb); mod != "" {
		return fmt.Sprintf(" and both change module %s: expect merge conflicts; add an `after` link or merge the tasks", mod), true
	}
	if p, ok := coChanged(m, ma, mb); ok {
		return fmt.Sprintf(", but %s and %s changed together in %d commits: they likely need coordinated edits (add an `after` link)", p.A, p.B, p.Commits), true
	}
	return "", false
}

// queuedReach maps each plan task to the queued tasks it runs after,
// directly or through other tasks.
func queuedReach(p api.Plan, queued []Queued) map[string]map[string]bool {
	deps := map[string][]string{}
	for _, q := range queued {
		deps[q.ID] = q.After
	}
	for _, t := range p.Tasks {
		deps[strings.TrimSpace(t.ID)] = t.After
	}
	out := map[string]map[string]bool{}
	for _, t := range p.Tasks {
		seen := map[string]bool{}
		stack := append([]string{}, deps[strings.TrimSpace(t.ID)]...)
		for len(stack) > 0 {
			d := strings.TrimSpace(stack[len(stack)-1])
			stack = stack[:len(stack)-1]
			if seen[d] {
				continue
			}
			seen[d] = true
			stack = append(stack, deps[d]...)
		}
		out[strings.TrimSpace(t.ID)] = seen
	}
	return out
}

// overlap reports a pair of globs that can match a common path. An empty
// scope means "everything".
func overlap(a, b []string) (string, string, bool) {
	if len(a) == 0 && len(b) == 0 {
		return "everything", "everything", true
	}
	if len(a) == 0 {
		return "everything", b[0], true
	}
	if len(b) == 0 {
		return a[0], "everything", true
	}
	for _, g := range a {
		for _, h := range b {
			if globsOverlap(g, h) {
				return g, h, true
			}
		}
	}
	return "", "", false
}

// globsOverlap is conservative: it compares the literal prefixes before
// the first wildcard; one being a prefix of the other means "may overlap".
func globsOverlap(g, h string) bool {
	if diff.MatchGlob(g, h) || diff.MatchGlob(h, g) {
		return true
	}
	pg, ph := literalPrefix(g), literalPrefix(h)
	gw, hw := pg != g, ph != h
	if !gw || !hw {
		return false // a literal path overlaps only what matches it (checked above)
	}
	if pg == ph {
		return true
	}
	switch {
	case gw && strings.HasPrefix(ph, pg):
		return true
	case hw && strings.HasPrefix(pg, ph):
		return true
	}
	return false
}

func literalPrefix(g string) string {
	if i := strings.IndexAny(g, "*?["); i >= 0 {
		return g[:i]
	}
	return g
}

func matchesAny(g string, files []string) bool {
	for _, f := range files {
		if diff.MatchGlob(g, f) {
			return true
		}
	}
	return false
}

// dirExists reports whether the directory holding the glob's literal
// prefix exists at the base ("pkg/limiter*" → pkg/).
func dirExists(g string, files []string) bool {
	p := literalPrefix(g)
	if !strings.HasSuffix(p, "/") {
		p = path.Dir(p)
		if p == "." {
			return true
		}
	}
	p = strings.TrimSuffix(p, "/")
	if p == "" {
		return true
	}
	for _, f := range files {
		if strings.HasPrefix(f, p+"/") {
			return true
		}
	}
	return false
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
