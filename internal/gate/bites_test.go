package gate

import (
	"context"
	"strings"
	"testing"

	"github.com/patcarter883/spine-next/internal/api"
	"github.com/patcarter883/spine-next/internal/testrepo"
	"github.com/patcarter883/spine-next/internal/tools"
	"github.com/patcarter883/spine-next/internal/tools/build"
)

func TestBitesNeedsTestsThatFailWithoutTheChange(t *testing.T) {
	ctx := context.Background()
	repo := testrepo.New(t)
	test := func(name, want string) string {
		return "package pkg\n\nimport \"testing\"\n\nfunc Test" + name + "(t *testing.T) {\n\tif " + want + " {\n\t\tt.Fatal(\"wrong\")\n\t}\n}\n"
	}
	repo.Write(map[string]string{
		"go.mod":          "module ex.com/b\n\ngo 1.22\n",
		"pkg/f.go":        "package pkg\n\nfunc F() int { return 1 }\n",
		"pkg/f_test.go":   test("F", "F() != 1"),
		"docs/readme.txt": "x\n",
	})
	base := repo.Commit("base")
	branch := func(name string, files map[string]string) string {
		repo.Branch(name, base)
		repo.Write(files)
		return repo.Commit(name)
	}
	snaps := map[string]string{
		"biting":    branch("biting", map[string]string{"pkg/f.go": "package pkg\n\nfunc F() int { return 2 }\n", "pkg/f_test.go": test("F", "F() != 2")}),
		"toothless": branch("toothless", map[string]string{"pkg/f.go": "package pkg\n\nfunc F() int { return 2 }\n", "pkg/f_test.go": test("F", "F() < 0")}),
		"new code":  branch("new-code", map[string]string{"pkg/h.go": "package pkg\n\nfunc H() int { return 3 }\n", "pkg/h_test.go": test("H", "H() != 3")}),
		"tests":     branch("tests", map[string]string{"pkg/g_test.go": test("G", "F() != 1")}),
		"no tests":  branch("no-tests", map[string]string{"pkg/f.go": "package pkg\n\nfunc F() int { return 2 }\n", "docs/readme.txt": "y\n"}),
	}
	reg := tools.New(tools.Options{Cache: tools.NewMemCache()})
	reg.MustRegister((&build.Runner{}).Tools()...)
	e := &Engine{Reg: reg, ScratchDir: t.TempDir()}
	cfg, err := FromRefs([]api.GateRef{{Name: "bites", Tool: "test.bites", Args: map[string]any{
		"argv":   []any{"sh", "-c", "go test ./pkg/", "sh", "{changed:**/*_test.go}"},
		"revert": []any{"pkg/**"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{"biting": true, "toothless": false, "new code": true, "tests": true, "no tests": true} {
		v, err := e.Evaluate(ctx, Request{Repo: repo.Dir, Commit: snaps[name], Base: base, Config: &cfg})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		g := v.Gates[0]
		if v.Passed != want {
			t.Errorf("%s: passed=%v, want %v: %+v", name, v.Passed, want, g)
		}
		if !want && (len(g.New) != 1 || g.New[0].Code != "TestsPassWithoutChange" || !strings.Contains(g.New[0].Message, "pkg/f.go reverted, the tests in pkg/f_test.go still pass")) {
			t.Errorf("%s: %+v", name, g.New)
		}
	}
	parsed := func(script string) Config {
		c, err := FromRefs([]api.GateRef{{Name: "bites", Tool: "test.bites", Args: map[string]any{
			"argv": []any{"sh", "-c", script, "sh", "{changed:**/*_test.go}"}, "revert": []any{"pkg/**"}, "parser": "gotest-json",
		}}})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	for script, want := range map[string]string{"go test -json ./pkg/": "", "echo broken >&2; exit 3": "UnparsedFailure"} {
		c := parsed(script)
		v, err := e.Evaluate(ctx, Request{Repo: repo.Dir, Commit: snaps["biting"], Base: base, Config: &c})
		if err != nil {
			t.Fatal(err)
		}
		if g := v.Gates[0]; g.Reason != want || v.Passed != (want == "") || (want != "" && g.OutputTail != "broken") {
			t.Errorf("%q: a failure must name failing tests to count: %+v", script, g)
		}
	}
	if s := repo.Git("status", "--porcelain"); s != "" {
		t.Fatalf("checkout touched: %s", s)
	}
}
