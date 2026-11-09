package agent

import (
	"strings"
	"testing"

	"github.com/patcarter883/spine-next/internal/plan"
	"github.com/patcarter883/spine-next/internal/repoctx"
)

func TestSurveyPromptStatesTheGatesLimits(t *testing.T) {
	for _, tc := range []struct {
		tokens int
		want   string
	}{
		{0, "at most 4800 bytes"},
		{1200, "at most 4800 bytes"},
		{500, "at most 2000 bytes"},
	} {
		out := Prompt{Task: "s", Attempt: 1, Survey: true, NotesTokens: tc.tokens}.Render()
		if !strings.Contains(out, tc.want) || !strings.Contains(out, "wc -c .spine/context.md") {
			t.Errorf("tokens %d: want %q measured with wc -c:\n%s", tc.tokens, tc.want, out)
		}
		if !strings.Contains(out, "a YAML list of at most three strings, never a single string") {
			t.Errorf("tokens %d: caveats are not described as a list:\n%s", tc.tokens, out)
		}
	}
	if repoctx.Tokens(strings.Repeat("x", 4800)) != 1200 || repoctx.Tokens(strings.Repeat("x", 4801)) != 1201 {
		t.Fatal("the stated byte cap no longer matches the gate's token estimate")
	}
}

func TestPlanPromptListsQueuedWork(t *testing.T) {
	out := Prompt{Task: "p", Attempt: 1, Plan: true, Intent: "goal"}.Render()
	if strings.Contains(out, "Work already queued") || !strings.Contains(out, "`after` names only ids in this plan") {
		t.Fatalf("no queued work: %s", out)
	}
	q := []plan.Queued{
		{ID: "fix-policy", Title: "Fix BusinessPolicy", State: "Working", Scope: []string{"app/Policies/**"}},
		{ID: "b-tests", State: "proposed", Plan: "plan-b", After: []string{"fix-policy"}},
	}
	out = Prompt{Task: "p", Attempt: 1, Plan: true, Intent: "goal", Queued: q}.Render()
	for _, want := range []string{"# Work already queued", "- `fix-policy` (Working): Fix BusinessPolicy; scope: app/Policies/**",
		"- `b-tests` (proposed by plan plan-b, not submitted yet); scope: everything; after: fix-policy", "`after` names ids in this plan or queued tasks"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestPromptsAskForOutcomesAndCheckedClaims(t *testing.T) {
	planner := Prompt{Task: "p", Attempt: 1, Plan: true, Intent: "goal"}.Render()
	for _, want := range []string{"Write each intent as the outcome to reach", "verify every fact you state", "`\"tests_only\": true`", "\"tests_only\": false"} {
		if !strings.Contains(planner, want) {
			t.Errorf("planner prompt lacks %q", want)
		}
	}
	work := Prompt{Task: "w", Attempt: 1, Intent: "add tests"}.Render()
	if !strings.Contains(work, "trust the code") || strings.Contains(work, TestsOnlyConstraint) {
		t.Errorf("work prompt:\n%s", work)
	}
	if tests := (Prompt{Task: "w", Attempt: 1, Intent: "add tests", TestsOnly: true}).Render(); !strings.Contains(tests, TestsOnlyConstraint) {
		t.Errorf("tests-only prompt lacks its constraint:\n%s", tests)
	}
}
