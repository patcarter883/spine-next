package e2e

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/patcarter883/spine-next/internal/api"
	"github.com/patcarter883/spine-next/internal/app"
	"github.com/patcarter883/spine-next/internal/inbox"
	"github.com/patcarter883/spine-next/internal/reconciler"
	"github.com/patcarter883/spine-next/internal/storage"
	"github.com/patcarter883/spine-next/internal/supervisor"
)

type supEnv struct {
	t    *testing.T
	sup  *supervisor.Supervisor
	hs   map[string]*harness
	stop func()
}

func startSupervisor(t *testing.T, hs map[string]*harness, maxAgents int, extra func(name string, o *app.Options)) *supEnv {
	t.Helper()
	t.Setenv("SPINE_HOME", t.TempDir())
	reg := supervisor.DefaultRegistry()
	for name, h := range hs {
		if _, err := reg.Add(context.Background(), h.repo.Dir, name); err != nil {
			t.Fatal(err)
		}
	}
	sup := &supervisor.Supervisor{Registry: reg, Config: supervisor.Config{MaxAgents: maxAgents, MaxGates: 2, Reload: 200 * time.Millisecond},
		Logf: t.Logf,
		AppOptions: func(r supervisor.Repo) app.Options {
			cfg := hs[r.Name].cfg
			o := app.Options{Config: &cfg, Workers: 4}
			if extra != nil {
				extra(r.Name, &o)
			}
			return o
		}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- sup.Run(ctx) }()
	return &supEnv{t: t, sup: sup, hs: hs, stop: func() {
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}}
}

func (e *supEnv) submit(repo string, id api.TaskID, s api.Spec) {
	cd := e.hs[repo].repo.Git("rev-parse", "--path-format=absolute", "--git-common-dir")
	if _, err := inbox.Write(inbox.Dir(cd), inbox.Command{Kind: inbox.Submit, Task: id, Spec: &s}); err != nil {
		e.t.Fatal(err)
	}
}

