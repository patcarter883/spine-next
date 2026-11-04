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
	// Plan asks for a task breakdown of Intent (a goal) written to PlanFile,
	// instead of a code change.
	Plan bool
	// Agents and Gates list what a planned task may name (planner only).
	Agents []string
	Gates  []string
	// Notes is the repository's .spine/context.md (already capped), for
	// adapters with context on. Modules is the rendered module map
	// (planners only).
	Notes   string
	Modules string
	// Survey asks for repository context: labels and caveats in the
	// module map Spine derived, and a capped context.md.
	Survey      bool
	NotesTokens int
}

// PlanFile is where a planner run writes its plan, relative to the worktree.
const PlanFile = "spine-plan.json"

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
	if p.Plan {
		return p.renderPlan()
	}
	if p.Survey {
		return p.renderSurvey()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "spine-task: %s\nspine-attempt: %d\nspine-round: %d\n\n", p.Task, p.Attempt, p.Round)
	b.WriteString("# Task\n\n")
	b.WriteString(strings.TrimSpace(p.Intent))
	b.WriteString("\n")
	if p.Notes != "" {
		b.WriteString("\n# Repository notes\n\n")
		b.WriteString(p.Notes)
		b.WriteString("\n")
	}
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

func (p Prompt) renderPlan() string {
	var b strings.Builder
	fmt.Fprintf(&b, "spine-task: %s\nspine-attempt: %d\nspine-round: %d\nspine-kind: plan\n\n", p.Task, p.Attempt, p.Round)
	b.WriteString("# Goal\n\n")
	b.WriteString(strings.TrimSpace(p.Intent))
	b.WriteString("\n\n# What to do\n\n")
	b.WriteString("Do not implement the goal. Read the repository and break the goal into small, independently verifiable tasks that coding agents will carry out one each, in isolated worktrees. ")
	b.WriteString("Each task must be reviewable on its own and leave the repository building and its tests passing. Prefer tasks that touch disjoint files so they can run in parallel; when one task needs another's change, list it in `after`.\n\n")
	fmt.Fprintf(&b, "Write the plan as JSON to `%s` in the current directory, and change nothing else:\n\n", PlanFile)
	b.WriteString("```json\n{\n  \"notes\": \"optional: assumptions, risks, what you left out\",\n  \"tasks\": [\n    {\n      \"id\": \"short-kebab-id\",\n      \"title\": \"commit subject, under 72 characters\",\n      \"intent\": \"precise instructions for the coding agent: what to change, where, and how to test it\",\n      \"scope\": [\"path/globs/**\", \"the task may change\"],\n      \"after\": [\"ids of tasks that must land first\"]\n    }\n  ]\n}\n```\n\n")
	if p.Notes != "" {
		b.WriteString("# Repository notes\n\n" + p.Notes + "\n\n")
	}
	if p.Modules != "" {
		b.WriteString("# Module map (derived from the code and its history)\n\n" + p.Modules + "\n")
		b.WriteString("Use module boundaries for scopes, include a module's tests in the scope of a task that changes it, and put modules that usually change together in one task or order them with `after`.\n\n")
	}
	b.WriteString("Rules: ids match `[a-z0-9][a-z0-9-]{0,40}` and are unique; `after` names only ids in this plan; there are no cycles; every task has a scope; 1 to 12 tasks.\n")
	if len(p.Agents) > 0 {
		fmt.Fprintf(&b, "A task may set `\"agent\"` to one of: %s (omit it for the default).\n", strings.Join(p.Agents, ", "))
	}
	if len(p.Gates) > 0 {
		fmt.Fprintf(&b, "\n# How tasks are checked\n\nEvery task must pass these gates, read from the base branch:\n")
		for _, g := range p.Gates {
			fmt.Fprintf(&b, "- %s\n", g)
		}
	}
	if len(p.Feedback) > 0 {
		b.WriteString("\n# Problems with your previous plan\n\n")
		for _, f := range p.Feedback {
			fmt.Fprintf(&b, "- %s: %s\n", f.Source, f.Hint)
			for _, d := range f.Diagnostics {
				fmt.Fprintf(&b, "  - %s\n", d.Message)
			}
		}
	}
	b.WriteString("\n# Constraints\n\n- Work only inside the current directory.\n- Do not run git commit, git push, git checkout, git reset or git stash.\n")
	fmt.Fprintf(&b, "- Write only `%s`. When it is written, stop.\n", PlanFile)
	return b.String()
}

func (p Prompt) renderSurvey() string {
	var b strings.Builder
	fmt.Fprintf(&b, "spine-task: %s\nspine-attempt: %d\nspine-round: %d\nspine-kind: survey\n\n", p.Task, p.Attempt, p.Round)
	b.WriteString("# Task\n\nDescribe this repository for coding agents that will work in it. Write two files and change nothing else.\n\n")
	b.WriteString("1. `.spine/modules.yaml` was derived from the code and its git history. For each module, set `label` (what it is, at most ten words) and, only where there is a real pitfall, `caveats` (at most two short sentences each: generated code, ordering constraints, files that must change together, slow tests). Do not change any other field, and do not add or remove modules.\n")
	words := p.NotesTokens * 3 / 4
	if words <= 0 {
		words = 900
	}
	fmt.Fprintf(&b, "2. `.spine/context.md`: a short factual summary, at most about %d words. Facts only, as terse bullet lists under these headings: Build and test (the exact commands, including how to run one package's or file's tests), Layout (where things live), Conventions (naming, errors, logging, formatting the gates do not already enforce), No-go areas (generated files, vendored code, files never to edit). Verify each command by running it. No tutorials, no history, no restating what the code says.\n", words)
	if strings.TrimSpace(p.Intent) != "" {
		b.WriteString("\n# Focus\n\n" + strings.TrimSpace(p.Intent) + "\n")
	}
	if p.Notes != "" {
		b.WriteString("\n# The current context.md (update it; keep what is still true)\n\n" + p.Notes + "\n")
	}
	if len(p.Feedback) > 0 {
		b.WriteString("\n# Problems with your previous attempt\n\n")
		for _, f := range p.Feedback {
			if f.Hint != "" {
				fmt.Fprintf(&b, "- %s: %s\n", f.Source, f.Hint)
			}
			for _, d := range f.Diagnostics {
				fmt.Fprintf(&b, "- %s\n", d.Message)
			}
		}
	}
	b.WriteString("\n# Constraints\n\n- Work only inside the current directory.\n- Do not run git commit, git push, git checkout, git reset or git stash.\n- Write only `.spine/modules.yaml` (labels and caveats) and `.spine/context.md`. When they are written, stop.\n")
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
