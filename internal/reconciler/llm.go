package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2"

	"github.com/patcarter883/spine-next/internal/api"
)

// LLMSupervisor asks Claude to choose the next action (RFC §3.5, M3). The
// response is constrained by structured outputs to a JSON object whose
// action enum is exactly Observation.Legal, so the model cannot name an
// action the budget forbids; the reconciler re-checks legality anyway and
// falls back to RuleSupervisor on any error, refusal or invalid output.
//
// The model sees only typed data: the spec, budget, counts, the new
// (baseline-subtracted) diagnostics and the rule supervisor's suggestion.
// It never writes status, and Verify/Merge are not actions it can choose.
type LLMSupervisor struct {
	Client  anthropic.Client
	Model   string        // default claude-opus-5-5
	Effort  string        // low|medium|high|xhigh|max (default medium)
	Timeout time.Duration // per decision (default 2m)
}

// Name identifies the supervisor in decision events.
func (s *LLMSupervisor) Name() string { return "llm:" + s.model() }

func (s *LLMSupervisor) model() string {
	if s.Model == "" {
		return "claude-opus-5-5"
	}
	return s.Model
}

const llmSystem = `You are the supervisor of an automated coding pipeline. A coding agent works on a task in an isolated git worktree; deterministic gates (build, tests, linters, API checks) decide whether its work passes. The latest round did not pass. Choose the single next action from the legal set:

- PatchWorker: run the agent again in the same worktree with the new failures as feedback. Best when failures are local and the agent is making progress.
- Rollback: restore the best earlier snapshot of this attempt, then patch. Best when the last round regressed (more new failures than before).
- RunWorker: discard the worktree and start a fresh attempt from the current base. Best when the approach is stuck (the same failures repeat), the base moved under the task, or the change conflicts.
- Escalate: hand the task to a human with a short reason. Best when the task looks underspecified, impossible within its scope, or the failures are outside the agent's control (infrastructure, flaky tests, pre-existing breakage).
- Abandon: mark the task failed. Only when continuing is clearly pointless.

Use the counts and diagnostics; prefer the cheapest action likely to succeed. "hint" is one or two sentences of concrete guidance for the agent (empty if none); never claim the work passed. "focus" lists fingerprints from the new diagnostics the agent should fix first (may be empty). "reason" is a short CamelCase code such as Regression, NoProgress, LocalFix, Underspecified.`

type llmDecision struct {
	Action string   `json:"action"`
	Reason string   `json:"reason"`
	Hint   string   `json:"hint"`
	Focus  []string `json:"focus"`
}

// observationJSON renders what the model may see, bounded in size.
func observationJSON(o Observation, suggestion api.Decision) ([]byte, error) {
	type diag struct {
		Gate, Code, File, Message, Fingerprint string
		Line                                   int `json:",omitempty"`
	}
	type round struct {
		Round      int    `json:"round"`
		Passed     bool   `json:"passed"`
		FailedGate string `json:"failed_gate,omitempty"`
		NewCount   int    `json:"new_failures"`
	}
	out := map[string]any{
		"intent":                  trunc(o.Spec.Intent, 4000),
		"scope":                   o.Spec.Scope,
		"attempt":                 o.Status.Attempt,
		"max_attempts":            o.Budget.MaxAttempts,
		"round":                   o.Status.PatchRound,
		"max_rounds":              o.Budget.MaxPatchRounds,
		"tokens_used":             o.Status.TokensUsed,
		"legal_actions":           o.Legal,
		"rule_suggestion":         map[string]any{"action": suggestion.Action, "reason": suggestion.Reason},
		"merge_conflicts":         o.Conflicts,
		"fails_only_on_new_base":  o.FailsOnBase,
		"best_snapshot_available": o.HasBest && !o.BestIsLast,
	}
	if o.Outcome != nil {
		out["last_agent_run"] = map[string]any{"result": o.Outcome.Reason, "stop_reason": o.Outcome.StopReason,
			"final_message": trunc(o.Outcome.Message, 1000)}
	}
	var hist []round
	for _, h := range o.History {
		hist = append(hist, round{Round: h.Round, Passed: h.Passed, FailedGate: h.FailedGate, NewCount: h.NewCount})
	}
	out["rounds_this_attempt"] = hist
	if g := o.Gate; g != nil {
		var ds []diag
		for _, v := range g.Gates {
			for _, d := range v.New {
				if len(ds) >= 30 {
					break
				}
				ds = append(ds, diag{Gate: v.Gate, Code: d.Code, File: d.File, Line: d.Line, Message: trunc(d.Message, 600), Fingerprint: d.Fingerprint})
			}
		}
		out["failed_gate"] = g.FailedGate
		out["new_failure_count"] = g.NewCount
		out["new_diagnostics"] = ds
	}
	return json.MarshalIndent(out, "", " ")
}

// Decide implements Supervisor.
func (s *LLMSupervisor) Decide(ctx context.Context, o Observation) (api.Decision, error) {
	suggestion := RuleSupervisor{}.decide(o)
	obs, err := observationJSON(o, suggestion)
	if err != nil {
		return api.Decision{}, err
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	legal := make([]any, len(o.Legal))
	for i, a := range o.Legal {
		legal[i] = string(a)
	}
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{"type": "string", "enum": legal},
			"reason": map[string]any{"type": "string"},
			"hint":   map[string]any{"type": "string"},
			"focus":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		},
		"required":             []string{"action", "reason", "hint", "focus"},
		"additionalProperties": false,
	}
	effort := anthropic.OutputConfigEffort(s.Effort)
	if effort == "" {
		effort = anthropic.OutputConfigEffortMedium
	}
	resp, err := s.Client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     s.model(),
		MaxTokens: 16000,
		System:    []anthropic.TextBlockParam{{Text: llmSystem}},
		Messages: []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(
			"Task state (JSON):\n" + string(obs) + "\n\nChoose the next action.")),
		},
		OutputConfig: anthropic.OutputConfigParam{
			Effort: effort,
			Format: anthropic.JSONOutputFormatParam{Schema: schema},
		},
	})
	if err != nil {
		return api.Decision{}, fmt.Errorf("llm supervisor: %w", err)
	}
	switch resp.StopReason {
	case anthropic.StopReasonEndTurn:
	case anthropic.StopReasonRefusal:
		return api.Decision{}, errors.New("llm supervisor: model refused")
	default:
		return api.Decision{}, fmt.Errorf("llm supervisor: stop reason %q", resp.StopReason)
	}
	var text strings.Builder
	for _, b := range resp.Content {
		if t, ok := b.AsAny().(anthropic.TextBlock); ok {
			text.WriteString(t.Text)
		}
	}
	var d llmDecision
	if err := json.Unmarshal([]byte(text.String()), &d); err != nil {
		return api.Decision{}, fmt.Errorf("llm supervisor: output is not the decision schema: %w", err)
	}
	known := map[string]bool{}
	if o.Gate != nil {
		for _, f := range o.Gate.Fingerprints {
			known[f] = true
		}
	}
	var focus []string
	for _, f := range d.Focus {
		if known[f] {
			focus = append(focus, f)
		}
	}
	dec := api.Decision{Action: api.ActionKind(d.Action), Reason: d.Reason, Hint: d.Hint, Focus: focus}
	if dec.Reason == "" {
		dec.Reason = "LLM"
	}
	return dec, nil
}
