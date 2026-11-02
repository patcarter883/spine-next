// Package diff implements the change-shape tools: diff.stats, diff.nonempty,
// diff.scope, diff.applycheck and git.preflight (RFC §5.1). All are Pure:
// they read git objects only, so their results are functions of the tree,
// base and args, and are cached accordingly.
package diff

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/patcarter883/spine-next/internal/gitro"
	"github.com/patcarter883/spine-next/internal/tools"
)

// Tools returns the diff tool set.
func Tools() []tools.Tool {
	return []tools.Tool{Stats{}, NonEmpty{}, Scope{}, ApplyCheck{}, Preflight{}}
}

// Sides resolves the trees a change is measured between: the merge base of
// Commit and Base (when both are known) or Base itself, and Tree.
func Sides(ctx context.Context, inv tools.Invocation) (left, right string, err error) {
	if inv.Base == "" {
		return "", "", fmt.Errorf("invocation has no base commit")
	}
	r := gitro.Repo{Dir: inv.Repo}
	leftRev := inv.Base
	if inv.Commit != "" {
		if leftRev, err = r.MergeBase(ctx, inv.Commit, inv.Base); err != nil {
			return "", "", err
		}
	}
	if left, err = r.TreeOf(ctx, leftRev); err != nil {
		return "", "", err
	}
	return left, inv.Tree, nil
}

// Changes lists the task's own changes (merge base → tree).
func Changes(ctx context.Context, inv tools.Invocation) ([]gitro.Change, string, error) {
	left, right, err := Sides(ctx, inv)
	if err != nil {
		return nil, "", err
	}
	cs, err := gitro.Repo{Dir: inv.Repo}.DiffTrees(ctx, left, right)
	return cs, left, err
}

func diag(sev tools.Severity, tool, code, file, msg string) tools.Diagnostic {
	return tools.Diagnostic{Severity: sev, Code: code, File: file, Message: msg,
		Fingerprint: tools.Fingerprint(tool, code, file)}
}

// - diff.stats -

type Stats struct{}

func (Stats) Spec() tools.Spec {
	return tools.Spec{Name: "diff.stats", Version: "1", Determinism: tools.Pure, ReadOnly: true, UsesBase: true,
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`)}
}

func (Stats) Run(ctx context.Context, inv tools.Invocation) (tools.Result, error) {
	left, right, err := Sides(ctx, inv)
	if err != nil {
		return tools.Result{}, err
	}
	out, err := gitro.Repo{Dir: inv.Repo}.Run(ctx, "diff-tree", "-r", "-z", "-M", "--numstat", left, right)
	if err != nil {
		return tools.Result{}, err
	}
	m := map[string]float64{"files_changed": 0, "insertions": 0, "deletions": 0, "binary_files": 0, "renames": 0}
	recs := bytes.Split(out, []byte{0})
	for i := 0; i < len(recs); i++ {
		rec := string(recs[i])
		if rec == "" {
			continue
		}
		p := strings.SplitN(rec, "\t", 3)
		if len(p) != 3 {
			continue
		}
		m["files_changed"]++
		if p[0] == "-" {
			m["binary_files"]++
		} else {
			a, _ := strconv.Atoi(p[0])
			d, _ := strconv.Atoi(p[1])
			m["insertions"] += float64(a)
			m["deletions"] += float64(d)
		}
		if p[2] == "" { // rename: old and new path follow as separate records
			m["renames"]++
			i += 2
		}
	}
	return tools.Result{Metrics: m}, nil
}

// - diff.nonempty -

// NonEmpty fails when the snapshot changes nothing against its merge base
// with base: a task cannot complete by doing nothing (RFC §5.1).
type NonEmpty struct{}

func (NonEmpty) Spec() tools.Spec {
	return tools.Spec{Name: "diff.nonempty", Version: "1", Determinism: tools.Pure, ReadOnly: true, UsesBase: true,
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`)}
}

func (NonEmpty) Run(ctx context.Context, inv tools.Invocation) (tools.Result, error) {
	left, right, err := Sides(ctx, inv)
	if err != nil {
		return tools.Result{}, err
	}
	r := gitro.Repo{Dir: inv.Repo}
	if inv.Commit != "" {
		mb, err := r.MergeBase(ctx, inv.Commit, inv.Base)
		if err != nil {
			return tools.Result{}, err
		}
		n, err := r.Line(ctx, "rev-list", "--count", mb+".."+inv.Commit)
		if err != nil {
			return tools.Result{}, err
		}
		if n == "0" {
			return tools.Result{ExitCode: 1, Diagnostics: []tools.Diagnostic{
				diag(tools.Error, "diff.nonempty", "NoCommits", "", "snapshot has no commits beyond its merge base with base")}}, nil
		}
	}
	if left == right {
		return tools.Result{ExitCode: 1, Diagnostics: []tools.Diagnostic{
			diag(tools.Error, "diff.nonempty", "NoChange", "", "snapshot tree is identical to its merge base with base")}}, nil
	}
	return tools.Result{}, nil
}

// - diff.scope -

type Scope struct{}

func (Scope) Spec() tools.Spec {
	return tools.Spec{Name: "diff.scope", Version: "1", Determinism: tools.Pure, ReadOnly: true, UsesBase: true,
		InputSchema: json.RawMessage(`{"type":"object","required":["scope"],"additionalProperties":false,
			"properties":{"scope":{"type":"array","items":{"type":"string","minLength":1}},
			"deny":{"type":"array","items":{"type":"string","minLength":1}}}}`)}
}

