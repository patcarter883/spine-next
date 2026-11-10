package reconciler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2"

	"github.com/patcarter883/spine-next/internal/agent"
	"github.com/patcarter883/spine-next/internal/api"
)

const llmMaxTokens = 16000

// LLMSupervisor asks a model to choose the next action (RFC §3.5, M3): Claude
// through the Anthropic API, or a provider from Pi's models.json. It is a
// direct API call with no tools, not a coding agent. The
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
	Effort  string        // low|medium|high|xhigh|max (default medium; Anthropic API only)
	Timeout time.Duration // per decision (default 2m)
	// Provider, when set, sends decisions to LoadProvider's server instead of
	// the Anthropic API. It is loaded per decision, so a models.json edit
	// applies to the next one, and a failure falls back like any other.
	Provider     string
	LoadProvider func() (agent.PiProvider, error)
}

// Name identifies the supervisor in decision events.
func (s *LLMSupervisor) Name() string {
	if s.Provider != "" {
		return "llm:" + s.Provider + "/" + s.model()
	}
	return "llm:" + s.model()
}

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

Use the counts and diagnostics; prefer the cheapest action likely to succeed. files_changed lists what the change touched, and each diagnostic's in_change says whether its file is one of them. A failure outside those files can still be the change's doing (a redeclared helper, a broken import, a renamed symbol), so read the output before blaming the base. tree_unchanged_since_last_round means the agent's last round changed nothing; another patch with the same feedback rarely helps. earlier_decisions are your previous calls on this attempt and hints_from_people what a person told the agent: do not repeat a hint that has already failed. When a gate failed without diagnostics, failed_gate_output_tail is the end of its output: read it before deciding. Call a failure pre-existing, flaky or infrastructure only when that output (or the diagnostics) shows it, for example a missing service, or the same failure on code the change neither touched nor calls; a crash in code the task touched, or output you cannot explain, is the agent's to fix. "hint" is one or two sentences of concrete guidance for the agent (empty if none); never claim the work passed. "focus" lists fingerprints from the new diagnostics the agent should fix first (may be empty). "reason" is a short CamelCase code such as Regression, NoProgress, LocalFix, Underspecified.

"proposal" applies only when you Escalate: what a person should do next, which they accept with one click. Kind "hint" resumes the agent with text as the hint; "retry" starts a fresh attempt with text as its hint; "scope" adds the globs in scope to the task's scope and retries (text says why), for work that cannot be done inside the current scope. Propose only a concrete step you expect to resolve the escalation; otherwise, or when not escalating, use kind "none".`

type llmDecision struct {
	Action   string   `json:"action"`
	Reason   string   `json:"reason"`
	Hint     string   `json:"hint"`
	Focus    []string `json:"focus"`
	Proposal struct {
		Kind  string   `json:"kind"`
		Text  string   `json:"text"`
		Scope []string `json:"scope"`
	} `json:"proposal"`
}

// observationJSON renders what the model may see, bounded in size.
func observationJSON(o Observation, suggestion api.Decision) ([]byte, error) {
	type diag struct {
		Gate, Code, File, Message, Fingerprint string
		Line                                   int  `json:",omitempty"`
		InChange                               bool `json:"in_change"`
	}
	type earlier struct {
		Round  int    `json:"round"`
		Action string `json:"action"`
		Reason string `json:"reason"`
		Hint   string `json:"hint,omitempty"`
	}
	changed := map[string]bool{}
	for _, f := range o.ChangedFiles {
		changed[f] = true
	}
	type round struct {
		Round      int    `json:"round"`
		Passed     bool   `json:"passed"`
		FailedGate string `json:"failed_gate,omitempty"`
		NewCount   int    `json:"new_failures"`
	}
	out := map[string]any{
		"intent":                          trunc(o.Spec.Intent, 4000),
		"scope":                           o.Spec.Scope,
		"attempt":                         o.Status.Attempt,
		"max_attempts":                    o.Budget.MaxAttempts,
		"round":                           o.Status.PatchRound,
		"max_rounds":                      o.Budget.MaxPatchRounds,
		"tokens_used":                     o.Status.TokensUsed,
		"legal_actions":                   o.Legal,
		"rule_suggestion":                 map[string]any{"action": suggestion.Action, "reason": suggestion.Reason},
		"merge_conflicts":                 o.Conflicts,
		"fails_only_on_new_base":          o.FailsOnBase,
		"best_snapshot_available":         o.HasBest && !o.BestIsLast,
		"files_changed_count":             len(o.ChangedFiles),
		"files_changed":                   o.ChangedFiles[:min(len(o.ChangedFiles), 60)],
		"tree_unchanged_since_last_round": o.TreeUnchanged,
	}
	var ed []earlier
	for _, e := range o.Earlier {
		ed = append(ed, earlier{Round: e.Round, Action: string(e.Action), Reason: e.Reason, Hint: trunc(e.Hint, 300)})
	}
	out["earlier_decisions"] = ed
	var people []string
	for _, h := range o.Status.Hints[max(0, len(o.Status.Hints)-5):] {
		people = append(people, trunc(h.Text, 500))
	}
	out["hints_from_people"] = people
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
				ds = append(ds, diag{Gate: v.Gate, Code: d.Code, File: d.File, Line: d.Line, Message: trunc(d.Message, 600), Fingerprint: d.Fingerprint, InChange: changed[d.File]})
			}
		}
		out["failed_gate"] = g.FailedGate
		out["new_failure_count"] = g.NewCount
		out["new_diagnostics"] = ds
		for _, v := range g.Gates {
			if v.Gate == g.FailedGate && v.OutputTail != "" {
				out["failed_gate_reason"] = v.Reason
				out["failed_gate_output_tail"] = trunc(v.OutputTail, 3000)
			}
		}
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
	kinds := []any{"none", string(api.ProposeHint), string(api.ProposeRetry)}
	if len(o.Spec.Scope) > 0 {
		kinds = append(kinds, string(api.ProposeScope))
	}
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{"type": "string", "enum": legal},
			"reason": map[string]any{"type": "string"},
			"hint":   map[string]any{"type": "string"},
			"focus":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"proposal": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"kind":  map[string]any{"type": "string", "enum": kinds},
					"text":  map[string]any{"type": "string"},
					"scope": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				},
				"required":             []string{"kind", "text", "scope"},
				"additionalProperties": false,
			},
		},
		"required":             []string{"action", "reason", "hint", "focus", "proposal"},
		"additionalProperties": false,
	}
	user := "Task state (JSON):\n" + string(obs) + "\n\nChoose the next action."
	complete := s.completeAnthropic
	if s.Provider != "" {
		complete = s.completeProvider
	}
	text, err := complete(ctx, user, schema)
	if err != nil {
		return api.Decision{}, fmt.Errorf("llm supervisor: %w", err)
	}
	var d llmDecision
	if err := json.Unmarshal([]byte(text), &d); err != nil {
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
	dec := api.Decision{Action: api.ActionKind(d.Action), Reason: d.Reason, Hint: d.Hint, Focus: focus, Proposal: proposal(d)}
	if dec.Reason == "" {
		dec.Reason = "LLM"
	}
	return dec, nil
}

