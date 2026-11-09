// Package gate is the gate engine (RFC §5.4): it runs a project's gates,
// cheap to expensive, against a snapshot and turns tool results into
// verdicts. Only this package produces GatePassed/Verified evidence (RFC
// §4.3). Baseline-relative gates subtract failures that already exist on the
// base tree, so pre-existing debt does not block a change, while any failure
// that cannot be parsed into diagnostics is never excused.
package gate

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/patcarter883/spine-next/internal/api"
	"github.com/patcarter883/spine-next/internal/gitro"
	"github.com/patcarter883/spine-next/internal/tools"
)

// MaxVerdictDiagnostics caps New per verdict.
const MaxVerdictDiagnostics = 100

// Engine evaluates gates.
type Engine struct {
	Reg *tools.Registry
	// ScratchDir holds materialized trees (default os.TempDir()).
	ScratchDir string
	// AlwaysBaseline runs baseline-relative gates on the base tree even when
	// the snapshot is clean, so Fixed is always populated. By default the
	// base run happens only when the snapshot has failures to excuse.
	AlwaysBaseline bool
}

// Request is one evaluation.
type Request struct {
	Repo   string // directory git resolves objects from
	Commit string // snapshot commit
	Base   string // base commit (integration target at evaluation time)
	// Worktree is the task's lease checkout; Baseline.Link directories are
	// symlinked from it into materialized trees. Optional.
	Worktree string
	// Scope fills diff.scope's "scope" argument when the gate omits it.
	Scope []string
	// Config overrides the gates file read from Base.
	Config *Config
}

// Verdict is the result of all gates.
type Verdict struct {
	Passed     bool              `json:"passed"`
	FailedGate string            `json:"failed_gate,omitempty"`
	Tree       string            `json:"tree"`
	BaseTree   string            `json:"base_tree"`
	Gates      []api.GateVerdict `json:"gates"`
	Duration   time.Duration     `json:"duration_ns"`
}

// ErrNoGates is returned when the base has no gates file and none is given.
var ErrNoGates = fmt.Errorf("gate: no %s at base and no gates configured", ConfigPath)

// Validate checks that every gate names a registered tool and has valid args.
func (e *Engine) Validate(c Config, scope []string) error {
	for _, n := range c.Order {
		g := c.Gates[n]
		if _, ok := e.Reg.Lookup(g.Tool); !ok {
			return fmt.Errorf("gate %s: %w: %s", n, tools.ErrUnknownTool, g.Tool)
		}
		if _, err := e.Reg.Canonicalize(g.Tool, gateArgs(g, scope)); err != nil {
			return fmt.Errorf("gate %s: %w", n, err)
		}
	}
	return nil
}

func gateArgs(g api.GateRef, scope []string) json.RawMessage {
	args := map[string]any{}
	for k, v := range g.Args {
		args[k] = v
	}
	if g.Tool == "diff.scope" {
		if _, ok := args["scope"]; !ok {
			s := scope
			if len(s) == 0 {
				s = []string{"**"}
			}
			args["scope"] = s
		}
	}
	b, _ := json.Marshal(args)
	return b
}

