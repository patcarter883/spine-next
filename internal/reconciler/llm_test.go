package reconciler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"

	"github.com/patcarter883/spine-next/internal/agent"
	"github.com/patcarter883/spine-next/internal/api"
)

// fakeAPI answers POST /v1/messages with the given text (or status code)
// and records the last request body.
func fakeAPI(t *testing.T, status int, text, stop string) (*LLMSupervisor, *[]byte) {
	var last []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		last, _ = io.ReadAll(r.Body)
		if status != 200 {
			http.Error(w, `{"type":"error","error":{"type":"api_error","message":"boom"}}`, status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-opus-5-5",
			"content":     []map[string]any{{"type": "text", "text": text}},
			"stop_reason": stop, "usage": map[string]any{"input_tokens": 10, "output_tokens": 5},
		})
	}))
	t.Cleanup(srv.Close)
	c := anthropic.NewClient(option.WithBaseURL(srv.URL), option.WithAPIKey("test"), option.WithMaxRetries(0))
	return &LLMSupervisor{Client: c}, &last
}

func obs() Observation {
	return Observation{
		Spec:   api.Spec{Intent: "fix it"},
		Status: api.Status{Attempt: 1, PatchRound: 1},
		Budget: api.Budget{MaxAttempts: 2, MaxPatchRounds: 3},
		Legal:  []api.ActionKind{api.ActEscalate, api.ActAbandon, api.ActPatchWorker, api.ActRunWorker},
		Gate: &api.GateSummary{FailedGate: "unit", NewCount: 1, Fingerprints: []string{"fp1"},
			Gates: []api.GateVerdict{{Gate: "unit", Tool: "go.test", ExitCode: 1, New: []api.Diagnostic{{Code: "x.TestA", Fingerprint: "fp1", Message: "boom"}}}}},
	}
}

func TestLLMSupervisorDecision(t *testing.T) {
	s, last := fakeAPI(t, 200, `{"action":"RunWorker","reason":"NoProgress","hint":"try another approach","focus":["fp1","bogus"]}`, "end_turn")
	d, err := s.Decide(context.Background(), obs())
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != api.ActRunWorker || d.Hint != "try another approach" || len(d.Focus) != 1 || d.Focus[0] != "fp1" {
		t.Fatalf("%+v", d)
	}
	// The request constrains the action enum to the legal set and sends the
	// typed observation, the default model and medium effort.
	var req map[string]any
	json.Unmarshal(*last, &req)
	if req["model"] != "claude-opus-5-5" {
		t.Fatalf("model %v", req["model"])
	}
	oc, _ := req["output_config"].(map[string]any)
	if oc["effort"] != "medium" {
		t.Fatalf("effort %v", oc)
	}
	enum := oc["format"].(map[string]any)["schema"].(map[string]any)["properties"].(map[string]any)["action"].(map[string]any)["enum"].([]any)
	if len(enum) != 4 || enum[0] != "Escalate" {
		t.Fatalf("enum %v", enum)
	}
	if !strings.Contains(string(*last), "x.TestA") || !strings.Contains(string(*last), "rule_suggestion") {
		t.Fatalf("observation not sent: %s", *last)
	}
}

func TestLLMSupervisorErrors(t *testing.T) {
	for name, c := range map[string]struct {
		status int
		text   string
		stop   string
	}{
		"http 500":   {500, "", ""},
		"refusal":    {200, "", "refusal"},
		"max tokens": {200, `{"action":`, "max_tokens"},
		"not json":   {200, "PatchWorker please", "end_turn"},
	} {
		s, _ := fakeAPI(t, c.status, c.text, c.stop)
		if _, err := s.Decide(context.Background(), obs()); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func providerSupervisor(t *testing.T, status int, resp string) (*LLMSupervisor, *[]byte, *http.Header) {
	var last []byte
	hdr := &http.Header{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path %s", r.URL.Path)
		}
		last, _ = io.ReadAll(r.Body)
		*hdr = r.Header.Clone()
		w.WriteHeader(status)
		io.WriteString(w, resp)
	}))
	t.Cleanup(srv.Close)
	return &LLMSupervisor{Provider: "local", Model: "m", LoadProvider: func() (agent.PiProvider, error) {
		return agent.PiProvider{Name: "local", BaseURL: srv.URL + "/v1/", API: agent.PiAPIOpenAI, APIKey: "k"}, nil
	}}, &last, hdr
}

func openAIReply(content, finish string) string {
	b, _ := json.Marshal(map[string]any{"choices": []map[string]any{{"finish_reason": finish, "message": map[string]any{"content": content}}}})
	return string(b)
}

func TestLLMSupervisorProvider(t *testing.T) {
	s, last, hdr := providerSupervisor(t, 200, openAIReply(`{"action":"RunWorker","reason":"NoProgress","hint":"h","focus":["fp1","bogus"]}`, "stop"))
	if s.Name() != "llm:local/m" {
		t.Fatal(s.Name())
	}
	d, err := s.Decide(context.Background(), obs())
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != api.ActRunWorker || len(d.Focus) != 1 || hdr.Get("Authorization") != "Bearer k" {
		t.Fatalf("%+v %v", d, hdr)
	}
	var req struct {
		Model          string
		ResponseFormat struct {
			JSONSchema struct {
				Schema struct {
					Properties struct {
						Action struct{ Enum []string }
					}
				}
			} `json:"json_schema"`
		} `json:"response_format"`
	}
	if err := json.Unmarshal(*last, &req); err != nil {
		t.Fatal(err)
	}
	if req.Model != "m" || len(req.ResponseFormat.JSONSchema.Schema.Properties.Action.Enum) != len(obs().Legal) {
		t.Fatalf("request: %s", *last)
	}
}

