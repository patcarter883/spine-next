package e2e

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/patcarter883/spine-next/internal/api"
	"github.com/patcarter883/spine-next/internal/gate"
	"github.com/patcarter883/spine-next/internal/inbox"
	"github.com/patcarter883/spine-next/internal/modmap"
	"github.com/patcarter883/spine-next/internal/repoctx"
)

// The repository-context track end to end: a survey task (the map is
// derived by Spine, the agent labels it and writes context.md; the
// spine.context gate rejects an agent that edits derived fields; the result
// waits for approval), then context.md reaches agents with context on and
// the module map reaches planners; the dashboard's gates check, context
// status and scope suggestions see it.
func TestRepositoryContext(t *testing.T) {
	h := newHarness(t, nil, 2)
	h.repo.Write(map[string]string{"pkg/base_test.go": "package pkg\n\nimport \"testing\"\n\nfunc TestBase(t *testing.T) {}\n"})
	h.repo.Commit("tests")
	ctx := context.Background()
	base := h.repo.Git("rev-parse", "HEAD")

	// What a well-behaved survey agent writes: the derived map plus labels.
	m, err := modmap.Derive(ctx, h.repo.Dir, base, nil, modmap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	good := *m
	good.Modules = append([]modmap.Module{}, m.Modules...)
	for i := range good.Modules {
		if good.Modules[i].Name == "pkg" {
			good.Modules[i].Label = "core arithmetic helpers"
			good.Modules[i].Caveats = []string{"Base() is part of the public API"}
		}
	}
	goodYAML, _ := good.Marshal()
	bad := good
	bad.Modules = append([]modmap.Module{}, good.Modules...)
	bad.Modules[0].Files += 7 // a derived field
	badYAML, _ := bad.Marshal()
	notes := "## Build and test\n- `go build ./...`\n- `go test ./pkg/...`\n\n## No-go areas\n- never edit shared.txt\n"
	writePlan(t, h, map[string]map[string]any{
		"survey-1": {
			"r0": map[string]any{"write": map[string]string{".spine/modules.yaml": string(badYAML), ".spine/context.md": notes}},
			"r1": map[string]any{"write": map[string]string{".spine/modules.yaml": string(goodYAML), ".spine/context.md": notes}},
		},
		"w":  {"*": map[string]any{"write": map[string]string{"pkg/w.go": goFile("W")}}},
		"pl": {"*": map[string]any{"write": map[string]string{"spine-plan.json": `{"tasks":[{"id":"x1","intent":"Add feature X to pkg with tests","scope":["pkg/*"]}]}`}}},
	})
	fake := h.cfg.Adapters["fake"]
	fake.Context = true // as for Pi
	h.cfg.Adapters["fake"] = fake
	a := h.open(ctx)
	defer a.Close()
	stop := runController(t, a)
	defer stop()
	d := newDash(t, a)

	// Before the survey: no context; the gates check passes on main.
	var ci struct {
		Modules *modmap.Map `json:"modules"`
		Stale   int         `json:"stale_commits"`
	}
	d.do("GET", "/api/repos/app/context", nil, &ci)
	if ci.Modules != nil {
		t.Fatal("context before the survey")
	}
	var gc gate.CheckReport
	if c := d.do("POST", "/api/repos/app/gates/check", map[string]string{}, &gc); c != 200 || !gc.OK || len(gc.Gates) != 4 || gc.Gates[3].Level != gate.CheckOK {
		t.Fatalf("gates check %d %+v", c, gc)
	}

	sv, err := h.cfg.SurveySpec("", "", "test")
	if err != nil {
		t.Fatal(err)
	}
	inbox.Write(inbox.Dir(a.CommonDir), inbox.Command{Kind: inbox.Submit, Task: "survey-1", Spec: &sv})
	tk := waitPhase(t, a, "survey-1", api.PhaseEscalated, "AwaitingApproval")
	if tk.Status.PatchRound != 1 {
		t.Fatalf("the tampered map should have cost a round: r%d", tk.Status.PatchRound)
	}
	prompts, _ := os.ReadFile(filepath.Join(h.log, "survey-1.log"))
	if !strings.Contains(string(prompts), "spine-kind: survey") || !strings.Contains(string(prompts), "derived fields differ") {
		t.Fatalf("survey prompts:\n%s", prompts)
	}
	command(t, a, inbox.Approve, "survey-1")
	waitPhase(t, a, "survey-1", api.PhaseIntegrated, "")
	if got := h.repo.Git("show", "main:.spine/context.md"); !strings.Contains(got, "go test ./pkg/...") {
		t.Fatalf("context.md on main: %q", got)
	}
	rc, err := repoctx.Load(ctx, h.repo.Dir, "main", 0)
	if err != nil || rc.Modules == nil || rc.Notes == "" {
		t.Fatalf("context at main: %+v %v", rc, err)
	}

	// A worker with context on gets context.md; its run records that.
	w := spec("add W", "pkg/**")
	inbox.Write(inbox.Dir(a.CommonDir), inbox.Command{Kind: inbox.Submit, Task: "w", Spec: &w})
	waitPhase(t, a, "w", api.PhaseIntegrated, "")
	prompts, _ = os.ReadFile(filepath.Join(h.log, "w.log"))
	if !strings.Contains(string(prompts), "# Repository notes") || !strings.Contains(string(prompts), "never edit shared.txt") {
		t.Fatalf("worker prompt lacks context.md:\n%s", prompts)
	}
	evs, _ := a.Store.ListEvents(ctx, "w", 0, 0)
	for _, e := range evs {
		if e.Kind == "agent_start" && !strings.Contains(string(e.Data), `"notes":true`) {
			t.Fatalf("agent_start without context info: %s", e.Data)
		}
	}

	// A planner gets the module map, with the survey's labels.
	pl := spec("Add feature X")
	pl.Kind = api.KindPlan
	inbox.Write(inbox.Dir(a.CommonDir), inbox.Command{Kind: inbox.Submit, Task: "pl", Spec: &pl})
	tk = waitPhase(t, a, "pl", api.PhaseEscalated, "")
	prompts, _ = os.ReadFile(filepath.Join(h.log, "pl.log"))
	if !strings.Contains(string(prompts), "# Module map") || !strings.Contains(string(prompts), "core arithmetic helpers") {
		t.Fatalf("planner prompt lacks the module map:\n%s", prompts)
	}

	// The dashboard: context status, scope suggestions, module-aware validation.
	d.do("GET", "/api/repos/app/context", nil, &ci)
	if ci.Modules == nil || ci.Stale < 0 {
		t.Fatalf("context status %+v", ci)
	}
	var sg struct {
		Suggestions []struct {
			Module string
			Scope  []string
		}
	}
	d.do("GET", "/api/repos/app/suggest?q=change+the+core+arithmetic+helpers+in+pkg", nil, &sg)
	if len(sg.Suggestions) == 0 || sg.Suggestions[0].Module != "pkg" {
		t.Fatalf("suggestions %+v", sg)
	}
	var vr struct{ Issues []struct{ Message string } }
	plan := api.Plan{Tasks: []api.PlanTask{{ID: "only-code", Intent: "Change Base without touching its tests", Scope: []string{"pkg/base.go"}}}}
	d.do("POST", "/api/repos/app/plans/validate", map[string]any{"plan": plan}, &vr)
	b, _ := json.Marshal(vr)
	if !strings.Contains(string(b), "not its tests") {
		t.Fatalf("validate: %s", b)
	}
}