// waitTask polls a repository's database read-only (the supervisor holds
// the writer lock), as the CLI does.
func (e *supEnv) waitTask(repo string, id api.TaskID, phase api.Phase) api.Task {
	e.t.Helper()
	db, _, _ := app.DBPath(e.hs[repo].repo.Dir)
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		if st, err := storage.OpenReadOnly(context.Background(), db); err == nil {
			tk, err := st.GetTask(context.Background(), id)
			st.Close()
			if err == nil && tk.Status.Phase == phase {
				return tk
			}
			if err == nil && tk.Status.Phase.Terminal() && tk.Status.Phase != phase {
				e.t.Fatalf("%s/%s ended %s (%s %s)", repo, id, tk.Status.Phase, tk.Status.Reason, tk.Status.Message)
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	e.t.Fatalf("%s/%s: timed out waiting for %s; repos %+v", repo, id, phase, e.sup.Status())
	return api.Task{}
}

func writeStep(file, content string, sleepMS int) map[string]any {
	return map[string]any{"write": map[string]string{file: content}, "sleep_ms": sleepMS}
}

// Two repositories under one supervisor: a cross-repository dependency is
// honoured, and the supervisor-wide agent limit holds across repositories.
func TestSupervisorTwoReposLimitAndCrossRepoDep(t *testing.T) {
	h1 := newHarness(t, map[string]map[string]any{
		"lib": {"*": writeStep("pkg/lib.go", goFile("Lib"), 400)},
		"x1":  {"*": writeStep("pkg/x1.go", goFile("X1"), 400)},
	}, 4)
	h2 := newHarness(t, map[string]map[string]any{
		"app": {"*": writeStep("pkg/app.go", goFile("App"), 100)},
		"x2":  {"*": writeStep("pkg/x2.go", goFile("X2"), 400)},
	}, 4)
	e := startSupervisor(t, map[string]*harness{"one": h1, "two": h2}, 1, nil)
	defer e.stop()

	sapp := spec("use the new lib", "pkg/**")
	sapp.DependsOn = []api.TaskID{"one:lib"}
	e.submit("two", "app", sapp)
	e.submit("two", "x2", spec("x2", "pkg/**"))
	e.submit("one", "x1", spec("x1", "pkg/**"))
	time.Sleep(time.Second) // app must still be waiting: lib is not even submitted
	db2, _, _ := app.DBPath(h2.repo.Dir)
	st, _ := storage.OpenReadOnly(context.Background(), db2)
	if tk, err := st.GetTask(context.Background(), "app"); err != nil || tk.Status.Phase != api.PhasePending {
		t.Fatalf("app should wait for one:lib: %+v %v", tk.Status, err)
	}
	st.Close()
	e.submit("one", "lib", spec("add lib", "pkg/**"))

	lib := e.waitTask("one", "lib", api.PhaseIntegrated)
	appT := e.waitTask("two", "app", api.PhaseIntegrated)
	e.waitTask("one", "x1", api.PhaseIntegrated)
	e.waitTask("two", "x2", api.PhaseIntegrated)
	if !appT.Status.StartedAt.After(lib.UpdatedAt.Add(-time.Second)) || appT.Status.StartedAt.Before(lib.CreatedAt) {
		t.Fatalf("app started %s, lib integrated %s", appT.Status.StartedAt, lib.UpdatedAt)
	}
	if _, peak := e.sup.Limits().Usage(); peak["agent"] != 1 {
		t.Fatalf("agent peak %d with max_agents=1", peak["agent"])
	}
	for _, s := range e.sup.Status() {
		if s.State != supervisor.StateRunning || s.Restarts != 0 {
			t.Fatalf("repo %s: %+v", s.Name, s)
		}
	}
}

// panicOnce panics the first time it is asked to decide.
type panicOnce struct{ n atomic.Int32 }

func (p *panicOnce) Name() string { return "panic-once" }
func (p *panicOnce) Decide(ctx context.Context, o reconciler.Observation) (api.Decision, error) {
	if p.n.Add(1) == 1 {
		panic("injected supervisor bug")
	}
	return reconciler.RuleSupervisor{}.Decide(ctx, o)
}

// A panic inside one repository's controller restarts that repository
// only; the other keeps working, and the panicked task still completes.
func TestSupervisorPanicIsolationAndBrokenRepo(t *testing.T) {
	bad := writeStep("pkg/p.go", goFile("P")+"\nfunc Broken() int { return \"x\" }\n", 0)
	h1 := newHarness(t, map[string]map[string]any{
		"p": {"r0": bad, "r1": writeStep("pkg/p.go", goFile("P"), 0)},
	}, 2)
	h2 := newHarness(t, map[string]map[string]any{
		"q": {"*": writeStep("pkg/q.go", goFile("Q"), 1500)},
	}, 2)
	h3 := newHarness(t, map[string]map[string]any{"z": {"*": writeStep("pkg/z.go", goFile("Z"), 0)}}, 2)

	// Hold h3's writer lock, as a stray standalone controller would.
	db3, _, _ := app.DBPath(h3.repo.Dir)
	held, err := storage.Open(context.Background(), db3, storage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	sup := &panicOnce{}
	e := startSupervisor(t, map[string]*harness{"one": h1, "two": h2, "three": h3}, 4, func(name string, o *app.Options) {
		if name == "one" {
			o.Supervisor = sup
		}
	})
	defer e.stop()
	e.submit("two", "q", spec("q", "pkg/**"))
	e.submit("one", "p", spec("p", "pkg/**"))
	e.submit("three", "z", spec("z", "pkg/**"))

	e.waitTask("one", "p", api.PhaseIntegrated)
	e.waitTask("two", "q", api.PhaseIntegrated)
	st := map[string]supervisor.RepoStatus{}
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		for _, s := range e.sup.Status() {
			st[s.Name] = s
		}
		if st["three"].State == supervisor.StateBroken {
			break
		}
	}
	if st["one"].Restarts != 1 || st["two"].Restarts != 0 {
		t.Fatalf("restarts: one=%d two=%d", st["one"].Restarts, st["two"].Restarts)
	}
	if st["three"].State != supervisor.StateBroken || st["three"].Error == "" {
		t.Fatalf("locked repo should be broken: %+v", st["three"])
	}
	// Release the lock: the broken repository recovers on its next retry.
	held.Close()
	e.waitTask("three", "z", api.PhaseIntegrated)
}