// Evaluate runs the gates in order. The first failing gate stops the run;
// later gates are reported Skipped. An error means a tool could not run (an
// infrastructure fault): the caller retries and must not count it as a
// failed round.
func (e *Engine) Evaluate(ctx context.Context, req Request) (Verdict, error) {
	start := time.Now()
	r := gitro.Repo{Dir: req.Repo}
	tree, err := r.TreeOf(ctx, req.Commit)
	if err != nil {
		return Verdict{}, fmt.Errorf("gate: snapshot %s: %w", req.Commit, err)
	}
	baseTree, err := r.TreeOf(ctx, req.Base)
	if err != nil {
		return Verdict{}, fmt.Errorf("gate: base %s: %w", req.Base, err)
	}
	mb, err := r.MergeBase(ctx, req.Commit, req.Base)
	if err != nil {
		return Verdict{}, fmt.Errorf("gate: merge base of %s and %s: %w", req.Commit, req.Base, err)
	}
	mbTree, err := r.TreeOf(ctx, mb)
	if err != nil {
		return Verdict{}, err
	}
	var cfg Config
	if req.Config != nil {
		cfg = *req.Config
	} else {
		c, ok, err := LoadConfig(ctx, req.Repo, req.Base)
		if err != nil {
			return Verdict{}, err
		}
		if !ok {
			return Verdict{}, ErrNoGates
		}
		cfg = c
	}
	if err := e.Validate(cfg, req.Scope); err != nil {
		return Verdict{}, err
	}

	mats := &materializer{repo: req.Repo, scratch: e.ScratchDir, worktree: req.Worktree, link: cfg.Baseline.Link}
	defer mats.cleanup()

	v := Verdict{Passed: true, Tree: tree, BaseTree: baseTree}
	for _, name := range cfg.Order {
		g := cfg.Gates[name]
		if !v.Passed {
			v.Gates = append(v.Gates, api.GateVerdict{Gate: name, Tool: g.Tool, Policy: g.Policy, Stage: g.Stage, Skipped: true, ExitCode: 1, Reason: "EarlierGateFailed"})
			continue
		}
		args := gateArgs(g, req.Scope)
		judge := func(recheck bool) (api.GateVerdict, bool, error) {
			cur, err := e.Reg.Invoke(ctx, g.Tool, tools.Invocation{
				Repo: req.Repo, Tree: tree, Commit: req.Commit, Base: req.Base, Args: args, Timeout: g.Timeout,
				BaseKey: mbTree + " " + baseTree, Materialize: mats.for_(tree), Recheck: recheck,
			})
			if err != nil {
				return api.GateVerdict{}, false, fmt.Errorf("gate %s: %w", name, err)
			}
			gv := api.GateVerdict{Gate: name, Tool: g.Tool, Policy: g.Policy, Stage: g.Stage, ToolExitCode: cur.ExitCode,
				Metrics: cur.Metrics, Cached: cur.Cached, Duration: cur.Duration, Artifacts: cur.Artifacts}
			if g.Policy == api.BaselineRelative {
				var base *tools.Result
				if cur.ExitCode != 0 || hasErrors(cur.Diagnostics) || e.AlwaysBaseline {
					b, err := e.Reg.Invoke(ctx, g.Tool, tools.Invocation{
						Repo: req.Repo, Tree: baseTree, Commit: req.Base, Base: req.Base, Args: args, Timeout: g.Timeout,
						BaseKey: baseTree + " " + baseTree, Materialize: mats.for_(baseTree),
					})
					if err != nil {
						return api.GateVerdict{}, false, fmt.Errorf("gate %s (baseline): %w", name, err)
					}
					base = &b
					for _, a := range b.Artifacts {
						a.Name = "base-" + a.Name
						gv.Artifacts = append(gv.Artifacts, a)
					}
					code := b.ExitCode
					gv.BaseExitCode = &code
					gv.Cached = gv.Cached && b.Cached
				}
				baselineVerdict(&gv, base, cur)
			} else {
				absoluteVerdict(&gv, cur)
			}
			if !gv.Passed() && len(gv.New) == 0 {
				gv.OutputTail = cur.OutputTail
			}
			return gv, cur.Cached, nil
		}
		gv, fromCache, err := judge(false)
		if err == nil && fromCache && !gv.Passed() {
			gv, _, err = judge(true)
		}
		if err != nil {
			return Verdict{}, err
		}
		if len(gv.New) > MaxVerdictDiagnostics {
			gv.New = gv.New[:MaxVerdictDiagnostics]
		}
		v.Gates = append(v.Gates, gv)
		if !gv.Passed() {
			v.Passed = false
			v.FailedGate = name
		}
	}
	v.Duration = time.Since(start)
	return v, nil
}

func absoluteVerdict(gv *api.GateVerdict, cur tools.Result) {
	if cur.ExitCode != 0 {
		gv.ExitCode = 1
		gv.Reason = "ToolFailed"
		if cur.Unparsed {
			gv.Reason = "UnparsedFailure"
		}
	}
	gv.New = errorsOf(cur.Diagnostics)
}

