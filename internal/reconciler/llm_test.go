package reconciler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"

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
