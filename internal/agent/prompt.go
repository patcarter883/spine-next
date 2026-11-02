package agent

import (
	"fmt"
	"strings"

	"github.com/patcarter883/spine-next/internal/api"
)

// Prompt is built from typed inputs only (RFC §3.4): the worker never sees
// the supervisor's reasoning, another worker's output, or transcripts.
type Prompt struct {
	Task        api.TaskID
	Attempt     int
	Round       int
	Intent      string
	Scope       []string
	Acceptance  []string
	Feedback    []api.Feedback
	Constraints []string
}

// MaxFeedbackDiagnostics caps diagnostics per feedback item (top-K).
const MaxFeedbackDiagnostics = 20

// DefaultConstraints apply to every run.
var DefaultConstraints = []string{
	"Work only inside the current directory; it is an isolated git worktree created for this task.",
	"Do not run git commit, git push, git checkout, git reset or git stash; Spine snapshots and integrates your changes.",
	"Do not change .spine/ configuration.",
	"When the task is complete, stop. Your changes are checked by deterministic gates, not by your own report.",
}

// Render produces the prompt text.
func (p Prompt) Render() string {
	var b strings.Builder
	fmt.Fprintf(&b, "spine-task: %s\nspine-attempt: %d\nspine-round: %d\n\n", p.Task, p.Attempt, p.Round)
	b.WriteString("# Task\n\n")
	b.WriteString(strings.TrimSpace(p.Intent))
	b.WriteString("\n")
	if len(p.Scope) > 0 {
		b.WriteString("\n# Scope\n\nYou may only change paths matching:\n")
		for _, s := range p.Scope {
			fmt.Fprintf(&b, "- `%s`\n", s)
		}
	}
	if len(p.Acceptance) > 0 {
		b.WriteString("\n# Acceptance\n\nThe change is accepted only if these checks pass:\n")
		for _, a := range p.Acceptance {
			fmt.Fprintf(&b, "- %s\n", a)
		}
	}
	if len(p.Feedback) > 0 {
		b.WriteString("\n# Feedback from the previous round\n\nYour previous changes are still in the worktree. These checks failed; fix them without breaking anything else.\n")
		for _, f := range p.Feedback {
			fmt.Fprintf(&b, "\n## %s\n", f.Source)
			if f.Hint != "" {
				fmt.Fprintf(&b, "\nHint: %s\n", f.Hint)
			}
			for _, c := range f.Conflicts {
				fmt.Fprintf(&b, "- merge conflict with the current base in `%s`\n", c)
			}
			ds := f.Diagnostics
			if len(ds) > MaxFeedbackDiagnostics {
				ds = ds[:MaxFeedbackDiagnostics]
			}
			for _, d := range ds {
				loc := d.File
				if d.Line > 0 {
					loc = fmt.Sprintf("%s:%d", d.File, d.Line)
				}
				if loc != "" {
					loc = " (" + loc + ")"
				}
				msg := strings.TrimSpace(d.Message)
				if len(msg) > 800 {
					msg = msg[:800] + "…"
				}
				fmt.Fprintf(&b, "- [%s] %s%s: %s\n", d.Severity, d.Code, loc, indent(msg))
			}
			if n := len(f.Diagnostics) - len(ds); n > 0 {
				fmt.Fprintf(&b, "- … and %d more\n", n)
			}
		}
	}
	cs := append(append([]string{}, DefaultConstraints...), p.Constraints...)
	b.WriteString("\n# Constraints\n\n")
	for _, c := range cs {
		fmt.Fprintf(&b, "- %s\n", c)
	}
	return b.String()
}

func indent(s string) string { return strings.ReplaceAll(s, "\n", "\n  ") }

// DescribeGate renders a gate for the Acceptance section.
func DescribeGate(g api.GateRef) string {
	desc := map[string]string{
		"ts.parse":        "every changed file parses",
		"diff.scope":      "only in-scope paths change",
		"diff.nonempty":   "the change is not empty",
		"ts.api_contract": "no exported API is removed or changed, and new imports resolve",
		"go.build":        "`go build ./...` succeeds",
		"go.vet":          "`go vet ./...` is clean",
		"go.test":         "`go test ./...` introduces no new failures",
	}[g.Tool]
	if desc == "" && g.Tool == "cmd.run" {
		if argv, ok := g.Args["argv"].([]any); ok {
			parts := make([]string, len(argv))
			for i, a := range argv {
				parts[i] = fmt.Sprint(a)
			}
			desc = "`" + strings.Join(parts, " ") + "` passes"
		}
	}
	if desc == "" {
		desc = g.Tool
	}
	if g.Policy == api.BaselineRelative {
		desc += " (pre-existing failures are tolerated; new ones are not)"
	}
	return fmt.Sprintf("%s: %s", g.Name, desc)
}
