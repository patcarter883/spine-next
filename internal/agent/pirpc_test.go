package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/patcarter883/spine-next/internal/api"
)

func (e env) runPi(t *testing.T, tweak func(*Adapter, *RunRequest)) (api.Outcome, []Event, error) {
	t.Helper()
	var evs []Event
	a := Adapter{Name: "pi", Protocol: "pi-rpc", Argv: []string{fakePiBin}, Version: "1.5.0", Model: "local/qwen",
		StallTimeout: time.Minute, ToolTimeout: time.Minute, MaxTurns: 80,
		Env: map[string]string{"FAKE_PI_PLAN": e.plan, "FAKE_PI_LOG": e.log}}
	req := RunRequest{Lease: e.lease, Timeout: time.Minute,
		Prompt: Prompt{Task: "t1", Attempt: 1, Intent: "make b.txt"}, Events: func(ev Event) { evs = append(evs, ev) }}
	if tweak != nil {
		tweak(&a, &req)
	}
	req.Adapter = a
	out, err := (&Runner{Procs: e.m}).Run(context.Background(), req)
	return out, evs, err
}

func TestPiCompletedRun(t *testing.T) {
	e := setup(t, map[string]map[string]any{"t1": {"write": map[string]string{"b.txt": "hi\n"}, "tokens": 300, "turns": 2}})
	out, evs, err := e.runPi(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Reason != api.OutcomeCompleted || out.StopReason != "stop" || out.Message != "Done." || out.Turns != 3 || out.TokensUsed != 900 || out.CostUSD == 0 {
		t.Fatalf("%+v", out)
	}
	if b, _ := os.ReadFile(filepath.Join(e.lease.Path, "b.txt")); string(b) != "hi\n" {
		t.Fatalf("file %q", b)
	}
	var tools []string
	for _, ev := range evs {
		if ev.Kind == "tool_call" {
			tools = append(tools, ev.Title)
		}
	}
	if strings.Join(tools, ",") != "write: b.txt,read: x" {
		t.Fatalf("events %+v", evs)
	}
	argv, _ := os.ReadFile(filepath.Join(e.log, "argv"))
	for _, f := range []string{"--mode rpc", "--no-session", "--no-approve", "--no-extensions", "--no-mcp", "--model local/qwen"} {
		if !strings.Contains(string(argv), f) {
			t.Errorf("argv lacks %s: %s", f, argv)
		}
	}
}

// A failed run must never read as success, whatever the exit code.
func TestPiFailureIsNotSuccess(t *testing.T) {
	for name, step := range map[string]map[string]any{
		"error stop":   {"stop": "error", "error_message": "500 from vLLM"},
		"retries fail": {"stop": "error", "error_message": "fetch failed", "retry_fail": true},
		"length":       {"stop": "length"},
	} {
		t.Run(name, func(t *testing.T) {
			e := setup(t, map[string]map[string]any{"*": step})
			out, _, err := e.runPi(t, nil)
			if err != nil {
				t.Fatal(err)
			}
			if out.Reason == api.OutcomeCompleted || out.ExitCode != 0 {
				t.Fatalf("%+v", out)
			}
			if name == "retries fail" && !strings.Contains(out.Message, "connection refused") {
				t.Fatalf("message %q", out.Message)
			}
		})
	}
}

// A tool call past ToolTimeout is aborted and the session continues; the
// continuation completes the task.
func TestPiToolTimeoutRecovers(t *testing.T) {
	e := setup(t, map[string]map[string]any{"*": {"hang_tool": true, "then": map[string]any{"write": map[string]string{"c.txt": "ok"}}}})
	start := time.Now()
	out, evs, err := e.runPi(t, func(a *Adapter, _ *RunRequest) { a.ToolTimeout = 2 * time.Second })
	if err != nil {
		t.Fatal(err)
	}
	if out.Reason != api.OutcomeCompleted || time.Since(start) > 20*time.Second {
		t.Fatalf("%+v after %s", out, time.Since(start))
	}
	if _, err := os.Stat(filepath.Join(e.lease.Path, "c.txt")); err != nil {
		t.Fatal("continuation did not run")
	}
	prompts, _ := os.ReadFile(filepath.Join(e.log, "prompts"))
	if !strings.Contains(string(prompts), "bash: npm run dev") || !strings.Contains(string(prompts), "ran longer than 2s") {
		t.Fatalf("continuation prompt: %s", prompts)
	}
	sawGuard := false
	for _, ev := range evs {
		sawGuard = sawGuard || (ev.Kind == "guard" && ev.Title == "continue")
	}
	if !sawGuard {
		t.Fatal("no guard event")
	}
}

func TestPiGuardsEndRuns(t *testing.T) {
	cases := []struct {
		name  string
		step  map[string]any
		tweak func(*Adapter, *RunRequest)
		want  api.OutcomeReason
		msg   string
	}{
		{"turn limit", map[string]any{"turns": 50}, func(a *Adapter, _ *RunRequest) { a.MaxTurns = 5 }, api.OutcomeBudgetExceeded, "turn limit"},
		{"token budget", map[string]any{"turns": 50, "tokens": 1000}, func(_ *Adapter, r *RunRequest) { r.MaxTokens = 5000 }, api.OutcomeBudgetExceeded, "token budget"},
		{"repeated tool timeouts", map[string]any{"hang_tool": true, "then": map[string]any{"hang_tool": true, "then": map[string]any{"hang_tool": true}}},
			func(a *Adapter, _ *RunRequest) { a.ToolTimeout = time.Second }, api.OutcomeBudgetExceeded, "timed out 3 times"},
		{"stall", map[string]any{"silent": true}, func(a *Adapter, _ *RunRequest) { a.StallTimeout = 2 * time.Second }, api.OutcomeStalled, "no activity"},
		{"run timeout", map[string]any{"hang_tool": true}, func(_ *Adapter, r *RunRequest) { r.Timeout = 3 * time.Second }, api.OutcomeTimeout, "run timeout"},
		{"abort ignored", map[string]any{"hang_tool": true, "ignore_abort": true}, func(_ *Adapter, r *RunRequest) { r.Timeout = 2 * time.Second }, api.OutcomeTimeout, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := setup(t, map[string]map[string]any{"*": c.step})
			out, _, err := e.runPi(t, c.tweak)
			if err != nil {
				t.Fatal(err)
			}
			if out.Reason != c.want || !strings.Contains(out.Message, c.msg) {
				t.Fatalf("%+v", out)
			}
		})
	}
}

