package gate

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/patcarter883/spine-next/internal/api"
	"github.com/patcarter883/spine-next/internal/testrepo"
	"github.com/patcarter883/spine-next/internal/tools"
)

func d(fp string) tools.Diagnostic {
	return tools.Diagnostic{Severity: tools.Error, Code: fp, Fingerprint: fp}
}

func TestBaselineVerdict(t *testing.T) {
	base := &tools.Result{ExitCode: 1, Diagnostics: []tools.Diagnostic{d("old1"), d("old2")}}
	cases := []struct {
		name   string
		cur    tools.Result
		code   int
		reason string
		new    int
		fixed  int
	}{
		{"only pre-existing", tools.Result{ExitCode: 1, Diagnostics: []tools.Diagnostic{d("old1")}}, 0, "PreexistingFailuresOnly", 0, 1},
		{"new failure", tools.Result{ExitCode: 1, Diagnostics: []tools.Diagnostic{d("old1"), d("new")}}, 1, "NewFailures", 1, 1},
		{"unparsed", tools.Result{ExitCode: 2, Unparsed: true}, 1, "UnparsedFailure", 0, 2},
		{"truncated", tools.Result{ExitCode: 1, Truncated: true, Diagnostics: []tools.Diagnostic{d("old1")}}, 1, "TruncatedFailure", 0, 1},
		{"clean", tools.Result{}, 0, "", 0, 2},
		{"warnings never fail", tools.Result{Diagnostics: []tools.Diagnostic{{Severity: tools.Warning, Fingerprint: "w"}}}, 0, "", 0, 2},
	}
	for _, c := range cases {
		var gv api.GateVerdict
		baselineVerdict(&gv, base, c.cur)
		if gv.ExitCode != c.code || gv.Reason != c.reason || len(gv.New) != c.new || len(gv.Fixed) != c.fixed {
			t.Errorf("%s: %+v", c.name, gv)
		}
	}
	// No base result (snapshot clean, base not run): nothing to excuse.
	var gv api.GateVerdict
	baselineVerdict(&gv, nil, tools.Result{ExitCode: 1, Diagnostics: []tools.Diagnostic{d("x")}})
	if gv.ExitCode != 1 {
		t.Fatal("failure without baseline must fail")
	}
}

type countTool struct {
	name  string
	runs  map[string]int
	diags map[string][]tools.Diagnostic // by tree
	dirs  bool
	det   tools.Determinism
}

func (c *countTool) Spec() tools.Spec {
	return tools.Spec{Name: c.name, Version: "1", Determinism: c.det, NeedsDir: c.dirs, ReadOnly: true}
}
func (c *countTool) Run(ctx context.Context, inv tools.Invocation) (tools.Result, error) {
	c.runs[inv.Tree]++
	ds := c.diags[inv.Tree]
	r := tools.Result{Diagnostics: ds}
	if len(ds) > 0 {
		r.ExitCode = 1
	}
	return r, nil
}

func TestEvaluateOrderShortCircuitAndConfigFromBase(t *testing.T) {
	ctx := context.Background()
	repo := testrepo.New(t)
	repo.Write(map[string]string{ConfigPath: "gates:\n  a: {tool: t.a}\n  b: {tool: t.b, policy: baseline}\n  c: {tool: t.c}\norder: [a, b, c]\n", "f": "1"})
	base := repo.Commit("base")
	// The snapshot tries to delete gate c: it must not matter.
	repo.Write(map[string]string{ConfigPath: "gates:\n  a: {tool: t.a}\norder: [a]\n", "f": "2"})
	snap := repo.Commit("snap")
	baseTree, snapTree := repo.Tree(base), repo.Tree(snap)

	a := &countTool{name: "t.a", runs: map[string]int{}}
	b := &countTool{name: "t.b", runs: map[string]int{}, dirs: true, diags: map[string][]tools.Diagnostic{
		baseTree: {d("debt")}, snapTree: {d("debt")},
	}}
	c := &countTool{name: "t.c", runs: map[string]int{}, diags: map[string][]tools.Diagnostic{snapTree: {d("boom")}}}
	reg := tools.New(tools.Options{Cache: tools.NewMemCache()})
	reg.MustRegister(a, b, c)
	e := &Engine{Reg: reg, ScratchDir: t.TempDir()}

	v, err := e.Evaluate(ctx, Request{Repo: repo.Dir, Commit: snap, Base: base})
	if err != nil {
		t.Fatal(err)
	}
	if v.Passed || v.FailedGate != "c" || len(v.Gates) != 3 || v.Gates[1].Reason != "PreexistingFailuresOnly" || v.Gates[1].Excused != 1 {
		t.Fatalf("%+v", v)
	}
	if b.runs[baseTree] != 1 || b.runs[snapTree] != 1 {
		t.Fatalf("baseline runs %v", b.runs)
	}
	// Second evaluation: everything cached, no tool runs.
	v2, err := e.Evaluate(ctx, Request{Repo: repo.Dir, Commit: snap, Base: base})
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range v2.Gates {
		if !g.Cached {
			t.Fatalf("gate %s not cached", g.Gate)
		}
	}
	if a.runs[snapTree] != 1 || b.runs[snapTree] != 1 || c.runs[snapTree] != 1 {
		t.Fatalf("re-ran tools: a=%v b=%v c=%v", a.runs, b.runs, c.runs)
	}

	// A failing early gate short-circuits.
	a.diags = map[string][]tools.Diagnostic{snapTree: {d("x")}}
	cfg, _ := FromRefs([]api.GateRef{{Name: "a2", Tool: "t.a", Args: map[string]any{}}, {Name: "c", Tool: "t.c"}})
	reg2 := tools.New(tools.Options{})
	reg2.MustRegister(a, c)
	v3, err := (&Engine{Reg: reg2}).Evaluate(ctx, Request{Repo: repo.Dir, Commit: snap, Base: base, Config: &cfg})
	if err != nil {
		t.Fatal(err)
	}
	if v3.Passed || v3.FailedGate != "a2" || !v3.Gates[1].Skipped {
		t.Fatalf("%+v", v3)
	}
}