// proposal keeps a model's proposal only when it names something to do.
func proposal(d llmDecision) *api.Proposal {
	p := api.Proposal{Kind: api.ProposalKind(d.Proposal.Kind), Text: trunc(strings.TrimSpace(d.Proposal.Text), 2000)}
	switch p.Kind {
	case api.ProposeHint:
		if p.Text == "" {
			return nil
		}
	case api.ProposeRetry:
	case api.ProposeScope:
		for _, g := range d.Proposal.Scope {
			if g = strings.TrimSpace(g); g != "" {
				p.Scope = append(p.Scope, g)
			}
		}
		if len(p.Scope) == 0 {
			return nil
		}
	default:
		return nil
	}
	return &p
}

func (s *LLMSupervisor) completeAnthropic(ctx context.Context, user string, schema map[string]any) (string, error) {
	effort := anthropic.OutputConfigEffort(s.Effort)
	if effort == "" {
		effort = anthropic.OutputConfigEffortMedium
	}
	resp, err := s.Client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     s.model(),
		MaxTokens: llmMaxTokens,
		System:    []anthropic.TextBlockParam{{Text: llmSystem}},
		Messages:  []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(user))},
		OutputConfig: anthropic.OutputConfigParam{
			Effort: effort,
			Format: anthropic.JSONOutputFormatParam{Schema: schema},
		},
	})
	if err != nil {
		return "", err
	}
	switch resp.StopReason {
	case anthropic.StopReasonEndTurn:
	case anthropic.StopReasonRefusal:
		return "", errors.New("model refused")
	default:
		return "", fmt.Errorf("stop reason %q", resp.StopReason)
	}
	var text strings.Builder
	for _, b := range resp.Content {
		if t, ok := b.AsAny().(anthropic.TextBlock); ok {
			text.WriteString(t.Text)
		}
	}
	return text.String(), nil
}

// completeProvider calls an OpenAI-compatible chat completions endpoint with
// the decision schema as a structured output.
func (s *LLMSupervisor) completeProvider(ctx context.Context, user string, schema map[string]any) (string, error) {
	if s.LoadProvider == nil {
		return "", fmt.Errorf("provider %q is not configured", s.Provider)
	}
	p, err := s.LoadProvider()
	if err != nil {
		return "", err
	}
	key, err := p.ResolveKey()
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(map[string]any{
		"model":      s.model(),
		"max_tokens": llmMaxTokens,
		"stream":     false,
		"messages": []map[string]string{
			{"role": "system", "content": llmSystem},
			{"role": "user", "content": user},
		},
		"response_format": map[string]any{"type": "json_schema",
			"json_schema": map[string]any{"name": "decision", "strict": true, "schema": schema}},
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(p.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("provider %s: HTTP %d: %s", s.Provider, resp.StatusCode, trunc(string(raw), 300))
	}
	var out struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content string `json:"content"`
				Refusal string `json:"refusal"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("provider %s: bad response: %w", s.Provider, err)
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("provider %s: no choices in the response", s.Provider)
	}
	c := out.Choices[0]
	if c.Message.Refusal != "" {
		return "", errors.New("model refused")
	}
	if c.FinishReason != "stop" {
		return "", fmt.Errorf("provider %s: finish reason %q", s.Provider, c.FinishReason)
	}
	return c.Message.Content, nil
}
