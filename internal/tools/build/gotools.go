package build

import (
	"context"
	"encoding/json"
	"time"

	"github.com/patcarter883/spine-next/internal/tools"
)

// goCmd is a cmd.run specialization with a fixed argv and parser.
type goCmd struct {
	r       *Runner
	name    string
	timeout time.Duration
	argv    func(a goArgs) []string
	parser  string
}

type goArgs struct {
	Packages []string `json:"packages,omitempty"`
	Flags    []string `json:"flags,omitempty"` // appended before packages, e.g. ["-race"] or ["-run","TestX"]
	Dir      string   `json:"dir,omitempty"`
}

const goArgsSchema = `{"type":"object","additionalProperties":false,"properties":{
  "packages":{"type":"array","items":{"type":"string","pattern":"^[^-]"}},
  "flags":{"type":"array","items":{"type":"string"}},
  "dir":{"type":"string","pattern":"^[^/]"}}}`

func goTool(r *Runner, name string) tools.Tool {
	g := &goCmd{r: r, name: name, parser: "go-build", timeout: 5 * time.Minute}
	switch name {
	case "go.build":
		g.argv = func(a goArgs) []string { return append(append([]string{"go", "build"}, a.Flags...), pkgs(a)...) }
	case "go.vet":
		g.argv = func(a goArgs) []string { return append(append([]string{"go", "vet"}, a.Flags...), pkgs(a)...) }
	case "go.test":
		g.parser, g.timeout = "gotest-json", 10*time.Minute
		g.argv = func(a goArgs) []string {
			return append(append([]string{"go", "test", "-json"}, a.Flags...), pkgs(a)...)
		}
	}
	return g
}

func pkgs(a goArgs) []string {
	if len(a.Packages) == 0 {
		return []string{"./..."}
	}
	return a.Packages
}

func (g *goCmd) Spec() tools.Spec {
	return tools.Spec{Name: g.name, Version: "1", Determinism: tools.Hermetic, NeedsDir: true,
		Timeout: g.timeout, InputSchema: json.RawMessage(goArgsSchema)}
}

func (g *goCmd) EnvKey(ctx context.Context, inv tools.Invocation) (string, error) {
	return g.r.envKey("go")
}

func (g *goCmd) Run(ctx context.Context, inv tools.Invocation) (tools.Result, error) {
	var a goArgs
	if err := json.Unmarshal(inv.Args, &a); err != nil {
		return tools.Result{}, err
	}
	return g.r.run(ctx, inv.Dir, Args{Argv: g.argv(a), Parser: g.parser, Dir: a.Dir})
}
