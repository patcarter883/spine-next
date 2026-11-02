//go:build cgo

// Package corpus is the M1 exit criterion (RFC §10): gate verdicts match
// ground truth on a corpus of Go, Python, TypeScript and PHP repositories,
// and identical trees hit the cache.
//
// Each fixture is a repository whose base commit carries pre-existing debt
// (a failing test, a lint error, a type error), so baseline-relative gates
// are exercised for real. Each task is a commit on top of base with a known
// verdict. The real toolchains run (go, ruff, pytest, tsc, eslint, php); a
// fixture whose tools are missing is skipped, unless SPINE_CORPUS_REQUIRE=1,
// in which case it fails (CI sets this).
package corpus

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/patcarter883/spine-next/internal/gate"
	"github.com/patcarter883/spine-next/internal/testrepo"
	"github.com/patcarter883/spine-next/internal/tools"
	"github.com/patcarter883/spine-next/internal/tools/build"
	"github.com/patcarter883/spine-next/internal/tools/builtin"
)

type task struct {
	name   string
	edit   map[string]string
	remove []string
	// Ground truth.
	pass       bool
	failedGate string
	reason     string // reason on the failed gate (optional)
	code       string // a diagnostic code expected on the failed gate (optional)
	excused    map[string]int
	fixed      map[string]int
}

type fixture struct {
	name  string
	needs []string
	scope []string
	base  map[string]string
	tasks []task
}

func TestCorpus(t *testing.T) {
	for _, f := range fixtures {
		t.Run(f.name, func(t *testing.T) { runFixture(t, f) })
	}
}

func runFixture(t *testing.T, f fixture) {
	for _, bin := range f.needs {
		if _, err := exec.LookPath(bin); err != nil {
			if os.Getenv("SPINE_CORPUS_REQUIRE") == "1" {
				t.Fatalf("%s not installed", bin)
			}
			t.Skipf("%s not installed", bin)
		}
	}
	ctx := context.Background()
	var ran atomic.Int64 // tool executions (cache misses)
	reg := builtin.New(tools.Options{
		Cache: tools.NewMemCache(),
		Observer: func(ev tools.Event) {
			if !ev.Cached && ev.Err == nil {
				ran.Add(1)
			}
		},
	}, &build.Runner{ScratchDir: t.TempDir()})
	eng := &gate.Engine{Reg: reg, ScratchDir: t.TempDir(), AlwaysBaseline: true}

	repo := testrepo.New(t)
	repo.Write(f.base)
	base := repo.Commit("base")

	for _, tk := range f.tasks {
		t.Run(tk.name, func(t *testing.T) {
			repo.Branch("task/"+tk.name, base)
			repo.Write(tk.edit)
			repo.Remove(tk.remove...)
			snap := repo.Commit(tk.name)
			v, err := eng.Evaluate(ctx, gate.Request{Repo: repo.Dir, Commit: snap, Base: base, Scope: f.scope})
			if err != nil {
				t.Fatalf("evaluate: %v", err)
			}
			check(t, tk, v)
		})
	}

	// Cache criterion: the "good" task's tree, committed again with a
	// different message and evaluated from a second linked worktree, must be
	// answered entirely from the cache with zero tool executions.
	good := f.tasks[0]
	if good.name != "good" {
		t.Fatal("first task must be named good")
	}
	wt := filepath.Join(t.TempDir(), "wt2")
	repo.Git("worktree", "add", "-q", "-b", "again/good", wt, base)
	gitIn(t, wt, "checkout", "-q", "task/good", "--", ".")
	for _, p := range good.remove {
		os.Remove(filepath.Join(wt, p))
	}
	gitIn(t, wt, "add", "-A")
	gitIn(t, wt, "commit", "-q", "--allow-empty", "-m", "same content, different commit")
	again := strings.TrimSpace(gitIn(t, wt, "rev-parse", "HEAD"))
	if repo.Tree(again) != repo.Tree("task/good") || again == repo.Git("rev-parse", "task/good") {
		t.Fatal("setup: expected a different commit with an identical tree")
	}
	before := ran.Load()
	v, err := eng.Evaluate(ctx, gate.Request{Repo: wt, Commit: again, Base: base, Scope: f.scope})
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range v.Gates {
		if !g.Cached {
			t.Errorf("cache: gate %s missed on an identical tree", g.Gate)
		}
	}
	if n := ran.Load() - before; n != 0 {
		t.Errorf("cache: %d tool executions on an identical tree", n)
	}
	if !v.Passed {
		t.Errorf("cache: cached verdict differs: %+v", v)
	}
}

func check(t *testing.T, tk task, v gate.Verdict) {
	t.Helper()
	dump := func() string {
		var b strings.Builder
		for _, g := range v.Gates {
			b.WriteString("  " + g.Gate + ": exit=" + strconv.Itoa(g.ExitCode) + " reason=" + g.Reason + " excused=" + strconv.Itoa(g.Excused) + " fixed=" + strconv.Itoa(len(g.Fixed)))
			if g.Skipped {
				b.WriteString(" skipped")
			}
			b.WriteString("\n")
			for _, d := range g.New {
				b.WriteString("    " + d.Code + " " + d.File + ": " + firstLine(d.Message) + "\n")
			}
		}
		return b.String()
	}
	if v.Passed != tk.pass || v.FailedGate != tk.failedGate {
		t.Fatalf("verdict: passed=%v failed=%q, want passed=%v failed=%q\n%s", v.Passed, v.FailedGate, tk.pass, tk.failedGate, dump())
	}
	byName := map[string]int{}
	for i, g := range v.Gates {
		byName[g.Gate] = i
	}
	if tk.failedGate != "" {
		g := v.Gates[byName[tk.failedGate]]
		if tk.reason != "" && g.Reason != tk.reason {
			t.Errorf("reason %q, want %q\n%s", g.Reason, tk.reason, dump())
		}
		if tk.code != "" {
			found := false
			for _, d := range g.New {
				if d.Code == tk.code || strings.HasSuffix(d.Code, tk.code) {
					found = true
				}
			}
			if !found {
				t.Errorf("no %s diagnostic on %s\n%s", tk.code, tk.failedGate, dump())
			}
		}
	}
	for gname, n := range tk.excused {
		if g := v.Gates[byName[gname]]; g.Excused != n {
			t.Errorf("%s excused %d, want %d\n%s", gname, g.Excused, n, dump())
		}
	}
	for gname, n := range tk.fixed {
		if g := v.Gates[byName[gname]]; len(g.Fixed) != n {
			t.Errorf("%s fixed %d, want %d\n%s", gname, len(g.Fixed), n, dump())
		}
	}
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(s, "\n")
	return l
}