type scopeArgs struct {
	Scope []string `json:"scope"`
	Deny  []string `json:"deny"`
}

func (Scope) Run(ctx context.Context, inv tools.Invocation) (tools.Result, error) {
	var a scopeArgs
	if err := json.Unmarshal(inv.Args, &a); err != nil {
		return tools.Result{}, err
	}
	cs, _, err := Changes(ctx, inv)
	if err != nil {
		return tools.Result{}, err
	}
	var res tools.Result
	check := func(p string) {
		switch {
		case InScope(a.Deny, p):
			res.Diagnostics = append(res.Diagnostics, diag(tools.Error, "diff.scope", "DeniedPath", p, "change to a denied path: "+p))
		case !InScope(a.Scope, p):
			res.Diagnostics = append(res.Diagnostics, diag(tools.Error, "diff.scope", "OutOfScope", p, "change outside task scope: "+p))
		}
	}
	for _, c := range cs {
		check(c.Path)
		if c.OldPath != "" {
			check(c.OldPath)
		}
	}
	if len(res.Diagnostics) > 0 {
		res.ExitCode = 1
	}
	res.Metrics = map[string]float64{"paths_checked": float64(len(cs))}
	return res, nil
}

// - diff.applycheck -

// ApplyCheck validates a worker-produced patch against the tree using a
// temporary index; nothing on disk or in the repository changes.
type ApplyCheck struct{}

func (ApplyCheck) Spec() tools.Spec {
	return tools.Spec{Name: "diff.applycheck", Version: "1", Determinism: tools.Pure, ReadOnly: true,
		InputSchema: json.RawMessage(`{"type":"object","required":["patch"],"additionalProperties":false,
			"properties":{"patch":{"type":"string","maxLength":4194304}}}`)}
}

var reApplyErr = regexp.MustCompile(`^error: (?:patch failed: )?([^:]+)(?::\d+)?(?:: (.*))?$`)

func (ApplyCheck) Run(ctx context.Context, inv tools.Invocation) (tools.Result, error) {
	var a struct {
		Patch string `json:"patch"`
	}
	if err := json.Unmarshal(inv.Args, &a); err != nil {
		return tools.Result{}, err
	}
	idx, err := os.CreateTemp("", "spine-apply-*.idx")
	if err != nil {
		return tools.Result{}, err
	}
	idxPath := idx.Name()
	idx.Close()
	os.Remove(idxPath)
	defer os.Remove(idxPath)
	r := gitro.Repo{Dir: inv.Repo}
	env := []string{"GIT_INDEX_FILE=" + idxPath}
	if _, code, stderr, err := r.RunRC(ctx, env, nil, "read-tree", inv.Tree); err != nil {
		return tools.Result{}, err
	} else if code != 0 {
		return tools.Result{}, &gitro.Error{Args: []string{"read-tree", inv.Tree}, ExitCode: code, Stderr: stderr}
	}
	_, code, stderr, err := r.RunRC(ctx, env, []byte(a.Patch), "apply", "--check", "--cached", "-")
	if err != nil {
		return tools.Result{}, err
	}
	var res tools.Result
	if code == 0 {
		return res, nil
	}
	res.ExitCode = 1
	seen := map[string]bool{}
	for _, line := range strings.Split(stderr, "\n") {
		m := reApplyErr.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil || seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		msg := strings.TrimSpace(line)
		res.Diagnostics = append(res.Diagnostics, diag(tools.Error, "diff.applycheck", "PatchDoesNotApply", m[1], msg))
	}
	if len(res.Diagnostics) == 0 {
		res.Diagnostics = append(res.Diagnostics, diag(tools.Error, "diff.applycheck", "PatchInvalid", "", strings.TrimSpace(stderr)))
	}
	return res, nil
}

// - git.preflight -

// Preflight runs merge-tree --write-tree of the snapshot commit into base.
type Preflight struct{}

func (Preflight) Spec() tools.Spec {
	return tools.Spec{Name: "git.preflight", Version: "1", Determinism: tools.Pure, ReadOnly: true, UsesBase: true,
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`)}
}

func (Preflight) Run(ctx context.Context, inv tools.Invocation) (tools.Result, error) {
	if inv.Commit == "" || inv.Base == "" {
		return tools.Result{}, fmt.Errorf("git.preflight needs a commit and a base")
	}
	out, code, stderr, err := gitro.Repo{Dir: inv.Repo}.RunRC(ctx, nil, nil,
		"merge-tree", "--write-tree", "--name-only", "--no-messages", inv.Base, inv.Commit)
	if err != nil {
		return tools.Result{}, err
	}
	if code > 1 {
		return tools.Result{}, &gitro.Error{Args: []string{"merge-tree"}, ExitCode: code, Stderr: stderr}
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	data, _ := json.Marshal(map[string]string{"tree": lines[0]})
	res := tools.Result{Data: data}
	if code == 1 {
		res.ExitCode = 1
		for _, p := range lines[1:] {
			if p = strings.TrimSpace(p); p != "" {
				res.Diagnostics = append(res.Diagnostics, diag(tools.Error, "git.preflight", "Conflict", p, "merge conflict: "+p))
			}
		}
	}
	res.Metrics = map[string]float64{"conflicts": float64(len(res.Diagnostics))}
	return res, nil
}
