package e2e

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/patcarter883/spine-next/internal/agent"
	"github.com/patcarter883/spine-next/internal/api"
	"github.com/patcarter883/spine-next/internal/inbox"
)

// A Pi (RPC) adapter through the whole controller: round 0 fails a gate,
// round 1 receives the failing test as feedback and fixes it; the run's
// tokens are recorded (the fake reports usage on each of two assistant
// messages per round: 2×500 + 2×400).
func TestPiAdapterThroughController(t *testing.T) {
	h := newHarness(t, nil, 2)
	dir := t.TempDir()
	plan := map[string]any{
		"p/r0": map[string]any{"write": map[string]string{"pkg/p.go": goFile("P"), "pkg/p_test.go": goTest("P", 3)}, "tokens": 500},
		"p/r1": map[string]any{"write": map[string]string{"pkg/p_test.go": goTest("P", 2)}, "tokens": 400},
	}
	b, _ := json.Marshal(plan)
	pf := filepath.Join(dir, "plan.json")
	os.WriteFile(pf, b, 0o644)
	h.cfg.Agent = "pi"
	h.cfg.Adapters["pi"] = agent.Adapter{Name: "pi", Protocol: "pi-rpc", Argv: []string{fakePiBin}, Version: "1.5.0",
		Model: "local/qwen3-coder", StallTimeout: time.Minute, ToolTimeout: time.Minute,
		Env: map[string]string{"FAKE_PI_PLAN": pf, "FAKE_PI_LOG": filepath.Join(dir, "log")}}
	a := h.open(context.Background())
	defer a.Close()
	stop := runController(t, a)
	defer stop()
	s := spec("add P", "pkg/**")
	inbox.Write(inbox.Dir(a.CommonDir), inbox.Command{Kind: inbox.Submit, Task: "p", Spec: &s})
	tk := waitPhase(t, a, "p", api.PhaseIntegrated, "")
	if tk.Status.PatchRound != 1 || tk.Status.TokensUsed != 1800 {
		t.Fatalf("round %d tokens %d", tk.Status.PatchRound, tk.Status.TokensUsed)
	}
	prompts, _ := os.ReadFile(filepath.Join(dir, "log", "prompts"))
	if !strings.Contains(string(prompts), "spine-round: 1") || !strings.Contains(string(prompts), "TestP") {
		t.Fatalf("round 1 prompt lacked the failing test:\n%s", prompts)
	}
}
