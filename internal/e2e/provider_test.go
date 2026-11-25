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
	"github.com/patcarter883/spine-next/internal/app"
	"github.com/patcarter883/spine-next/internal/inbox"
	"github.com/patcarter883/spine-next/internal/reconciler"
)

func providerTiming(t *testing.T, backoff, max, limit time.Duration) {
	b, m, l := reconciler.ProviderBackoff, reconciler.ProviderBackoffMax, reconciler.ProviderOutageLimit
	reconciler.ProviderBackoff, reconciler.ProviderBackoffMax, reconciler.ProviderOutageLimit = backoff, max, limit
	t.Cleanup(func() {
		reconciler.ProviderBackoff, reconciler.ProviderBackoffMax, reconciler.ProviderOutageLimit = b, m, l
	})
}

func piHarness(t *testing.T, plan map[string]any) (*harness, string) {
	h := newHarness(t, nil, 2)
	dir := t.TempDir()
	b, _ := json.Marshal(plan)
	pf := filepath.Join(dir, "plan.json")
	os.WriteFile(pf, b, 0o644)
	h.cfg.Agent = "pi"
	h.cfg.Adapters["pi"] = agent.Adapter{Name: "pi", Protocol: "pi-rpc", Argv: []string{fakePiBin}, Version: "1.5.0",
		Model: "local/qwen3-coder", StallTimeout: time.Minute, ToolTimeout: time.Minute,
		Env: map[string]string{"FAKE_PI_PLAN": pf, "FAKE_PI_LOG": filepath.Join(dir, "log")}}
	return h, filepath.Join(dir, "log")
}

func submit(t *testing.T, a *app.App, id api.TaskID, s api.Spec) {
	t.Helper()
	if _, err := inbox.Write(inbox.Dir(a.CommonDir), inbox.Command{Kind: inbox.Submit, Task: id, Spec: &s}); err != nil {
		t.Fatal(err)
	}
}

