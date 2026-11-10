package e2e

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/patcarter883/spine-next/internal/api"
	"github.com/patcarter883/spine-next/internal/app"
	"github.com/patcarter883/spine-next/internal/inbox"
)

func runController(t *testing.T, a *app.App) (stop func()) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Controller.Run(ctx) }()
	return func() {
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func command(t *testing.T, a *app.App, kind inbox.Kind, id api.TaskID) {
	t.Helper()
	if _, err := inbox.Write(inbox.Dir(a.CommonDir), inbox.Command{Kind: kind, Task: id, By: "test"}); err != nil {
		t.Fatal(err)
	}
}

func waitPhase(t *testing.T, a *app.App, id api.TaskID, phase api.Phase, reason string) api.Task {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		tk, err := a.Store.GetTask(context.Background(), id)
		if err == nil && tk.Status.Phase == phase && (reason == "" || tk.Status.Reason == reason) {
			return tk
		}
		time.Sleep(100 * time.Millisecond)
	}
	tk, _ := a.Store.GetTask(context.Background(), id)
	dumpEvents(t, a.Store, id)
	t.Fatalf("%s: want %s/%s, have %s/%s %s", id, phase, reason, tk.Status.Phase, tk.Status.Reason, tk.Status.Message)
	return tk
}

// B is verified on its branch but held for approval; meanwhile A changes
// Base() on main. Approving B gates the merge result, which fails (B's
// test assumed the old Base), so B gets a fresh attempt from the new base
// and integrates. The tree that lands is always a tree that passed.
func TestApprovalMergeGateOnMovedBase(t *testing.T) {
	plan := map[string]map[string]any{
		"b": {
			"a1r0": map[string]any{"write": map[string]string{"pkg/b_test.go": "package pkg\n\nimport \"testing\"\n\nfunc TestB(t *testing.T) {\n\tif Base() != 1 {\n\t\tt.Fatal(Base())\n\t}\n}\n"}},
			"a2r0": map[string]any{"write": map[string]string{"pkg/b_test.go": "package pkg\n\nimport \"testing\"\n\nfunc TestB(t *testing.T) {\n\tif Base() != 5 {\n\t\tt.Fatal(Base())\n\t}\n}\n"}},
		},
		"a": {"*": map[string]any{"write": map[string]string{"pkg/base.go": "package pkg\n\n// Base is here.\nfunc Base() int { return 5 }\n"}}},
	}
	h := newHarness(t, plan, 4)
	a := h.open(context.Background())
	defer a.Close()
	stop := runController(t, a)
	defer func() {
		if stop != nil {
			stop()
		}
	}()
	sb := spec("test Base", "pkg/**")
	sb.Escalation.RequireApprovalBeforeMerge = true
	if _, err := inbox.Write(inbox.Dir(a.CommonDir), inbox.Command{Kind: inbox.Submit, Task: "b", Spec: &sb}); err != nil {
		t.Fatal(err)
	}
	tb := waitPhase(t, a, "b", api.PhaseEscalated, "AwaitingApproval")
	if !tb.Status.IsTrue(api.CondGatePassed) || tb.Status.Worktree == nil {
		t.Fatalf("held task should be gate-passed with its worktree kept: %+v", tb.Status)
	}
	sa := spec("bump Base", "pkg/**")
	inbox.Write(inbox.Dir(a.CommonDir), inbox.Command{Kind: inbox.Submit, Task: "a", Spec: &sa})
	waitPhase(t, a, "a", api.PhaseIntegrated, "")

	if tb.Status.ProposedCommit == "" {
		t.Fatal("held task should record the merge commit that would land")
	}
	command(t, a, inbox.Approve, "b")
	// The approved change fails on the moved base; the fresh attempt is a
	// different change, so it waits for approval again.
	deadline := time.Now().Add(2 * time.Minute)
	for {
		tb, _ = a.Store.GetTask(context.Background(), "b")
		if tb.Status.Attempt == 2 && tb.Status.Reason == "AwaitingApproval" || time.Now().After(deadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if tb.Status.Attempt != 2 || tb.Status.Phase != api.PhaseEscalated || !strings.Contains(tb.Status.Message, "differs from the one you approved") {
		t.Fatalf("b: want attempt 2 held for a new approval: a%d %q", tb.Status.Attempt, tb.Status.Message)
	}
	command(t, a, inbox.Approve, "b")
	tb = waitPhase(t, a, "b", api.PhaseIntegrated, "")
	sawMergeFail := false
	evs, _ := a.Store.ListEvents(context.Background(), "b", 0, 0)
	for _, e := range evs {
		if e.Kind == "decision" && contains(string(e.Data), "FailsOnNewBase") {
			sawMergeFail = true
		}
	}
	if !sawMergeFail {
		dumpEvents(t, a.Store, "b")
		t.Fatal("expected a FailsOnNewBase decision")
	}
	stop()
	stop = nil
	if s := h.repo.Git("status", "--porcelain"); s != "" {
		t.Fatalf("primary dirty: %s", s)
	}
}

// A task that never fixes its failing test escalates when its budget is
// spent; retry grants one more attempt, which succeeds.
func TestNoProgressEscalationAndRetry(t *testing.T) {
	bad := map[string]any{"write": map[string]string{"pkg/c.go": goFile("C"), "pkg/c_test.go": goTest("C", 9)}}
	plan := map[string]map[string]any{"c": {
		"a1r0": bad, "a1r1": bad, "a1r2": bad,
		"a2r0": map[string]any{"write": map[string]string{"pkg/c.go": goFile("C"), "pkg/c_test.go": goTest("C", 2)}},
	}}
	h := newHarness(t, plan, 2)
	a := h.open(context.Background())
	defer a.Close()
	stop := runController(t, a)
	defer stop()
	s := spec("add C", "pkg/**")
	s.Budget = api.Budget{MaxAttempts: 1, MaxPatchRounds: 2}
	inbox.Write(inbox.Dir(a.CommonDir), inbox.Command{Kind: inbox.Submit, Task: "c", Spec: &s})
	tk := waitPhase(t, a, "c", api.PhaseEscalated, "")
	if tk.Status.Reason != "NoProgress" && tk.Status.Reason != "BudgetExhausted" {
		t.Fatalf("reason %s", tk.Status.Reason)
	}
	if tk.Status.IsTrue(api.CondGatePassed) {
		t.Fatal("escalated task claims GatePassed")
	}
	command(t, a, inbox.Retry, "c")
	tk = waitPhase(t, a, "c", api.PhaseIntegrated, "")
	if tk.Status.Attempt != 2 || tk.Spec.Generation != 1 {
		t.Fatalf("retry: attempt %d generation %d", tk.Status.Attempt, tk.Spec.Generation)
	}
}

// Stopping the controller mid-run cancels the agent; a new controller
// resumes the task in a patch round and finishes it.
func TestControllerRestartResumes(t *testing.T) {
	plan := map[string]map[string]any{"d": {
		"r0": map[string]any{"hang": true},
		"r1": map[string]any{"write": map[string]string{"pkg/d.go": goFile("D")}},
	}}
	h := newHarness(t, plan, 2)
	a := h.open(context.Background())
	stop := runController(t, a)
	s := spec("add D", "pkg/**")
	inbox.Write(inbox.Dir(a.CommonDir), inbox.Command{Kind: inbox.Submit, Task: "d", Spec: &s})
	waitPhase(t, a, "d", api.PhaseWorking, "AgentRunning")
	time.Sleep(500 * time.Millisecond)
	stop()
	tk, _ := a.Store.GetTask(context.Background(), "d")
	if tk.Status.Op != nil {
		t.Fatalf("op left after graceful stop: %+v", tk.Status.Op)
	}
	a.Close()

	a2 := h.open(context.Background())
	defer a2.Close()
	stop2 := runController(t, a2)
	defer stop2()
	tk = waitPhase(t, a2, "d", api.PhaseIntegrated, "")
	if tk.Status.PatchRound != 1 {
		t.Fatalf("want resume in round 1, got r%d", tk.Status.PatchRound)
	}
}

// Cancel stops a running agent and releases the worktree.
func TestCancel(t *testing.T) {
	plan := map[string]map[string]any{"e": {"*": map[string]any{"hang": true}}}
	h := newHarness(t, plan, 2)
	a := h.open(context.Background())
	defer a.Close()
	stop := runController(t, a)
	defer stop()
	s := spec("hang", "pkg/**")
	inbox.Write(inbox.Dir(a.CommonDir), inbox.Command{Kind: inbox.Submit, Task: "e", Spec: &s})
	waitPhase(t, a, "e", api.PhaseWorking, "AgentRunning")
	command(t, a, inbox.Cancel, "e")
	waitPhase(t, a, "e", api.PhaseCancelled, "")
	deadline := time.Now().Add(10 * time.Second)
	for {
		l, err := a.Store.GetLease(context.Background(), "e", 1)
		if err == nil && l.State == "Released" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("lease not released: %+v %v", l, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// A task whose merge check passed in its current round, left Verifying by a
// restart, goes back to the merge queue instead of re-running its branch
// gates.
func TestPassedMergeCheckSurvivesRestart(t *testing.T) {
	plan := map[string]map[string]any{"m": {"*": map[string]any{"write": map[string]string{"pkg/m.go": goFile("M")}}}}
	h := newHarness(t, plan, 2)
	a := h.open(context.Background())
	stop := runController(t, a)
	s := spec("add M", "pkg/**")
	s.Escalation.RequireApprovalBeforeMerge = true
	inbox.Write(inbox.Dir(a.CommonDir), inbox.Command{Kind: inbox.Submit, Task: "m", Spec: &s})
	waitPhase(t, a, "m", api.PhaseEscalated, "AwaitingApproval")
	stop()
	ctx := context.Background()
	if _, err := a.Store.UpdateStatus(ctx, "m", 0, api.SourceGate, func(st *api.Status) error {
		st.LastGate.OnMerge = true
		st.Phase, st.Reason, st.Op = api.PhaseVerifying, "GateRunningOnMerge", nil
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Store.UpdateStatus(ctx, "m", 0, api.SourceHuman, func(st *api.Status) error {
		st.SetCondition(api.CondApproved, true, "Approved", "test", api.SourceHuman)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	evs, _ := a.Store.ListEvents(ctx, "m", 0, 0)
	before := len(evs)
	a.Close()

	a2 := h.open(ctx)
	defer a2.Close()
	stop2 := runController(t, a2)
	defer stop2()
	waitPhase(t, a2, "m", api.PhaseIntegrated, "")
	evs, _ = a2.Store.ListEvents(ctx, "m", 0, 0)
	for _, e := range evs[before:] {
		if e.Kind == "verdict" {
			t.Fatalf("re-gated after restart: %s", e.Data)
		}
	}
}

// A merge-stage gate is left out of every round and runs on the merge
// result. Its failure goes back to the agent as its own to fix, not as a
// moved base, and the patched change lands once it passes.
func TestMergeStageGateRunsOnlyOnTheMergeResult(t *testing.T) {
	plan := map[string]map[string]any{"m": {
		"a1r0": map[string]any{"write": map[string]string{"pkg/m.go": goFile("M"), "flag.txt": "no\n"}},
		"a1r1": map[string]any{"write": map[string]string{"flag.txt": "ok\n"}},
	}}
	h := newHarness(t, plan, 2)
	h.repo.Write(map[string]string{".spine/gates.yaml": strings.Replace(gates, "order: [scope, nonempty, build, unit]\n", "", 1) +
		"  slow:     {tool: cmd.run, stage: merge, args: {argv: [grep, -qx, ok, flag.txt]}}\norder: [scope, nonempty, build, unit, slow]\n"})
	h.repo.Commit("merge-stage gate")
	a := h.open(context.Background())
	defer a.Close()
	stop := runController(t, a)
	defer stop()
	s := spec("add M", "pkg/**", "flag.txt")
	inbox.Write(inbox.Dir(a.CommonDir), inbox.Command{Kind: inbox.Submit, Task: "m", Spec: &s})
	tk := waitPhase(t, a, "m", api.PhaseIntegrated, "")
	if tk.Status.PatchRound != 1 || tk.Status.Attempt != 1 {
		t.Fatalf("want one patch round in attempt 1: a%d r%d", tk.Status.Attempt, tk.Status.PatchRound)
	}
	var rounds, merges int
	evs, _ := a.Store.ListEvents(context.Background(), "m", 0, 0)
	for _, e := range evs {
		d := string(e.Data)
		switch e.Kind {
		case "verdict":
			if strings.Contains(d, `"on_merge":true`) {
				merges++
				if !strings.Contains(d, `"gate":"slow"`) {
					t.Fatalf("merge check without the merge-stage gate: %s", d)
				}
			} else {
				rounds++
				if strings.Contains(d, `"gate":"slow"`) {
					t.Fatalf("a round ran the merge-stage gate: %s", d)
				}
			}
		case "decision":
			if strings.Contains(d, "FailsOnNewBase") || strings.Contains(d, "NoVerdict") {
				t.Fatalf("a merge-stage failure is the change's verdict to fix: %s", d)
			}
		case "agent_start":
			if strings.Contains(d, `"round":1`) && !strings.Contains(d, `slow (cmd.run)`) {
				t.Fatalf("round 1 is not told the merge-stage gate failed: %s", d)
			}
		}
	}
	if rounds != 2 || merges != 2 {
		dumpEvents(t, a.Store, "m")
		t.Fatalf("want 2 round verdicts and 2 merge checks, have %d and %d", rounds, merges)
	}
}

// A merge-stage gate failing the same way two rounds running is no progress:
// the next attempt starts fresh rather than patching again.
func TestRepeatedMergeStageFailureIsNoProgress(t *testing.T) {
	plan := map[string]map[string]any{"m": {
		"a1r0": map[string]any{"write": map[string]string{"pkg/m.go": goFile("M"), "flag.txt": "no\n"}},
		"a1r1": map[string]any{"write": map[string]string{"pkg/n.go": goFile("N")}},
		"a2r0": map[string]any{"write": map[string]string{"pkg/m.go": goFile("M"), "flag.txt": "ok\n"}},
	}}
	h := newHarness(t, plan, 2)
	h.repo.Write(map[string]string{".spine/gates.yaml": strings.Replace(gates, "order: [scope, nonempty, build, unit]\n", "", 1) +
		"  slow:     {tool: cmd.run, stage: merge, args: {argv: [grep, -qx, ok, flag.txt]}}\norder: [scope, nonempty, build, unit, slow]\n"})
	h.repo.Commit("merge-stage gate")
	a := h.open(context.Background())
	defer a.Close()
	stop := runController(t, a)
	defer stop()
	s := spec("add M", "pkg/**", "flag.txt")
	inbox.Write(inbox.Dir(a.CommonDir), inbox.Command{Kind: inbox.Submit, Task: "m", Spec: &s})
	tk := waitPhase(t, a, "m", api.PhaseIntegrated, "")
	if tk.Status.Attempt != 2 {
		dumpEvents(t, a.Store, "m")
		t.Fatalf("want a fresh second attempt, have a%d r%d", tk.Status.Attempt, tk.Status.PatchRound)
	}
	evs, _ := a.Store.ListEvents(context.Background(), "m", 0, 0)
	for _, e := range evs {
		if e.Kind == "decision" && strings.Contains(string(e.Data), `"round":1`) && !strings.Contains(string(e.Data), "NoProgress") {
			t.Fatalf("round 1 decision: %s", e.Data)
		}
	}
}

// A tests-only task that changes only tests integrates; one that also
// changes code waits for approval, naming the files, and lands once approved.
func TestTestsOnlyTaskHoldsCodeChangesForApproval(t *testing.T) {
	plan := map[string]map[string]any{
		"tests": {"*": map[string]any{"write": map[string]string{"pkg/base_test.go": goTest("Base", 1)}}},
		"fixes": {"*": map[string]any{"write": map[string]string{"pkg/f.go": goFile("F"), "pkg/f_test.go": goTest("F", 2)}}},
	}
	h := newHarness(t, plan, 2)
	a := h.open(context.Background())
	defer a.Close()
	stop := runController(t, a)
	defer stop()
	for _, id := range []api.TaskID{"tests", "fixes"} {
		s := spec("test "+string(id), "pkg/**")
		s.TestsOnly = true
		inbox.Write(inbox.Dir(a.CommonDir), inbox.Command{Kind: inbox.Submit, Task: id, Spec: &s})
	}
	waitPhase(t, a, "tests", api.PhaseIntegrated, "")
	held := waitPhase(t, a, "fixes", api.PhaseEscalated, "AwaitingApproval")
	if !strings.Contains(held.Status.Message, "tests-only task changed code outside its tests (pkg/f.go)") {
		t.Fatalf("message: %q", held.Status.Message)
	}
	command(t, a, inbox.Approve, "fixes")
	waitPhase(t, a, "fixes", api.PhaseIntegrated, "")
}