func TestPiWrapUpSteer(t *testing.T) {
	e := setup(t, map[string]map[string]any{"*": {"turns": 400, "tokens": 1}})
	out, evs, _ := e.runPi(t, func(a *Adapter, r *RunRequest) { a.MaxTurns = 10000; r.Timeout = 3 * time.Second })
	for _, ev := range evs {
		if ev.Kind == "guard" {
			t.Logf("guard %+v", ev)
		}
	}
	steers, _ := os.ReadFile(filepath.Join(e.log, "steers"))
	if !strings.Contains(string(steers), "Time is nearly up") {
		t.Fatalf("no wrap-up steer (outcome %+v)", out)
	}
}

func TestPiVersionPinned(t *testing.T) {
	e := setup(t, map[string]map[string]any{"*": {}})
	_, _, err := e.runPi(t, func(a *Adapter, _ *RunRequest) { a.Version = "1.6.0" })
	if err == nil || !strings.Contains(err.Error(), "pins 1.6.0") {
		t.Fatalf("want version mismatch, got %v", err)
	}
	_, _, err = e.runPi(t, func(a *Adapter, _ *RunRequest) { a.Version = "" })
	if err == nil || !strings.Contains(err.Error(), "must pin") {
		t.Fatalf("want pin required, got %v", err)
	}
}