func countEvents(t *testing.T, a *app.App, id api.TaskID, kind string, match func(json.RawMessage) bool) int {
	t.Helper()
	evs, err := a.Store.ListEvents(context.Background(), id, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range evs {
		if e.Kind == kind && (match == nil || match(e.Data)) {
			n++
		}
	}
	return n
}

func providerOutcome(d json.RawMessage) bool {
	var o api.Outcome
	return json.Unmarshal(d, &o) == nil && o.Provider == api.ProviderUnreachable
}

// The provider is down for the task's first two runs and then comes back:
// the same round runs again each time, the supervisor is never asked and
// no attempt, round or token is charged.
func TestProviderOutageResumes(t *testing.T) {
	providerTiming(t, 100*time.Millisecond, 400*time.Millisecond, time.Minute)
	h, _ := piHarness(t, map[string]any{"p": map[string]any{"down": 2, "write": map[string]string{"pkg/p.go": goFile("P"), "pkg/p_test.go": goTest("P", 2)}}})
	a := h.open(context.Background())
	defer a.Close()
	stop := runController(t, a)
	defer stop()
	submit(t, a, "p", spec("add P", "pkg/**"))
	tk := waitPhase(t, a, "p", api.PhaseIntegrated, "")
	s := tk.Status
	if s.Attempt != 1 || s.PatchRound != 0 || s.TokensUsed != 0 || s.ProviderFailures != 0 {
		t.Fatalf("a%d r%d tokens %d provider failures %d", s.Attempt, s.PatchRound, s.TokensUsed, s.ProviderFailures)
	}
	if n := countEvents(t, a, "p", "outcome", providerOutcome); n != 2 {
		t.Errorf("%d provider-failure outcomes, want 2", n)
	}
	if n := countEvents(t, a, "p", "agent_start", nil); n != 3 {
		t.Errorf("%d runs, want 3", n)
	}
	if n := countEvents(t, a, "p", "decision", nil); n != 0 {
		t.Errorf("supervisor asked %d times", n)
	}
	if n := countEvents(t, a, "p", "provider_back", nil); n != 1 {
		t.Errorf("%d provider_back events", n)
	}
}

// A provider that never comes back escalates the task as
// ProviderUnavailable once the outage limit passes, its budget untouched.
func TestProviderOutageEscalates(t *testing.T) {
	providerTiming(t, 50*time.Millisecond, 200*time.Millisecond, 1500*time.Millisecond)
	h, _ := piHarness(t, map[string]any{"*": map[string]any{"down": 1000}})
	a := h.open(context.Background())
	defer a.Close()
	stop := runController(t, a)
	defer stop()
	start := time.Now()
	submit(t, a, "p", spec("add P", "pkg/**"))
	tk := waitPhase(t, a, "p", api.PhaseEscalated, "ProviderUnavailable")
	s := tk.Status
	if s.Attempt != 1 || s.PatchRound != 0 || s.TokensUsed != 0 || s.ProviderFailures < 2 {
		t.Fatalf("a%d r%d tokens %d provider failures %d", s.Attempt, s.PatchRound, s.TokensUsed, s.ProviderFailures)
	}
	if !strings.Contains(s.Message, "adapter pi") || !strings.Contains(s.Message, "Connection error.") {
		t.Errorf("message %q", s.Message)
	}
	if n := countEvents(t, a, "p", "decision", nil); n != 0 {
		t.Errorf("supervisor asked %d times", n)
	}
	if d := time.Since(start); d > 30*time.Second {
		t.Errorf("escalated after %s", d)
	}
}

// While an adapter's provider is down, another task on that adapter does
// not start a run; it says what it waits for.
func TestProviderOutageHoldsOtherTasks(t *testing.T) {
	providerTiming(t, time.Hour, time.Hour, 2*time.Hour)
	h, log := piHarness(t, map[string]any{
		"p": map[string]any{"down": 1000},
		"q": map[string]any{"write": map[string]string{"pkg/q.go": goFile("Q"), "pkg/q_test.go": goTest("Q", 2)}},
	})
	a := h.open(context.Background())
	defer a.Close()
	stop := runController(t, a)
	defer stop()
	submit(t, a, "p", spec("add P", "pkg/**"))
	deadline := time.Now().Add(time.Minute)
	for {
		tk, err := a.Store.GetTask(context.Background(), "p")
		if err == nil && tk.Status.ProviderFailures > 0 {
			break
		}
		if time.Now().After(deadline) {
			dumpEvents(t, a.Store, "p")
			t.Fatal("p never recorded a provider failure")
		}
		time.Sleep(50 * time.Millisecond)
	}
	submit(t, a, "q", spec("add Q", "pkg/**"))
	time.Sleep(3 * time.Second)
	q, err := a.Store.GetTask(context.Background(), "q")
	if err != nil {
		t.Fatal(err)
	}
	if q.Status.Phase != api.PhasePending || !strings.Contains(q.Status.Message, "waiting for adapter pi: provider unreachable") {
		t.Fatalf("q: %s %s %q", q.Status.Phase, q.Status.Reason, q.Status.Message)
	}
	if n := countEvents(t, a, "q", "agent_start", nil); n != 0 {
		t.Fatalf("q started %d runs", n)
	}
	if n := countEvents(t, a, "p", "agent_start", nil); n != 1 {
		t.Fatalf("p started %d runs during its backoff", n)
	}
	if b, _ := os.ReadFile(filepath.Join(log, "down")); strings.Count(string(b), "\n") != 1 {
		t.Fatalf("%d runs reached the fake provider", strings.Count(string(b), "\n"))
	}
}

// A provider that rejects the credentials escalates at once.
func TestProviderAuthEscalates(t *testing.T) {
	h, _ := piHarness(t, map[string]any{"*": map[string]any{"stop": "error",
		"error_message": `401 {"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`}})
	a := h.open(context.Background())
	defer a.Close()
	stop := runController(t, a)
	defer stop()
	submit(t, a, "p", spec("add P", "pkg/**"))
	tk := waitPhase(t, a, "p", api.PhaseEscalated, "ProviderAuthFailed")
	if tk.Status.Attempt != 1 || tk.Status.PatchRound != 0 || !strings.Contains(tk.Status.Message, "invalid x-api-key") {
		t.Fatalf("a%d r%d %q", tk.Status.Attempt, tk.Status.PatchRound, tk.Status.Message)
	}
	if n := countEvents(t, a, "p", "decision", nil); n != 0 {
		t.Errorf("supervisor asked %d times", n)
	}
}
