package e2e

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/patcarter883/spine-next/internal/api"
	"github.com/patcarter883/spine-next/internal/inbox"
)

// The LLM supervisor (pointed at a fake Messages API) drives a patch round
// whose prompt carries its hint; an illegal action from the model falls back
// to the rules and is recorded as such.
func TestLLMSupervisorDrivesAndFallsBack(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		text := `{"action":"PatchWorker","reason":"LocalFix","hint":"F must return 2, as the test expects.","focus":[]}`
		if n > 1 {
			text = `{"action":"Replan","reason":"Nope","hint":"","focus":[]}` // not in the legal enum
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"id": "m", "type": "message", "role": "assistant", "model": "claude-opus-5-5",
			"content": []map[string]any{{"type": "text", "text": text}}, "stop_reason": "end_turn",
			"usage": map[string]any{"input_tokens": 1, "output_tokens": 1}})
	}))
	defer srv.Close()
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
	t.Setenv("ANTHROPIC_API_KEY", "test")

	bad := map[string]any{"write": map[string]string{"pkg/g.go": goFile("G"), "pkg/g_test.go": goTest("G", 7)}}
	plan := map[string]map[string]any{"g": {
		"r0": bad,
		// a different failure, so the rules (after the illegal LLM answer)
		// see progress and patch again rather than starting over
		"r1": map[string]any{"write": map[string]string{"pkg/g_test.go": strings.Replace(goTest("G", 7), "TestG(", "TestG2(", 1)}},
		"r2": map[string]any{"write": map[string]string{"pkg/g_test.go": goTest("G", 2)}},
	}}
	h := newHarness(t, plan, 2)
	h.cfg.Supervisor.Type = "llm"
	a := h.open(context.Background())
	defer a.Close()
	stop := runController(t, a)
	defer stop()
	s := spec("add G", "pkg/**")
	inbox.Write(inbox.Dir(a.CommonDir), inbox.Command{Kind: inbox.Submit, Task: "g", Spec: &s})
	tk := waitPhase(t, a, "g", api.PhaseIntegrated, "")
	if tk.Status.PatchRound != 2 {
		t.Fatalf("rounds %d", tk.Status.PatchRound)
	}
	evs, _ := a.Store.ListEvents(context.Background(), "g", 0, 0)
	var sups []string
	for _, e := range evs {
		if e.Kind == "decision" {
			var m map[string]any
			json.Unmarshal(e.Data, &m)
			sups = append(sups, m["supervisor"].(string))
		}
	}
	if len(sups) != 2 || sups[0] != "llm:claude-opus-5-5" || sups[1] != "rules (fallback)" {
		t.Fatalf("decisions by %v", sups)
	}
	b, _ := os.ReadFile(filepath.Join(h.log, "g.log"))
	if !strings.Contains(string(b), "F must return 2") {
		t.Fatal("LLM hint not in the next prompt")
	}
}

