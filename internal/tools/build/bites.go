package build

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/patcarter883/spine-next/internal/gitro"
	"github.com/patcarter883/spine-next/internal/tools"
	"github.com/patcarter883/spine-next/internal/tools/diff"
)

// BitesArgs are test.bites' arguments.
type BitesArgs struct {
	// Argv runs the change's tests. Each "{changed:GLOB}" element expands to
	// the added and modified files matching GLOB; those are the tests.
	Argv []string `json:"argv"`
	// Revert globs name the code the tests are meant to cover: matching
	// changed files are put back as they were before the change.
	Revert []string          `json:"revert"`
	Dir    string            `json:"dir,omitempty"`
	Env    map[string]string `json:"env,omitempty"`
}

const bitesSchema = `{
  "type": "object",
  "required": ["argv", "revert"],
  "additionalProperties": false,
  "properties": {
    "argv":   {"type": "array", "minItems": 1, "items": {"type": "string"}, "contains": {"type": "string", "pattern": "^\\{changed:.+\\}$"}},
    "revert": {"type": "array", "minItems": 1, "items": {"type": "string"}},
    "dir":    {"type": "string", "pattern": "^[^/]"},
    "env":    {"type": "object", "propertyNames": {"pattern": "^[A-Za-z_][A-Za-z0-9_]*$", "not": {"pattern": "^GIT_"}}, "additionalProperties": {"type": "string"}}
  }
}`

// Bites is test.bites: it runs the tests a change adds or edits against the
// change with its covered code reverted, and passes only if they fail
// there. Tests that pass either way do not test the change. A change with
// no such tests, or no such code, has nothing to check and passes.
type Bites struct{ r *Runner }

func (b *Bites) Spec() tools.Spec {
	return tools.Spec{Name: "test.bites", Version: "1", Determinism: tools.Hermetic, UsesBase: true,
		Timeout: 30 * time.Minute, InputSchema: json.RawMessage(bitesSchema)}
}

func (b *Bites) EnvKey(ctx context.Context, inv tools.Invocation) (string, error) {
	var a BitesArgs
	if err := json.Unmarshal(inv.Args, &a); err != nil {
		return "", err
	}
	return b.r.envKey(a.Argv[0])
}

func (b *Bites) Run(ctx context.Context, inv tools.Invocation) (tools.Result, error) {
	var a BitesArgs
	if err := json.Unmarshal(inv.Args, &a); err != nil {
		return tools.Result{}, err
	}
	cs, _, err := diff.Changes(ctx, inv)
	if err != nil {
		return tools.Result{}, fmt.Errorf("test.bites: %w", err)
	}
	var testGlobs []string
	for _, s := range a.Argv {
		if m := changedRe.FindStringSubmatch(s); m != nil && m[1] != "" {
			testGlobs = append(testGlobs, m[1])
		}
	}
	isTest := func(p string) bool {
		return slices.ContainsFunc(testGlobs, func(g string) bool { return diff.MatchGlob(g, p) })
	}
	var tests, covered []string
	var edits []gitro.TreeEdit
	for _, c := range cs {
		if isTest(c.Path) {
			if c.Status != 'D' {
				tests = append(tests, c.Path)
			}
			continue
		}
		if !slices.ContainsFunc(a.Revert, func(g string) bool {
			return diff.MatchGlob(g, c.Path) || (c.OldPath != "" && diff.MatchGlob(g, c.OldPath))
		}) {
			continue
		}
		covered = append(covered, c.Path)
		switch c.Status {
		case 'A':
			edits = append(edits, gitro.TreeEdit{Path: c.Path})
		case 'R':
			edits = append(edits, gitro.TreeEdit{Path: c.Path}, gitro.TreeEdit{Path: c.OldPath, Mode: c.OldMode, OID: c.OldOID})
		default:
			edits = append(edits, gitro.TreeEdit{Path: c.Path, Mode: c.OldMode, OID: c.OldOID})
		}
	}
	metrics := map[string]float64{"tests": float64(len(tests)), "reverted": float64(len(covered))}
	if len(tests) == 0 || len(covered) == 0 {
		return tools.Result{Metrics: metrics}, nil
	}
	if inv.MaterializeTree == nil {
		return tools.Result{}, tools.ErrNoDir
	}
	reverted, err := gitro.Repo{Dir: inv.Repo}.EditTree(ctx, inv.Tree, edits)
	if err != nil {
		return tools.Result{}, fmt.Errorf("test.bites: %w", err)
	}
	dir, err := inv.MaterializeTree(ctx, reverted)
	if err != nil {
		return tools.Result{}, fmt.Errorf("test.bites: %w", err)
	}
	argv, _, err := expandChanged(ctx, inv, Args{Argv: a.Argv, Dir: a.Dir})
	if err != nil {
		return tools.Result{}, fmt.Errorf("test.bites: %w", err)
	}
	res, err := b.r.run(ctx, dir, Args{Argv: argv, Dir: a.Dir, Env: a.Env})
	if err != nil {
		return tools.Result{}, err
	}
	if slices.ContainsFunc(res.Diagnostics, func(d tools.Diagnostic) bool { return d.Code == "Timeout" }) {
		return res, nil
	}
	out := tools.Result{Duration: res.Duration, Artifacts: res.Artifacts, Metrics: metrics}
	if res.ExitCode != 0 {
		return out, nil
	}
	sort.Strings(tests)
	sort.Strings(covered)
	out.ExitCode = 1
	out.Diagnostics = []tools.Diagnostic{{Severity: tools.Error, Code: "TestsPassWithoutChange", File: tests[0],
		Message: fmt.Sprintf("With the change to %s reverted, the tests in %s still pass, so they do not check what the change does. "+
			"Add assertions that fail without it: assert the exact result or response body the change produces, not only that a call succeeds.",
			list(covered, 8), list(tests, 8)),
		Fingerprint: tools.Fingerprint("test.bites", "TestsPassWithoutChange", strings.Join(tests, ","))}}
	return out, nil
}

func list(ps []string, n int) string {
	if len(ps) <= n {
		return strings.Join(ps, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(ps[:n], ", "), len(ps)-n)
}
