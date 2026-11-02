package build

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/patcarter883/spine-next/internal/artifacts"
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