// An escalation's proposal is shown on the task; accepting it adds the
// proposed scope and retries with the proposal's text as a hint, and the
// widened task lands a change the old scope would have refused.
func TestAcceptedScopeProposalRetriesWithTheWiderScope(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		text := `{"action":"Escalate","reason":"OutOfScope","hint":"G needs lib","focus":[],"proposal":{"kind":"scope","text":"Record the change in lib/notes.txt too.","scope":["lib/**"]}}`
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"id": "m", "type": "message", "role": "assistant", "model": "claude-opus-5-5",
			"content": []map[string]any{{"type": "text", "text": text}}, "stop_reason": "end_turn",
			"usage": map[string]any{"input_tokens": 1, "output_tokens": 1}})
	}))
	defer srv.Close()
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
	t.Setenv("ANTHROPIC_API_KEY", "test")

	plan := map[string]map[string]any{"g": {
		"a1r0": map[string]any{"write": map[string]string{"pkg/g.go": goFile("G"), "pkg/g_test.go": goTest("G", 7)}},
		"a2r0": map[string]any{"write": map[string]string{"pkg/g.go": goFile("G"), "pkg/g_test.go": goTest("G", 2), "lib/notes.txt": "G\n"}},
	}}
	h := newHarness(t, plan, 2)
	h.cfg.Supervisor.Type = "llm"
	a := h.open(context.Background())
	defer a.Close()
	stop := runController(t, a)
	defer stop()
	s := spec("add G", "pkg/**")
	inbox.Write(inbox.Dir(a.CommonDir), inbox.Command{Kind: inbox.Submit, Task: "g", Spec: &s})
	tk := waitPhase(t, a, "g", api.PhaseEscalated, "OutOfScope")
	if p := tk.Status.Proposal; p == nil || p.Kind != api.ProposeScope || p.Scope[0] != "lib/**" {
		t.Fatalf("proposal %+v", tk.Status.Proposal)
	}
	cmd, err := inbox.Accept(tk, "pat")
	if err != nil {
		t.Fatal(err)
	}
	inbox.Write(inbox.Dir(a.CommonDir), cmd)
	tk = waitPhase(t, a, "g", api.PhaseIntegrated, "")
	if tk.Status.Attempt != 2 || tk.Status.Proposal != nil || strings.Join(tk.Spec.Scope, ",") != "pkg/**,lib/**" {
		t.Fatalf("a%d proposal %+v scope %v", tk.Status.Attempt, tk.Status.Proposal, tk.Spec.Scope)
	}
	if calls.Load() != 1 {
		t.Fatalf("supervisor asked %d times", calls.Load())
	}
	b, _ := os.ReadFile(filepath.Join(h.log, "g.log"))
	if !strings.Contains(string(b), "Record the change in lib/notes.txt too.") {
		t.Fatal("the accepted proposal's text did not reach the retry")
	}
}

// The supervisor is told which files the attempt changed, whether the last
// round changed anything, and what it decided before.
func TestSupervisorSeesTheChangeAndItsEarlierCalls(t *testing.T) {
	var bodies []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		n := len(bodies)
		mu.Unlock()
		text := `{"action":"PatchWorker","reason":"LocalFix","hint":"G must return 2.","focus":[],"proposal":{"kind":"none","text":"","scope":[]}}`
		if n > 1 {
			text = `{"action":"Escalate","reason":"NoProgress","hint":"","focus":[],"proposal":{"kind":"none","text":"","scope":[]}}`
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"id": "m", "type": "message", "role": "assistant", "model": "claude-opus-5-5",
			"content": []map[string]any{{"type": "text", "text": text}}, "stop_reason": "end_turn",
			"usage": map[string]any{"input_tokens": 1, "output_tokens": 1}})
	}))
	defer srv.Close()
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
	t.Setenv("ANTHROPIC_API_KEY", "test")

	bad := map[string]any{"write": map[string]string{"pkg/g.go": goFile("G"), "pkg/g_test.go": goTest("G", 7)}}
	h := newHarness(t, map[string]map[string]any{"g": {"r0": bad, "r1": bad}}, 2)
	h.cfg.Supervisor.Type = "llm"
	a := h.open(context.Background())
	defer a.Close()
	stop := runController(t, a)
	defer stop()
	s := spec("add G", "pkg/**")
	inbox.Write(inbox.Dir(a.CommonDir), inbox.Command{Kind: inbox.Submit, Task: "g", Spec: &s})
	waitPhase(t, a, "g", api.PhaseEscalated, "NoProgress")
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 {
		t.Fatalf("%d decisions", len(bodies))
	}
	for i, want := range [][]string{
		{`\"pkg/g_test.go\"`, `\"tree_unchanged_since_last_round\": false`, `\"earlier_decisions\": null`},
		{`\"pkg/g_test.go\"`, `\"tree_unchanged_since_last_round\": true`, `\"hint\": \"G must return 2.\"`},
	} {
		for _, w := range want {
			if !strings.Contains(bodies[i], w) {
				t.Fatalf("decision %d lacks %s:\n%s", i+1, w, bodies[i])
			}
		}
	}
}
