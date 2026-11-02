package reconciler

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/patcarter883/spine-next/internal/api"
)

// Observation is everything a supervisor may see (RFC §3.5). All typed.
type Observation struct {
	Spec        api.Spec
	Status      api.Status
	Gate        *api.GateSummary
	History     []api.GateSummary // this attempt, oldest first
	Outcome     *api.Outcome
	Conflicts   []string
	Budget      api.Budget
	Legal       []api.ActionKind
	HasBest     bool
	BestIsLast  bool
	FailsOnBase bool // the gate failed on the merge result, not the branch
}

// Supervisor maps an observation to a decision. Only RuleSupervisor exists
// in v2.0; the LLM supervisor (M3) implements the same interface and falls
// back to the rules on error or an illegal action.
type Supervisor interface {
	Decide(obs Observation) api.Decision
}

// legalActions encodes the budget as a closed set (RFC §4.6).
func legalActions(t *api.Task, b api.Budget) []api.ActionKind {
	acts := []api.ActionKind{api.ActEscalate, api.ActAbandon}
	s := t.Status
	if s.Worktree != nil && s.PatchRound < b.MaxPatchRounds {
		acts = append(acts, api.ActPatchWorker)
		if s.BestSnapshot != nil && len(s.Snapshots) > 1 {
			acts = append(acts, api.ActRollback)
		}
	}
	if s.Attempt < b.MaxAttempts {
		acts = append(acts, api.ActRunWorker)
	}
	return acts
}

// RuleSupervisor is the deterministic supervisor (RFC §4.6).
type RuleSupervisor struct{}

func fpKey(g *api.GateSummary) string {
	if g == nil {
		return ""
	}
	return g.FailedGate + "|" + strings.Join(g.Fingerprints, ",")
}

// Decide implements the rule list:
//  0. merge conflict, or a failure that only appears on the merged tree →
//     fresh attempt from the new base, else escalate;
//  1. agent refused or exhausted its own token budget → escalate;
//     crashed, stalled or timed out → patch round (continue the work);
//  2. new-failure count rose since the previous round → roll back to the
//     best snapshot and patch, focusing on the regressed fingerprints;
//  3. the same failure set two rounds running → fresh attempt, else
//     escalate (no progress);
//  4. scope violations → patch with "revert out-of-scope changes";
//  5. otherwise patch with the top new diagnostics;
//  6. nothing legal left → escalate (budget exhausted).
func (RuleSupervisor) Decide(o Observation) api.Decision {
	legal := func(a api.ActionKind) bool { return slices.Contains(o.Legal, a) }
	patchOr := func(reason, hint string, focus []string) api.Decision {
		if legal(api.ActPatchWorker) {
			return api.Decision{Action: api.ActPatchWorker, Reason: reason, Hint: hint, Focus: focus}
		}
		if legal(api.ActRunWorker) {
			return api.Decision{Action: api.ActRunWorker, Reason: reason + "PatchBudgetSpent"}
		}
		return api.Decision{Action: api.ActEscalate, Reason: "BudgetExhausted"}
	}
	if len(o.Conflicts) > 0 || o.FailsOnBase {
		why := "MergeConflict"
		if o.FailsOnBase {
			why = "FailsOnNewBase"
		}
		if legal(api.ActRunWorker) {
			return api.Decision{Action: api.ActRunWorker, Reason: why}
		}
		return api.Decision{Action: api.ActEscalate, Reason: why}
	}
	if o.Outcome != nil && o.Gate == nil {
		switch o.Outcome.Reason {
		case api.OutcomeRefused:
			return api.Decision{Action: api.ActEscalate, Reason: "AgentRefused"}
		case api.OutcomeBudgetExceeded:
			return api.Decision{Action: api.ActEscalate, Reason: "AgentBudgetExceeded"}
		case api.OutcomeCrashed, api.OutcomeStalled, api.OutcomeTimeout, api.OutcomeCancelled:
			return patchOr("Agent"+string(o.Outcome.Reason), "The previous run ended before finishing ("+string(o.Outcome.Reason)+"). Continue the task.", nil)
		}
	}
	g := o.Gate
	if g == nil {
		return patchOr("NoVerdict", "", nil)
	}
	if n := len(o.History); n >= 2 {
		prev := o.History[n-2]
		if g.NewCount > prev.NewCount && legal(api.ActRollback) && o.HasBest && !o.BestIsLast {
			return api.Decision{Action: api.ActRollback, Reason: "Regression", Focus: newOnly(g, &prev),
				Hint: fmt.Sprintf("The last round made things worse (%d new failures, was %d); the worktree was restored to the best earlier state.", g.NewCount, prev.NewCount)}
		}
		if fpKey(g) == fpKey(&prev) {
			if legal(api.ActRunWorker) {
				return api.Decision{Action: api.ActRunWorker, Reason: "NoProgress"}
			}
			return api.Decision{Action: api.ActEscalate, Reason: "NoProgress"}
		}
	}
	if g.FailedGate != "" {
		for _, v := range g.Gates {
			if v.Gate == g.FailedGate && v.Tool == "diff.scope" {
				return patchOr("ScopeCreep", "Revert every change outside the task scope.", g.Fingerprints)
			}
		}
	}
	return patchOr("GateFailed", "", g.Fingerprints)
}

func newOnly(cur, prev *api.GateSummary) []string {
	old := map[string]bool{}
	for _, f := range prev.Fingerprints {
		old[f] = true
	}
	var out []string
	for _, f := range cur.Fingerprints {
		if !old[f] {
			out = append(out, f)
		}
	}
	sort.Strings(out)
	return out
}