// baselineVerdict implements RFC §5.4: pass iff no error diagnostic is new
// relative to the base tree; a failure with no parseable diagnostics is
// never excused.
func baselineVerdict(gv *api.GateVerdict, base *tools.Result, cur tools.Result) {
	known := map[string]bool{}
	if base != nil {
		for _, d := range base.Diagnostics {
			if d.Severity == api.SeverityError {
				known[d.Fingerprint] = true
			}
		}
	}
	curSet := map[string]bool{}
	for _, d := range cur.Diagnostics {
		if d.Severity != api.SeverityError {
			continue
		}
		curSet[d.Fingerprint] = true
		if known[d.Fingerprint] {
			gv.Excused++
		} else {
			gv.New = append(gv.New, d)
		}
	}
	for fp := range known {
		if !curSet[fp] {
			gv.Fixed = append(gv.Fixed, fp)
		}
	}
	sort.Strings(gv.Fixed)
	switch {
	case len(gv.New) > 0:
		gv.ExitCode, gv.Reason = 1, "NewFailures"
	case cur.ExitCode != 0 && (cur.Unparsed || !hasErrors(cur.Diagnostics)):
		gv.ExitCode, gv.Reason = 1, "UnparsedFailure"
	case cur.ExitCode != 0 && cur.Truncated:
		// Diagnostics were cut off: the excused set may hide new failures.
		gv.ExitCode, gv.Reason = 1, "TruncatedFailure"
	case cur.ExitCode != 0:
		gv.Reason = "PreexistingFailuresOnly"
	}
}

func errorsOf(ds []tools.Diagnostic) []api.Diagnostic {
	var out []api.Diagnostic
	for _, d := range ds {
		if d.Severity == api.SeverityError {
			out = append(out, d)
		}
	}
	return out
}

func hasErrors(ds []tools.Diagnostic) bool { return len(errorsOf(ds)) > 0 }

// materializer checks trees out into scratch directories on first use and
// removes them when the evaluation ends.
type materializer struct {
	repo, scratch, worktree string
	link                    []string
	mu                      sync.Mutex
	dirs                    map[string]*matDir
}

type matDir struct {
	once sync.Once
	dir  string
	err  error
}

func (m *materializer) for_(tree string) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		m.mu.Lock()
		if m.dirs == nil {
			m.dirs = map[string]*matDir{}
		}
		d, ok := m.dirs[tree]
		if !ok {
			d = &matDir{}
			m.dirs[tree] = d
		}
		m.mu.Unlock()
		d.once.Do(func() { d.dir, d.err = m.make(ctx, tree) })
		return d.dir, d.err
	}
}

func (m *materializer) make(ctx context.Context, tree string) (string, error) {
	root := m.scratch
	if root == "" {
		root = os.TempDir()
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	parent, err := os.MkdirTemp(root, "spine-tree-")
	if err != nil {
		return "", err
	}
	// A fixed leaf name keeps absolute paths identical across materializations
	// of different trees in one evaluation except for the parent component,
	// which NormalizeMessage strips as the root.
	dir := filepath.Join(parent, "src")
	if err := (gitro.Repo{Dir: m.repo}).Materialize(ctx, tree, dir); err != nil {
		os.RemoveAll(parent)
		return "", err
	}
	if m.worktree != "" {
		for _, l := range m.link {
			src := filepath.Join(m.worktree, filepath.FromSlash(l))
			dst := filepath.Join(dir, filepath.FromSlash(l))
			if _, err := os.Lstat(src); err != nil {
				continue
			}
			if _, err := os.Lstat(dst); err == nil {
				continue // the tree has its own; never shadow committed content
			}
			os.MkdirAll(filepath.Dir(dst), 0o755)
			if err := os.Symlink(src, dst); err != nil {
				os.RemoveAll(parent)
				return "", err
			}
		}
	}
	return dir, nil
}

func (m *materializer) cleanup() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, d := range m.dirs {
		if d.dir != "" {
			os.RemoveAll(filepath.Dir(d.dir))
		}
	}
}
