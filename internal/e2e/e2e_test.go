// Package e2e drives the whole controller (storage, worktrees, ACP agent
// runs, gates, supervisor, CAS integration) against a real repository with
// the scripted fakeacp agent. TestParallelEight is the M2 exit criterion:
// 8 parallel tasks on a fixture repo, all integrated through CAS, with no
// index races and no LLM supervisor.
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/patcarter883/spine-next/internal/agent"
	"github.com/patcarter883/spine-next/internal/api"
	"github.com/patcarter883/spine-next/internal/app"
	"github.com/patcarter883/spine-next/internal/config"
	"github.com/patcarter883/spine-next/internal/storage"
	"github.com/patcarter883/spine-next/internal/testrepo"
)

var fakeBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "e2e-fakeacp-")
	if err != nil {
		panic(err)
	}
	fakeBin = filepath.Join(dir, "fakeacp")
	if out, err := exec.Command("go", "build", "-o", fakeBin, "../agent/fakeacp").CombinedOutput(); err != nil {
		panic(string(out))
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

const gates = `gates:
  scope:    {tool: diff.scope}
  nonempty: {tool: diff.nonempty}
  build:    {tool: go.build}
  unit:     {tool: go.test, policy: baseline}
order: [scope, nonempty, build, unit]
`

type harness struct {
	t    *testing.T
	repo *testrepo.Repo
	plan string
	log  string
	cfg  config.Config
}

func newHarness(t *testing.T, plan map[string]map[string]any, parallel int) *harness {
	t.Helper()
	r := testrepo.New(t)
	r.Write(map[string]string{
		".spine/gates.yaml": gates,
		"go.mod":            "module ex.com/app\n\ngo 1.22\n",
		"pkg/base.go":       "package pkg\n\n// Base is here.\nfunc Base() int { return 1 }\n",
		"shared.txt":        "base\n",
	})
	r.Commit("base")
	dir := t.TempDir()
	b, _ := json.Marshal(plan)
	pf := filepath.Join(dir, "plan.json")
	os.WriteFile(pf, b, 0o644)
	cfg := config.Default()
	cfg.Agent = "fake"
	cfg.Adapters["fake"] = agent.Adapter{Name: "fake", Argv: []string{fakeBin}, Mode: "acceptEdits", Permissions: "worktree",
		StallTimeout: time.Minute, Env: map[string]string{"FAKE_ACP_PLAN": pf, "FAKE_ACP_LOG": filepath.Join(dir, "log")}}
	cfg.Parallel = parallel
	cfg.WorktreeRoot = t.TempDir()
	cfg.Resync = time.Second
	cfg.Budget.PerRunTimeout = time.Minute
	return &harness{t: t, repo: r, plan: pf, log: filepath.Join(dir, "log"), cfg: cfg}
}

func (h *harness) open(ctx context.Context) *app.App {
	h.t.Helper()
	a, err := app.Open(ctx, h.repo.Dir, app.Options{Config: &h.cfg, Workers: 8, Logf: h.t.Logf})
	if err != nil {
		h.t.Fatal(err)
	}
	return a
}

func spec(intent string, scope ...string) api.Spec {
	return api.Spec{Intent: intent, BaseRef: "refs/heads/main", Scope: scope}
}

// waitAll waits until every task is terminal.
func waitAll(t *testing.T, st *storage.Store, ids []api.TaskID, d time.Duration) map[api.TaskID]api.Task {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		out := map[api.TaskID]api.Task{}
		done := true
		for _, id := range ids {
			tk, err := st.GetTask(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			out[id] = tk
			if !tk.Status.Phase.Terminal() {
				done = false
			}
		}
		if done {
			return out
		}
		if time.Now().After(deadline) {
			for id, tk := range out {
				t.Logf("%s: %s %s %s (a%d r%d)", id, tk.Status.Phase, tk.Status.Reason, tk.Status.Message, tk.Status.Attempt, tk.Status.PatchRound)
				dumpEvents(t, st, id)
			}
			t.Fatal("timed out waiting for tasks")
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func dumpEvents(t *testing.T, st *storage.Store, id api.TaskID) {
	evs, _ := st.ListEvents(context.Background(), id, 0, 0)
	for _, e := range evs {
		d := string(e.Data)
		if len(d) > 300 {
			d = d[:300] + "…"
		}
		t.Logf("  %s %s %s", id, e.Kind, d)
	}
}

func goFile(name string) string {
	return fmt.Sprintf("package pkg\n\n// %s is a feature.\nfunc %s() int { return 2 }\n", name, name)
}

func goTest(name string, want int) string {
	return fmt.Sprintf("package pkg\n\nimport \"testing\"\n\nfunc Test%s(t *testing.T) {\n\tif %s() != %d {\n\t\tt.Fatalf(\"%s() = %%d\", %s())\n\t}\n}\n", name, name, want, name, name)
}

func TestParallelEight(t *testing.T) {
	plan := map[string]map[string]any{}
	var ids []api.TaskID
	for i := 1; i <= 8; i++ {
		id := fmt.Sprintf("t%d", i)
		name := fmt.Sprintf("F%d", i)
		ids = append(ids, api.TaskID(id))
		plan[id] = map[string]any{"*": map[string]any{"write": map[string]string{
			"pkg/" + strings.ToLower(name) + ".go":      goFile(name),
			"pkg/" + strings.ToLower(name) + "_test.go": goTest(name, 2),
		}, "tokens": 100}}
	}
	// t3: the first round's test is wrong; the second round (fed the
	// failing test) fixes it.
	plan["t3"] = map[string]any{
		"r0": map[string]any{"write": map[string]string{"pkg/f3.go": goFile("F3"), "pkg/f3_test.go": goTest("F3", 3)}},
		"r1": map[string]any{"write": map[string]string{"pkg/f3_test.go": goTest("F3", 2)}},
	}
	// t5: the agent crashes in round 0; round 1 does the work.
	plan["t5"] = map[string]any{
		"r0": map[string]any{"crash": true},
		"r1": map[string]any{"write": map[string]string{"pkg/f5.go": goFile("F5"), "pkg/f5_test.go": goTest("F5", 2)}},
	}
	// t6: the agent commits its own work (Spine must still integrate it).
	plan["t6"]["*"].(map[string]any)["git_commit"] = true
	// t7 and t8 rewrite the same line: whichever lands second conflicts,
	// gets a fresh attempt on the new base, and appends instead.
	for _, id := range []string{"t7", "t8"} {
		plan[id] = map[string]any{
			"a1r0": map[string]any{"write": map[string]string{"shared.txt": id + "\n"}},
			"a2r0": map[string]any{"append": map[string]string{"shared.txt": id + "\n"}},
		}
	}
	h := newHarness(t, plan, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := h.open(ctx)
	defer a.Close()
	for _, id := range ids {
		scope := []string{"pkg/**"}
		if id == "t7" || id == "t8" {
			scope = []string{"shared.txt"}
		}
		if _, err := a.Store.CreateTask(ctx, id, spec("implement "+string(id), scope...)); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan error, 1)
	go func() { done <- a.Controller.Run(ctx) }()
	got := waitAll(t, a.Store, ids, 4*time.Minute)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		tk := got[id]
		if tk.Status.Phase != api.PhaseIntegrated {
			dumpEvents(t, a.Store, id)
			t.Errorf("%s: %s %s %s", id, tk.Status.Phase, tk.Status.Reason, tk.Status.Message)
		}
	}
	if t.Failed() {
		return
	}
	if r := got["t3"].Status; r.PatchRound != 1 || r.Attempt != 1 {
		t.Errorf("t3: want one patch round, got a%d r%d", r.Attempt, r.PatchRound)
	}
	if b, _ := os.ReadFile(filepath.Join(h.log, "t3.log")); !strings.Contains(string(b), "TestF3") || !strings.Contains(string(b), "spine-round: 1") {
		t.Errorf("t3 round 1 prompt did not carry the failing test:\n%s", b)
	}
	if r := got["t5"].Status; r.PatchRound != 1 {
		t.Errorf("t5: crash should lead to a patch round, got r%d", r.PatchRound)
	}
	attempts := got["t7"].Status.Attempt + got["t8"].Status.Attempt
	if attempts != 3 {
		t.Errorf("t7/t8: want exactly one fresh attempt after the conflict, attempts %d+%d", got["t7"].Status.Attempt, got["t8"].Status.Attempt)
	}

	// Repository invariants.
	r := h.repo
	if s := r.Git("status", "--porcelain"); s != "" {
		t.Errorf("primary checkout not clean (index race?):\n%s", s)
	}
	if r.Git("rev-parse", "HEAD") != r.Git("rev-parse", "refs/heads/main") || r.Git("symbolic-ref", "HEAD") != "refs/heads/main" {
		t.Error("primary checkout not at main")
	}
	for i := 1; i <= 8; i++ {
		if i == 7 || i == 8 {
			continue
		}
		if _, err := os.Stat(filepath.Join(r.Dir, fmt.Sprintf("pkg/f%d.go", i))); err != nil {
			t.Errorf("f%d.go not on main", i)
		}
	}
	lines := strings.Fields(r.Git("show", "main:shared.txt"))
	sort.Strings(lines)
	if strings.Join(lines, ",") != "t7,t8" {
		t.Errorf("shared.txt = %q", r.Git("show", "main:shared.txt"))
	}
	if n := strings.Count(r.Git("log", "--format=%s", "main"), "\n") + 1; n != 9 {
		t.Errorf("main has %d commits, want 9 (base + 8 squashed tasks)", n)
	}
	if out := r.Git("fsck", "--no-dangling"); strings.Contains(out, "error") {
		t.Errorf("fsck: %s", out)
	}
	if wl := r.Git("worktree", "list", "--porcelain"); strings.Count(wl, "worktree ") != 1 {
		t.Errorf("leftover worktrees:\n%s", wl)
	}
	cmd := exec.Command("go", "test", "./...")
	cmd.Dir = r.Dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("integrated main does not pass its own tests: %v\n%s", err, out)
	}
	procs, _ := a.Store.ListProcs(context.Background(), storage.ProcRunning)
	for _, p := range procs {
		if syscall.Kill(-p.PGID, 0) == nil {
			t.Errorf("process group %d alive after shutdown", p.PGID)
		}
	}
}
