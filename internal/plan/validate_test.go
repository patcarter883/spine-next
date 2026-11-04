package plan

import (
	"strings"
	"testing"

	"github.com/patcarter883/spine-next/internal/api"
	"github.com/patcarter883/spine-next/internal/modmap"
)

func has(is []Issue, l Level, sub string) bool {
	for _, i := range is {
		if i.Level == l && strings.Contains(i.String(), sub) {
			return true
		}
	}
	return false
}

func TestValidate(t *testing.T) {
	env := Env{
		Repo:   "web",
		Agents: []string{"claude", "pi"},
		Gates:  []string{"parse", "scope", "test"},
		Files:  []string{"api/handler.go", "api/handler_test.go", "web/app.ts", "docs/index.md"},
		TaskPhase: func(id string) (api.Phase, bool) {
			if id == "old" {
				return api.PhaseIntegrated, true
			}
			if id == "dead" {
				return api.PhaseFailed, true
			}
			return "", false
		},
		RepoTask: func(repo, id string) (api.Phase, bool, bool) {
			return api.PhasePending, repo == "billing", id == "rates"
		},
	}
	good := api.Plan{Tasks: []api.PlanTask{
		{ID: "api", Intent: "Add validation to the signup handler with tests", Scope: []string{"api/**"}, After: []string{"old", "billing:rates"}},
		{ID: "ui", Intent: "Show validation errors on the signup form", Scope: []string{"web/**"}, After: []string{"api"}, Agent: "pi"},
		{ID: "docs", Intent: "Document the signup validation rules for users", Scope: []string{"docs/**"}, Gates: []string{"parse"}},
	}}
	if is := Validate(good, env); HasErrors(is) {
		t.Fatalf("good plan: %v", is)
	}
	bad := api.Plan{Tasks: []api.PlanTask{
		{ID: "a", Intent: "x", Scope: []string{"api/**"}, After: []string{"b"}},
		{ID: "b", Intent: "Second task with a cycle back to a", Scope: []string{"api/**"}, After: []string{"a"}},
		{ID: "a", Intent: "", Agent: "gpt", Gates: []string{"lint"}},
		{ID: "bad id!", Intent: "something long enough here", Scope: []string{"/etc/**", "../x"}, After: []string{"nope", "mars:x", "dead"}},
		{ID: "old", Intent: "an id that already exists in the repo"},
		{ID: "p1", Intent: "parallel one touching the same files", Scope: []string{"web/**"}},
		{ID: "p2", Intent: "parallel two touching the same files", Scope: []string{"web/app.ts"}},
		{ID: "ghost", Intent: "scope that matches nothing at all", Scope: []string{"nothing/**/*.go"}},
	}}
	is := Validate(bad, env)
	for _, c := range []struct {
		l   Level
		sub string
	}{
		{Error, "dependency cycle"}, {Error, "duplicate id"}, {Error, "missing intent"}, {Error, "unknown agent"},
		{Error, "unknown gate"}, {Error, "must match"}, {Error, "relative path"}, {Error, "neither in this plan"},
		{Error, "unknown repository"}, {Warning, "is Failed"}, {Error, "already exists"}, {Warning, "may run beside"},
		{Warning, "does not exist at the base"}, {Warning, "very short"},
	} {
		if !has(is, c.l, c.sub) {
			t.Errorf("missing %s %q in:\n%v", c.l, c.sub, is)
		}
	}
	if is[0].Level != Error {
		t.Fatal("errors not first")
	}
}

func TestValidateWithModules(t *testing.T) {
	m := &modmap.Map{Version: 1, Modules: []modmap.Module{
		{Name: "api", Language: "go", Tests: []string{"api/handler_test.go"}, NTests: 1},
		{Name: "store", Language: "go", Tests: []string{"store/store_test.go"}, NTests: 1},
		{Name: "web", Language: "typescript"},
	}, CoChange: []modmap.Pair{{A: "api", B: "store", Commits: 9, Strength: 0.8}}}
	env := Env{Modules: m}
	p := api.Plan{Tasks: []api.PlanTask{
		{ID: "a", Intent: "change the handler signature everywhere", Scope: []string{"api/handler.go"}},
		{ID: "b", Intent: "change the store interface for the handler", Scope: []string{"store/**"}},
		{ID: "c", Intent: "another change to the same api package", Scope: []string{"api/routes.go", "api/*_test.go"}},
		{ID: "d", Intent: "frontend work that is independent of the api", Scope: []string{"web/**"}},
	}}
	is := Validate(p, env)
	for _, want := range []string{
		`a.scope: changes module api but not its tests`,
		`c.scope: may run beside "a" and both change module api`,
		`b.scope: may run beside "a", but api and store changed together in 9 commits`,
	} {
		if !has(is, Warning, want) {
			t.Errorf("missing %q in\n%v", want, is)
		}
	}
	for _, i := range is {
		if strings.HasPrefix(i.Task, "d") || strings.Contains(i.Message, `beside "d"`) || (i.Task == "b" && strings.Contains(i.Message, "tests")) || (i.Task == "c" && strings.Contains(i.Message, "tests")) {
			t.Errorf("unexpected: %s", i)
		}
	}
}
