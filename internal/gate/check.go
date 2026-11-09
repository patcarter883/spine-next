package gate

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/patcarter883/spine-next/internal/api"
	"github.com/patcarter883/spine-next/internal/gitro"
	"github.com/patcarter883/spine-next/internal/tools"
)

// CheckLevel classifies one gate in a dry run.
type CheckLevel string

const (
	CheckOK      CheckLevel = "ok"       // runs and passes on the revision
	CheckExcused CheckLevel = "excused"  // baseline gate with pre-existing failures (tasks are judged on new ones only)
	CheckPerTask CheckLevel = "per-task" // depends on a task's change (diff.*, API contract); arguments validated only
	CheckFail    CheckLevel = "fail"     // every task would fail it, or it cannot run
)

// GateCheck is one gate's dry-run result.
type GateCheck struct {
	Gate        string            `json:"gate"`
	Tool        string            `json:"tool"`
	Policy      api.GatePolicy    `json:"policy"`
	Level       CheckLevel        `json:"level"`
	Message     string            `json:"message"`
	ExitCode    int               `json:"exit_code"`
	Errors      int               `json:"errors"`
	Diagnostics []api.Diagnostic  `json:"diagnostics,omitempty"` // first few
	Duration    time.Duration     `json:"duration_ns"`
	Artifacts   []api.ArtifactRef `json:"artifacts,omitempty"`
	OutputTail  string            `json:"output_tail,omitempty"`
}

// CheckReport is `spine gates check`.
type CheckReport struct {
	Rev      string        `json:"rev"`
	Commit   string        `json:"commit"`
	Source   string        `json:"source"` // where the gates came from
	Gates    []GateCheck   `json:"gates"`
	OK       bool          `json:"ok"` // no gate at level fail
	Duration time.Duration `json:"duration_ns"`
}

// Check dry-runs every gate of cfg on rev with no agent and no change: the
// onboarding test that the gates can run here at all, and which of them a
// task can actually pass. Unlike Evaluate it runs every gate (no early
// stop) and reports raw tool results. worktree, when set, is a provisioned
// checkout that cfg.Baseline.Link directories are linked from, as a task's
// lease is.
func (e *Engine) Check(ctx context.Context, repo, rev string, cfg Config, source, worktree string) (CheckReport, error) {
	start := time.Now()
	r := gitro.Repo{Dir: repo}
	commit, err := r.Line(ctx, "rev-parse", "--verify", "-q", rev+"^{commit}")
	if err != nil {
		return CheckReport{}, fmt.Errorf("gates check: %s is not a commit", rev)
	}
	tree, err := r.TreeOf(ctx, commit)
	if err != nil {
		return CheckReport{}, err
	}
	rep := CheckReport{Rev: rev, Commit: commit, Source: source, OK: true}
	mats := &materializer{repo: repo, scratch: e.ScratchDir, worktree: worktree, link: cfg.Baseline.Link}
	defer mats.cleanup()
	for _, name := range cfg.Order {
		g := cfg.Gates[name]
		gc := GateCheck{Gate: name, Tool: g.Tool, Policy: g.Policy}
		if gc.Policy == "" {
			gc.Policy = api.Absolute
		}
		t, ok := e.Reg.Lookup(g.Tool)
		args := gateArgs(g, nil)
		switch {
		case !ok:
			gc.Level, gc.Message = CheckFail, "unknown tool "+g.Tool
		case t.Spec().UsesBase:
			if _, err := e.Reg.Canonicalize(g.Tool, args); err != nil {
				gc.Level, gc.Message = CheckFail, "bad arguments: "+err.Error()
			} else {
				gc.Level, gc.Message = CheckPerTask, "judges each task's change against its base; arguments are valid"
			}
		default:
			res, err := e.Reg.Invoke(ctx, g.Tool, tools.Invocation{Repo: repo, Tree: tree, Commit: commit, Base: commit,
				Args: args, Timeout: g.Timeout, BaseKey: tree + " " + tree, Materialize: mats.for_(tree)})
			if err != nil {
				gc.Level, gc.Message = CheckFail, "could not run: "+err.Error()
				break
			}
			gc.ExitCode, gc.Duration, gc.Artifacts, gc.OutputTail = res.ExitCode, res.Duration, res.Artifacts, res.OutputTail
			errs := errorsOf(res.Diagnostics)
			gc.Errors = len(errs)
			if len(errs) > 5 {
				errs = errs[:5]
			}
			gc.Diagnostics = errs
			switch {
			case res.ExitCode == 0:
				gc.Level, gc.Message = CheckOK, "passes"
			case g.Policy == api.BaselineRelative && !res.Unparsed && gc.Errors > 0 && !res.Truncated:
				gc.Level = CheckExcused
				gc.Message = fmt.Sprintf("%d pre-existing failure(s); tasks are judged only on failures they introduce", gc.Errors)
			case g.Policy == api.BaselineRelative:
				gc.Level = CheckFail
				gc.Message = "fails without parseable diagnostics, so the baseline cannot excuse it: every task would fail"
			default:
				gc.Level = CheckFail
				gc.Message = fmt.Sprintf("exits %d on this revision with an absolute policy: every task would fail (fix it, or use policy: baseline)", res.ExitCode)
			}
		}
		if gc.Level == CheckFail {
			rep.OK = false
		}
		rep.Gates = append(rep.Gates, gc)
	}
	rep.Duration = time.Since(start)
	return rep, nil
}

// String renders a report for the terminal.
func (r CheckReport) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "gates from %s, checked on %s (%s)\n", r.Source, r.Rev, r.Commit[:12])
	for _, g := range r.Gates {
		fmt.Fprintf(&b, "%-9s %-12s %-16s %s", g.Level, g.Gate, g.Tool, g.Message)
		if g.Duration > 0 {
			fmt.Fprintf(&b, " [%s]", g.Duration.Round(time.Millisecond))
		}
		b.WriteString("\n")
		for _, d := range g.Diagnostics {
			loc := d.File
			if d.Line > 0 {
				loc = fmt.Sprintf("%s:%d", d.File, d.Line)
			}
			msg, _, _ := strings.Cut(d.Message, "\n")
			fmt.Fprintf(&b, "            %s %s %s\n", d.Code, loc, msg)
		}
		if g.Level == CheckFail && g.OutputTail != "" {
			lines := strings.Split(g.OutputTail, "\n")
			if len(lines) > 15 {
				lines = lines[len(lines)-15:]
			}
			for _, l := range lines {
				fmt.Fprintf(&b, "            | %s\n", l)
			}
		}
	}
	return b.String()
}
