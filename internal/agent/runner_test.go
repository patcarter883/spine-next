package agent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/patcarter883/spine-next/internal/api"
	"github.com/patcarter883/spine-next/internal/storage"
	"github.com/patcarter883/spine-next/internal/testrepo"
	"github.com/patcarter883/spine-next/internal/worktree"
)

var fakeBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "fakeacp-")
	if err != nil {
		panic(err)
	}
	fakeBin = filepath.Join(dir, "fakeacp")
	if out, err := exec.Command("go", "build", "-o", fakeBin, "./fakeacp").CombinedOutput(); err != nil {
		panic(string(out))
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type env struct {
	m     *worktree.Manager
	lease *worktree.Lease
	plan  string
	log   string
	st    *storage.Store
}

func setup(t *testing.T, plan map[string]map[string]any) env {
	t.Helper()
	ctx := context.Background()
	repo := testrepo.New(t)
	repo.Write(map[string]string{"a.txt": "a\n"})
	base := repo.Commit("base")
	cd, _ := worktree.ResolveCommonDir(repo.Dir)
	st, err := storage.Open(ctx, worktree.DatabasePath(cd), storage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	m, err := worktree.New(ctx, worktree.Config{RepoDir: repo.Dir, Root: t.TempDir(), Store: st})
	if err != nil {
		t.Fatal(err)
	}
	l, err := m.Create(ctx, "t1", 1, base)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	b, _ := json.Marshal(plan)
	pf := filepath.Join(dir, "plan.json")
	os.WriteFile(pf, b, 0o644)
	return env{m: m, lease: l, plan: pf, log: filepath.Join(dir, "log"), st: st}
}

func (e env) run(t *testing.T, timeout time.Duration, stall time.Duration, maxTokens int64) (api.Outcome, []Event) {
	t.Helper()
	var evs []Event
	a := Adapter{Name: "fake", Argv: []string{fakeBin}, Mode: "acceptEdits", StallTimeout: stall,
		Env: map[string]string{"FAKE_ACP_PLAN": e.plan, "FAKE_ACP_LOG": e.log}}
	r := &Runner{Procs: e.m}
	out, err := r.Run(context.Background(), RunRequest{Lease: e.lease, Adapter: a, Timeout: timeout, MaxTokens: maxTokens,
		Prompt: Prompt{Task: "t1", Attempt: 1, Intent: "make b.txt", Scope: []string{"**"}},
		Events: func(ev Event) { evs = append(evs, ev) }})
	if err != nil {
		t.Fatal(err)
	}
	// Run never returns with the agent's group alive.
	procs, _ := e.st.ListProcs(context.Background(), storage.ProcRunning)
	for _, p := range procs {
		if syscall.Kill(-p.PGID, 0) == nil {
			t.Fatalf("process group %d still alive", p.PGID)
		}
	}
	if len(procs) != 0 {
		t.Fatalf("proc rows left running: %+v", procs)
	}
	return out, evs
}

func TestCompletedRunEditsWorktree(t *testing.T) {
	e := setup(t, map[string]map[string]any{"t1": {"*": map[string]any{"write": map[string]string{"b.txt": "hello\n"}, "tokens": 1200, "message": "added b"}}})
	out, evs := e.run(t, time.Minute, time.Minute, 0)
	if out.Reason != api.OutcomeCompleted || out.StopReason != "end_turn" || out.TokensUsed != 1200 || out.Message != "added b" {
		t.Fatalf("%+v", out)
	}
	if b, _ := os.ReadFile(filepath.Join(e.lease.Path, "b.txt")); string(b) != "hello\n" {
		t.Fatalf("file %q", b)
	}
	if len(evs) == 0 || evs[0].Kind != "tool_call" {
		t.Fatalf("events %+v", evs)
	}
	logb, _ := os.ReadFile(filepath.Join(e.log, "t1.log"))
	if !strings.Contains(string(logb), "spine-task: t1") || !strings.Contains(string(logb), "# Constraints") {
		t.Fatalf("prompt %s", logb)
	}
}

func TestCrashTimeoutStallBudget(t *testing.T) {
	cases := []struct {
		name    string
		step    map[string]any
		timeout time.Duration
		stall   time.Duration
		tokens  int64
		want    api.OutcomeReason
	}{
		{"crash", map[string]any{"crash": true}, time.Minute, time.Minute, 0, api.OutcomeCrashed},
		{"timeout", map[string]any{"hang": true}, 2 * time.Second, time.Minute, 0, api.OutcomeTimeout},
		{"stall", map[string]any{"hang": true, "silent": true}, time.Minute, 2 * time.Second, 0, api.OutcomeStalled},
		{"refusal", map[string]any{"stop": "refusal"}, time.Minute, time.Minute, 0, api.OutcomeRefused},
		{"max-tokens", map[string]any{"stop": "max_tokens"}, time.Minute, time.Minute, 0, api.OutcomeBudgetExceeded},
		{"token-budget", map[string]any{"tokens": 5000, "hang": true}, time.Minute, time.Minute, 1000, api.OutcomeBudgetExceeded},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := setup(t, map[string]map[string]any{"*": {"*": c.step}})
			start := time.Now()
			out, _ := e.run(t, c.timeout, c.stall, c.tokens)
			if out.Reason != c.want {
				t.Fatalf("reason %s, want %s: %+v", out.Reason, c.want, out)
			}
			if time.Since(start) > 10*time.Second {
				t.Fatalf("took %s", time.Since(start))
			}
		})
	}
}

func TestPermissionPolicy(t *testing.T) {
	e := setup(t, map[string]map[string]any{"*": {"*": map[string]any{"permission": "/etc/passwd", "write": map[string]string{"x.txt": "x"}}}})
	out, _ := e.run(t, time.Minute, time.Minute, 0)
	if out.Reason != api.OutcomeCompleted {
		t.Fatalf("%+v", out)
	}
	if b, _ := os.ReadFile(filepath.Join(e.log, "permission-passwd")); string(b) != "false" {
		t.Fatalf("outside path allowed: %q", b)
	}
	if _, err := os.Stat(filepath.Join(e.lease.Path, "x.txt")); err == nil {
		t.Fatal("rejected step still wrote")
	}
	e2 := setup(t, map[string]map[string]any{"*": {"*": map[string]any{"permission": "src/y.go", "write": map[string]string{"y.txt": "y"}}}})
	e2.run(t, time.Minute, time.Minute, 0)
	if b, _ := os.ReadFile(filepath.Join(e2.log, "permission-y.go")); string(b) != "true" {
		t.Fatalf("inside path rejected: %q", b)
	}
}