func TestEvaluateErrors(t *testing.T) {
	ctx := context.Background()
	repo := testrepo.New(t)
	repo.Write(map[string]string{"f": "1"})
	base := repo.Commit("base")
	e := &Engine{Reg: tools.New(tools.Options{})}
	if _, err := e.Evaluate(ctx, Request{Repo: repo.Dir, Commit: base, Base: base}); !errors.Is(err, ErrNoGates) {
		t.Fatalf("want ErrNoGates, got %v", err)
	}
	cfg, _ := FromRefs([]api.GateRef{{Tool: "nope"}})
	if _, err := e.Evaluate(ctx, Request{Repo: repo.Dir, Commit: base, Base: base, Config: &cfg}); !errors.Is(err, tools.ErrUnknownTool) {
		t.Fatalf("want ErrUnknownTool, got %v", err)
	}
	for _, bad := range []string{
		"gates:\n  a: {tool: x}\norder: [a, b]\n",
		"gates:\n  a: {tool: x}\n  b: {tool: y}\norder: [a]\n",
		"gates:\n  a: {tool: x, policy: maybe}\n",
		"gates:\n  a: {tool: x, timeout: soon}\n",
		"gates:\n  a: {tool: x, unknown_field: 1}\n",
	} {
		if _, err := ParseConfig([]byte(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	c, err := ParseConfig([]byte("gates:\n  u: {tool: cmd.run, args: {argv: [go, test], ok_exit: [0, 5]}, timeout: 2m, policy: baseline}\nbaseline:\n  link: [node_modules]\n"))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(c.Gates["u"].Args)
	if string(b) != `{"argv":["go","test"],"ok_exit":[0,5]}` || c.Gates["u"].Timeout.Minutes() != 2 || c.Baseline.Link[0] != "node_modules" {
		t.Fatalf("%s %+v", b, c)
	}
}

func TestEvaluateRerunsACachedFlakyFailure(t *testing.T) {
	ctx := context.Background()
	repo := testrepo.New(t)
	repo.Write(map[string]string{ConfigPath: "gates:\n  suite: {tool: t.suite, policy: baseline}\norder: [suite]\n", "f": "1"})
	base := repo.Commit("base")
	repo.Write(map[string]string{"f": "2"})
	snap := repo.Commit("snap")
	baseTree, snapTree := repo.Tree(base), repo.Tree(snap)

	suite := &countTool{name: "t.suite", runs: map[string]int{}, dirs: true, det: tools.Hermetic, diags: map[string][]tools.Diagnostic{
		baseTree: {d("debt")}, snapTree: {d("debt"), d("flake")},
	}}
	reg := tools.New(tools.Options{Cache: tools.NewMemCache()})
	reg.MustRegister(suite)
	e := &Engine{Reg: reg, ScratchDir: t.TempDir()}
	eval := func() Verdict {
		v, err := e.Evaluate(ctx, Request{Repo: repo.Dir, Commit: snap, Base: base})
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	if v := eval(); v.Passed {
		t.Fatalf("the flake should fail the first check: %+v", v)
	}
	suite.diags[snapTree] = []tools.Diagnostic{d("debt")}
	if v := eval(); !v.Passed || v.Gates[0].Cached {
		t.Fatalf("an unchanged tree whose cached result fails is run again: %+v", v)
	}
	if suite.runs[snapTree] != 2 || suite.runs[baseTree] != 1 {
		t.Fatalf("only the branch reruns: %v", suite.runs)
	}
	if v := eval(); !v.Passed || !v.Gates[0].Cached || suite.runs[snapTree] != 2 {
		t.Fatalf("a passing result is reused even with excused failures: %+v runs %v", v, suite.runs)
	}
}
