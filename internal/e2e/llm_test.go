package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
