package agent

import (
	"context"
	"os"
	"path/filepath"
	"slices"
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

func TestPiAgentDirReachesHostRun(t *testing.T) {
	t.Setenv(PiAgentDirEnv, "/from/env")
	has := func(a Adapter, want string) bool { return slices.Contains(buildEnv(a), PiAgentDirEnv+"="+want) }
	host := Adapter{Protocol: "pi-rpc", PassEnv: []string{"PATH"}}
	if !has(host, "/from/env") {
		t.Fatal("a host-run Pi must get the agent dir, even with a custom pass_env")
	}
	if has(Adapter{Protocol: "pi-rpc", Container: &Container{Image: "x"}}, "/from/env") {
		t.Fatal("a container gets its private copy, not the host path")
	}
	if has(Adapter{Protocol: "acp"}, "/from/env") {
		t.Fatal("other agents don't need it")
	}
}

func TestLoadPiProviderAndKeys(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "models.json"), []byte(`{"providers":{"local":{"baseUrl":"http://h/v1","api":"openai-completions","apiKey":"$SPINE_TEST_KEY"},
	 "claude":{"baseUrl":"http://h","api":"anthropic-messages"}}}`), 0o600)
	p, err := LoadPiProvider(dir, "local")
	if err != nil || p.BaseURL != "http://h/v1" {
		t.Fatalf("%+v %v", p, err)
	}
	if _, err := p.ResolveKey(); err == nil {
		t.Fatal("an unset variable must be an error")
	}
	t.Setenv("SPINE_TEST_KEY", "secret")
	if k, err := p.ResolveKey(); err != nil || k != "secret" {
		t.Fatalf("%q %v", k, err)
	}
	if _, err := LoadPiProvider(dir, "claude"); err == nil {
		t.Fatal("other APIs are not callable")
	}
	if _, err := LoadPiProvider(dir, "nope"); err == nil {
		t.Fatal("unknown provider")
	}
	for raw, want := range map[string]string{"": "", "lit": "lit", "$$x": "$x", "$!x": "!x"} {
		if k, err := (PiProvider{APIKey: raw}).ResolveKey(); err != nil || k != want {
			t.Errorf("%q: %q %v", raw, k, err)
		}
	}
	if _, err := (PiProvider{APIKey: "!cat key"}).ResolveKey(); err == nil {
		t.Fatal("commands must be refused")
	}
}

func TestPiProviderKeyReachesAgent(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "models.json"), []byte(`{"providers":{"local":{"baseUrl":"http://h/v1","api":"openai-completions","apiKey":"$SPINE_TEST_LOCAL_KEY"}}}`), 0o600)
	t.Setenv("SPINE_TEST_LOCAL_KEY", "local-secret")
	t.Setenv("OPENROUTER_API_KEY", "or-secret")
	t.Setenv("OPENAI_API_KEY", "oa-secret")
	base := Adapter{Protocol: "pi-rpc", PassEnv: []string{"LANG"}, Env: map[string]string{PiAgentDirEnv: dir}, Container: &Container{Image: "x"}}
	with := func(model string) []string { a := base; a.Model = model; return buildEnv(a) }
	if env := with("openrouter/z-ai/glm-5.3"); !slices.Contains(env, "OPENROUTER_API_KEY=or-secret") || slices.Contains(env, "OPENAI_API_KEY=oa-secret") {
		t.Fatalf("a built-in provider gets its own key and no other: %v", env)
	}
	if env := with("local/m"); !slices.Contains(env, "SPINE_TEST_LOCAL_KEY=local-secret") {
		t.Fatalf("a models.json provider gets the variable its apiKey names: %v", env)
	}
	a := base
	a.Model, a.PassEnv = "openrouter/m", []string{"OPENROUTER_API_KEY"}
	if n := len(slices.DeleteFunc(buildEnv(a), func(s string) bool { return !strings.HasPrefix(s, "OPENROUTER_API_KEY=") })); n != 1 {
		t.Fatalf("passed once, got %d", n)
	}
	if env := buildEnv(Adapter{Protocol: "acp", Model: "openrouter/m"}); slices.Contains(env, "OPENROUTER_API_KEY=or-secret") {
		t.Fatal("only Pi adapters get provider keys this way")
	}
}