func TestLLMSupervisorProviderFailures(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		resp   string
		want   string
	}{
		"http":      {500, "boom", "HTTP 500"},
		"bad json":  {200, "<html>", "bad response"},
		"no choice": {200, `{"choices":[]}`, "no choices"},
		"truncated": {200, openAIReply(`{"action":`, "length"), "finish reason"},
		"not json":  {200, openAIReply("Escalate it", "stop"), "decision schema"},
	} {
		s, _, _ := providerSupervisor(t, tc.status, tc.resp)
		if _, err := s.Decide(context.Background(), obs()); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	s, _, _ := providerSupervisor(t, 200, "")
	s.LoadProvider = func() (agent.PiProvider, error) {
		return agent.PiProvider{Name: "local", BaseURL: "http://127.0.0.1:1", APIKey: "!touch /tmp/pwned"}, nil
	}
	if _, err := s.Decide(context.Background(), obs()); err == nil || !strings.Contains(err.Error(), "won't run") {
		t.Errorf("command key: %v", err)
	}
}

func TestUnparsedFailureOutputReachesSupervisorAndAgent(t *testing.T) {
	o := obs()
	o.Gate = &api.GateSummary{FailedGate: "pest", Gates: []api.GateVerdict{
		{Gate: "pint", Tool: "cmd.run"},
		{Gate: "pest", Tool: "cmd.run", ExitCode: 1, Reason: "UnparsedFailure", OutputTail: "PHP Fatal error: Cannot redeclare iaSowing()"},
	}}
	b, err := observationJSON(o, api.Decision{})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(b, &m)
	if m["failed_gate_output_tail"] != "PHP Fatal error: Cannot redeclare iaSowing()" || m["failed_gate_reason"] != "UnparsedFailure" {
		t.Fatalf("observation: %s", b)
	}
	fb := feedback(o, api.Decision{})
	if len(fb) != 1 || fb[0].Output != "PHP Fatal error: Cannot redeclare iaSowing()" {
		t.Fatalf("feedback: %+v", fb)
	}
	out := agent.Prompt{Task: "t", Attempt: 1, Round: 2, Intent: "x", Feedback: fb}.Render()
	if !strings.Contains(out, "The end of its output:\n\n```\nPHP Fatal error: Cannot redeclare iaSowing()\n```") {
		t.Fatalf("prompt:\n%s", out)
	}
}

// An escalation carries the model's proposal; "none", or a proposal with
// nothing to do, carries none. The model sees what the change touched,
// whether each failure is in it, whether the last round changed anything,
// and its earlier calls; "scope" is offered only to a task with a scope.
func TestLLMSupervisorProposalAndChangeContext(t *testing.T) {
	o := obs()
	o.Spec.Scope = []string{"pkg/**"}
	o.Gate.Gates[0].New[0].File = "pkg/a.go"
	o.ChangedFiles = []string{"pkg/a.go"}
	o.TreeUnchanged = true
	o.Earlier = []EarlierDecision{{Round: 0, Decision: api.Decision{Action: api.ActPatchWorker, Reason: "LocalFix", Hint: "look at pkg/a.go"}}}
	s, last := fakeAPI(t, 200, `{"action":"Escalate","reason":"OutOfScope","hint":"needs lib","focus":[],"proposal":{"kind":"scope","text":"A calls into lib.","scope":[" lib/** ",""]}}`, "end_turn")
	d, err := s.Decide(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if p := d.Proposal; p == nil || p.Kind != api.ProposeScope || len(p.Scope) != 1 || p.Scope[0] != "lib/**" || p.Text != "A calls into lib." {
		t.Fatalf("proposal %+v", d.Proposal)
	}
	var req struct {
		Messages []struct {
			Content []struct{ Text string } `json:"content"`
		} `json:"messages"`
		OutputConfig struct {
			Format struct {
				Schema struct {
					Properties struct {
						Proposal struct {
							Properties struct {
								Kind struct{ Enum []string } `json:"kind"`
							} `json:"properties"`
						} `json:"proposal"`
					} `json:"properties"`
				} `json:"schema"`
			} `json:"format"`
		} `json:"output_config"`
	}
	if err := json.Unmarshal(*last, &req); err != nil {
		t.Fatal(err)
	}
	if k := req.OutputConfig.Format.Schema.Properties.Proposal.Properties.Kind.Enum; !slices.Equal(k, []string{"none", "hint", "retry", "scope"}) {
		t.Fatalf("proposal kinds %v", k)
	}
	body := req.Messages[0].Content[0].Text
	for _, want := range []string{`"files_changed": [`, `"pkg/a.go"`, `"in_change": true`, `"tree_unchanged_since_last_round": true`, `"hint": "look at pkg/a.go"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("observation lacks %s:\n%s", want, body)
		}
	}

	o.Spec.Scope = nil
	for _, reply := range []string{
		`{"action":"Escalate","reason":"X","hint":"","focus":[],"proposal":{"kind":"none","text":"","scope":[]}}`,
		`{"action":"Escalate","reason":"X","hint":"","focus":[],"proposal":{"kind":"hint","text":"  ","scope":[]}}`,
		`{"action":"Escalate","reason":"X","hint":"","focus":[],"proposal":{"kind":"scope","text":"x","scope":[" "]}}`,
	} {
		s, last := fakeAPI(t, 200, reply, "end_turn")
		d, err := s.Decide(context.Background(), o)
		if err != nil || d.Proposal != nil {
			t.Fatalf("%s: %+v %v", reply, d.Proposal, err)
		}
		if !strings.Contains(string(*last), `"enum":["none","hint","retry"]`) {
			t.Fatalf("scope offered to a whole-repository task: %s", *last)
		}
	}
}
