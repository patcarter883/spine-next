package build

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/patcarter883/spine-next/internal/artifacts"
	"github.com/patcarter883/spine-next/internal/testrepo"
	"github.com/patcarter883/spine-next/internal/tools"
)

func goModule(t *testing.T, test string) string {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module ex.com/m\ngo 1.22\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "m_test.go"), []byte("package m\nimport \"testing\"\n"+test), 0o644)
	return dir
}

func TestGoTestLiveAndTimeout(t *testing.T) {
	ctx := context.Background()
	r := &Runner{Artifacts: &artifacts.Store{Dir: t.TempDir()}}
	reg := tools.New(tools.Options{Cache: tools.NewMemCache()})
	reg.MustRegister(r.Tools()...)

	dir := goModule(t, "func TestA(t *testing.T){ t.Fatal(\"nope\") }\nfunc TestB(t *testing.T){}\n")
	res, err := reg.Invoke(ctx, "go.test", tools.Invocation{Tree: "t1", Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode == 0 || len(res.Diagnostics) != 1 || res.Diagnostics[0].Code != "ex.com/m.TestA" || res.Metrics["tests_passed"] != 1 {
		t.Fatalf("%+v", res)
	}
	if len(res.Artifacts) != 1 || res.Artifacts[0].Size == 0 {
		t.Fatalf("artifacts %+v", res.Artifacts)
	}
	again, _ := reg.Invoke(ctx, "go.test", tools.Invocation{Tree: "t1", Dir: dir})
	if !again.Cached {
		t.Fatal("same tree should hit")
	}

	slow := goModule(t, "import \"time\"\nfunc TestSlow(t *testing.T){ time.Sleep(time.Minute) }\n")
	args, _ := json.Marshal(Args{Argv: []string{"go", "test", "-json", "./..."}, Parser: "gotest-json"})
	start := time.Now()
	res, err = reg.Invoke(ctx, "cmd.run", tools.Invocation{Tree: "t2", Dir: slow, Args: args, Timeout: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 124 || res.Diagnostics[0].Code != "Timeout" || time.Since(start) > 30*time.Second {
		t.Fatalf("timeout: %+v after %s", res, time.Since(start))
	}
	again, _ = reg.Invoke(ctx, "cmd.run", tools.Invocation{Tree: "t2", Dir: slow, Args: args, Timeout: 3 * time.Second})
	if again.Cached {
		t.Fatal("timeouts must not be cached")
	}
}

func TestCmdRunEnvAndMissingBinary(t *testing.T) {
	ctx := context.Background()
	t.Setenv("GIT_DIR", "/should/not/leak")
	r := &Runner{}
	reg := tools.New(tools.Options{})
	reg.MustRegister(r.Tools()...)
	dir := t.TempDir()
	args, _ := json.Marshal(Args{Argv: []string{"sh", "-c", `test -z "$GIT_DIR" && test "$HOME" != "` + os.Getenv("HOME") + `" && echo "E bad.txt:1 X1 broken" && exit 3`},
		Parser: "regex", Regex: &RegexParser{Pattern: `^E (?P<file>[^:]+):(?P<line>\d+) (?P<code>\S+) (?P<message>.*)$`}})
	res, err := reg.Invoke(ctx, "cmd.run", tools.Invocation{Tree: "x", Dir: dir, Args: args})
	if err != nil || res.ExitCode != 3 || len(res.Diagnostics) != 1 || res.Diagnostics[0].Code != "X1" {
		t.Fatalf("%+v %v", res, err)
	}
	args, _ = json.Marshal(Args{Argv: []string{"definitely-not-a-binary-xyz"}})
	if _, err := reg.Invoke(ctx, "cmd.run", tools.Invocation{Tree: "x", Dir: dir, Args: args}); err == nil {
		t.Fatal("missing binary must be an infrastructure error")
	}
	args, _ = json.Marshal(map[string]any{"argv": []string{"true"}, "env": map[string]string{"GIT_DIR": "/x"}})
	if _, err := reg.Invoke(ctx, "cmd.run", tools.Invocation{Tree: "x", Dir: dir, Args: args}); err == nil {
		t.Fatal("GIT_* env must be rejected by the schema")
	}
}

func TestCmdRunChangedFiles(t *testing.T) {
	ctx := context.Background()
	repo := testrepo.New(t)
	repo.Write(map[string]string{"keep.php": "<?php 1;", "gone.php": "<?php 1;", "notes.md": "a", "sub/x.php": "<?php 1;"})
	base := repo.Commit("base")
	repo.Write(map[string]string{"keep.php": "<?php 2;", "new.php": "<?php 1;", "notes.md": "b", "sub/x.php": "<?php 2;"})
	repo.Git("rm", "-q", "gone.php")
	change := repo.Commit("change")
	repo.Write(map[string]string{"notes.md": "c"})
	docsOnly := repo.Commit("docs only")

	r := &Runner{}
	reg := tools.New(tools.Options{Cache: tools.NewMemCache()})
	reg.MustRegister(r.Tools()...)
	out := filepath.Join(t.TempDir(), "args")
	run := func(commit, base string, argv ...string) tools.Result {
		args, _ := json.Marshal(Args{Argv: append([]string{"sh", "-c", `printf '%s\n' "$@" > ` + out, "sh"}, argv...)})
		os.Remove(out)
		res, err := reg.Invoke(ctx, "cmd.run", tools.Invocation{Repo: repo.Dir, Dir: repo.Dir, Tree: repo.Tree(commit), Commit: commit, Base: base, Args: args})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	read := func() string { b, _ := os.ReadFile(out); return string(b) }

	run(change, base, "--test", "{changed:**/*.php}")
	if got := read(); got != "--test\nkeep.php\nnew.php\nsub/x.php\n" {
		t.Fatalf("php files the change adds or modifies, never deleted ones: %q", got)
	}
	run(change, base, "{changed}")
	if got := read(); got != "keep.php\nnew.php\nnotes.md\nsub/x.php\n" {
		t.Fatalf("every added or modified file: %q", got)
	}
	if res := run(docsOnly, change, "{changed:**/*.php}"); res.ExitCode != 0 || res.Metrics["changed_files"] != 0 || read() != "" {
		t.Fatalf("no matching files: skipped and passing, %+v %q", res, read())
	}
	if res := run(change, docsOnly, "--test", "{changed:**/*.php}"); res.Cached {
		t.Fatal("the same tree measured against another base must not reuse the result")
	}
}
