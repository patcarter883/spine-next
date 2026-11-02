package diff_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/patcarter883/spine-next/internal/tools"
	"github.com/patcarter883/spine-next/internal/tools/diff"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

type fixture struct {
	repo, base, commit, tree string
}

func setup(t *testing.T) fixture {
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "src/a.go", "package src\n")
	write(t, dir, "docs/readme.md", "hi\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "base")
	base := git(t, dir, "rev-parse", "HEAD")
	git(t, dir, "checkout", "-qb", "task")
	write(t, dir, "src/a.go", "package src\n\nfunc A() {}\n")
	write(t, dir, "src/b.go", "package src\n")
	write(t, dir, "docs/readme.md", "hello\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "work")
	c := git(t, dir, "rev-parse", "HEAD")
	return fixture{repo: dir, base: base, commit: c, tree: git(t, dir, "rev-parse", "HEAD^{tree}")}
}

func registry() *tools.Registry {
	r := tools.New(tools.Options{Cache: tools.NewMemCache()})
	r.MustRegister(diff.Tools()...)
	return r
}

func (f fixture) inv(args string) tools.Invocation {
	return tools.Invocation{Repo: f.repo, Tree: f.tree, Commit: f.commit, Base: f.base, Args: json.RawMessage(args)}
}

func TestStatsAndNonEmpty(t *testing.T) {
	f := setup(t)
	reg := registry()
	ctx := context.Background()
	r, err := reg.Invoke(ctx, "diff.stats", f.inv(""))
	if err != nil {
		t.Fatal(err)
	}
	if r.Metrics["files_changed"] != 3 || r.Metrics["insertions"] != 4 || r.Metrics["deletions"] != 1 {
		t.Fatalf("stats: %v", r.Metrics)
	}
	if r, err := reg.Invoke(ctx, "diff.nonempty", f.inv("")); err != nil || r.ExitCode != 0 {
		t.Fatalf("nonempty on real change: %+v %v", r, err)
	}
	// The base itself as a "snapshot": no commits, no change.
	baseTree := git(t, f.repo, "rev-parse", f.base+"^{tree}")
	empty := tools.Invocation{Repo: f.repo, Tree: baseTree, Commit: f.base, Base: f.base}
	r, err = reg.Invoke(ctx, "diff.nonempty", empty)
	if err != nil || r.ExitCode != 1 || r.Diagnostics[0].Code != "NoChange" {
		t.Fatalf("nonempty on base: %+v %v", r, err)
	}
	// A commit that reverts to the base tree: commits exist, change does not.
	git(t, f.repo, "checkout", "-q", f.base, "--", ".")
	git(t, f.repo, "rm", "-q", "--cached", "src/b.go")
	os.Remove(filepath.Join(f.repo, "src/b.go"))
	git(t, f.repo, "commit", "-qam", "revert")
	rev := git(t, f.repo, "rev-parse", "HEAD")
	r, err = reg.Invoke(ctx, "diff.nonempty", tools.Invocation{Repo: f.repo, Tree: git(t, f.repo, "rev-parse", "HEAD^{tree}"), Commit: rev, Base: f.base})
	if err != nil || r.ExitCode != 1 || r.Diagnostics[0].Code != "NoChange" {
		t.Fatalf("nonempty on revert: %+v %v", r, err)
	}
}

func TestScope(t *testing.T) {
	f := setup(t)
	reg := registry()
	ctx := context.Background()
	r, err := reg.Invoke(ctx, "diff.scope", f.inv(`{"scope":["src/**"]}`))
	if err != nil || r.ExitCode != 1 || len(r.Diagnostics) != 1 || r.Diagnostics[0].File != "docs/readme.md" {
		t.Fatalf("scope: %+v %v", r, err)
	}
	r, err = reg.Invoke(ctx, "diff.scope", f.inv(`{"scope":["src/", "docs/*.md"]}`))
	if err != nil || r.ExitCode != 0 {
		t.Fatalf("scope wide: %+v %v", r, err)
	}
	r, err = reg.Invoke(ctx, "diff.scope", f.inv(`{"scope":["**"],"deny":["src/b.go"]}`))
	if err != nil || r.ExitCode != 1 || r.Diagnostics[0].Code != "DeniedPath" {
		t.Fatalf("deny: %+v %v", r, err)
	}
}

func TestApplyCheckAndPreflight(t *testing.T) {
	f := setup(t)
	reg := registry()
	ctx := context.Background()
	good := "--- a/src/b.go\n+++ b/src/b.go\n@@ -1 +1,2 @@\n package src\n+// ok\n"
	argsGood, _ := json.Marshal(map[string]string{"patch": good})
	if r, err := reg.Invoke(ctx, "diff.applycheck", f.inv(string(argsGood))); err != nil || r.ExitCode != 0 {
		t.Fatalf("good patch: %+v %v", r, err)
	}
	bad := "--- a/src/b.go\n+++ b/src/b.go\n@@ -1 +1,2 @@\n package nope\n+// x\n"
	argsBad, _ := json.Marshal(map[string]string{"patch": bad})
	r, err := reg.Invoke(ctx, "diff.applycheck", f.inv(string(argsBad)))
	if err != nil || r.ExitCode != 1 || r.Diagnostics[0].File != "src/b.go" {
		t.Fatalf("bad patch: %+v %v", r, err)
	}

	if r, err := reg.Invoke(ctx, "git.preflight", f.inv("")); err != nil || r.ExitCode != 0 {
		t.Fatalf("clean preflight: %+v %v", r, err)
	}
	// Move main so it conflicts with the task on src/a.go.
	git(t, f.repo, "checkout", "-q", "main")
	write(t, f.repo, "src/a.go", "package src\n\nfunc B() {}\n")
	git(t, f.repo, "commit", "-qam", "main moves")
	newBase := git(t, f.repo, "rev-parse", "HEAD")
	inv := f.inv("")
	inv.Base = newBase
	r, err = reg.Invoke(ctx, "git.preflight", inv)
	if err != nil || r.ExitCode != 1 || r.Diagnostics[0].File != "src/a.go" || r.Diagnostics[0].Code != "Conflict" {
		t.Fatalf("conflict preflight: %+v %v", r, err)
	}
}

func TestMatchGlob(t *testing.T) {
	cases := []struct {
		pat, p string
		want   bool
	}{
		{"src/**", "src/a/b.go", true},
		{"src/**", "src", true},
		{"src/**/*.go", "src/a.go", true},
		{"src/**/*.go", "src/x/y/a.go", true},
		{"src/**/*.go", "src/x/a.ts", false},
		{"*.md", "docs/a.md", false},
		{"**/*.md", "docs/a.md", true},
		{"docs/", "docs/a/b", true},
		{"a/b", "a/b/c", false},
	}
	for _, c := range cases {
		if got := diff.MatchGlob(c.pat, c.p); got != c.want {
			t.Errorf("MatchGlob(%q,%q)=%v", c.pat, c.p, got)
		}
	}
}
