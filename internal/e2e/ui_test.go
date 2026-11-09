package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/patcarter883/spine-next/internal/api"
	"github.com/patcarter883/spine-next/internal/app"
	"github.com/patcarter883/spine-next/internal/inbox"
	"github.com/patcarter883/spine-next/internal/storage"
	"github.com/patcarter883/spine-next/internal/ui"
)

// dash serves the dashboard for one app, as `spine ui` does.
type dash struct {
	t   *testing.T
	srv *httptest.Server
	a   *app.App
}

func newDash(t *testing.T, a *app.App) *dash {
	t.Helper()
	db, cd, err := app.DBPath(a.WT.Primary())
	if err != nil {
		t.Fatal(err)
	}
	ro, err := storage.OpenReadOnly(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ro.Close() })
	src := ui.SourceFunc(func(context.Context) []ui.Repo {
		return []ui.Repo{{Name: "app", Path: a.WT.Primary(), State: "running", Store: ro, InboxDir: inbox.Dir(cd), CommonDir: cd}}
	})
	s := &ui.Server{Source: src, Home: t.TempDir()}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return &dash{t: t, srv: srv, a: a}
}

func (d *dash) do(method, path string, body any, out any) int {
	d.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, d.srv.URL+path, rd)
	req.Header.Set("X-Spine", "1")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		d.t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			d.t.Fatalf("%s %s: %d %s", method, path, r.StatusCode, b)
		}
	}
	if r.StatusCode >= 400 {
		d.t.Logf("%s %s: %d %s", method, path, r.StatusCode, b)
	}
	return r.StatusCode
}

func (d *dash) text(path string) string {
	d.t.Helper()
	r, err := http.Get(d.srv.URL + path)
	if err != nil {
		d.t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return string(b)
}

func writePlan(t *testing.T, h *harness, plan map[string]map[string]any) {
	b, _ := json.Marshal(plan)
	if err := os.WriteFile(h.plan, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// Reviewing and steering a task from the dashboard: the diff of each
// snapshot and of the merge result, round-by-round comparison with fixed
// and introduced failures, stored gate logs, the agent's final message, a
// hint that resumes an escalated task in its kept worktree, a rollback to
// an earlier snapshot, and the live event stream.
func TestDashboardReviewHintRollback(t *testing.T) {
	bad := map[string]any{"write": map[string]string{"pkg/c.go": goFile("C"), "pkg/c_test.go": goTest("C", 9)}, "message": "I added C."}
	plan := map[string]map[string]any{
		"c": {"a1r0": bad, "a1r1": bad, "a1r2": map[string]any{"write": map[string]string{"pkg/c_test.go": goTest("C", 2)}, "message": "Fixed the test as hinted."}},
		"r": {
			"a1r0": map[string]any{"write": map[string]string{"pkg/r.go": goFile("R"), "pkg/r_test.go": goTest("R", 7)}},
			"a1r1": map[string]any{"write": map[string]string{"pkg/r.go": "package pkg\n\nfunc R() int { return \"broken\" }\n"}},
			"a1r2": map[string]any{"write": map[string]string{"pkg/r_test.go": goTest("R", 2)}},
		},
	}
	h := newHarness(t, plan, 2)
	a := h.open(context.Background())
	defer a.Close()
	stop := runController(t, a)
	defer stop()
	d := newDash(t, a)

	// Create through the dashboard, with a token budget and pinned gates.
	var cr map[string]any
	if c := d.do("POST", "/api/repos/app/tasks", map[string]any{"id": "c", "intent": "add C with a test", "scope": []string{"pkg/**"},
		"max_attempts": 1, "max_rounds": 1, "max_tokens": 1000000, "gates": []string{"scope", "build", "unit"}}, &cr); c != 200 {
		t.Fatalf("create %d %v", c, cr)
	}
	tk := waitPhase(t, a, "c", api.PhaseEscalated, "")
	if len(tk.Spec.Acceptance) != 3 || tk.Spec.Budget.MaxTokens != 1000000 || !strings.HasSuffix(tk.Spec.CreatedBy, "(local)") {
		t.Fatalf("spec %+v", tk.Spec)
	}

	// Diff of the first snapshot against its base.
	var df struct {
		Files []struct{ Path, Status string }
		Patch string
	}
	if c := d.do("GET", "/api/repos/app/tasks/c/diff?seq=1", nil, &df); c != 200 || len(df.Files) != 2 || !strings.Contains(df.Patch, "+func C() int { return 2 }") {
		t.Fatalf("diff %d %+v", c, df)
	}
	if c := d.do("GET", "/api/repos/app/tasks/c/diff?seq=99", nil, &map[string]any{}); c != 400 {
		t.Fatalf("unknown snapshot: %d", c)
	}
	if c := d.do("GET", "/api/repos/app/tasks/c/diff?merge="+strings.Repeat("a", 40), nil, &map[string]any{}); c != 400 {
		t.Fatalf("foreign commit accepted: %d", c)
	}

	// Rounds: the failing test persists between rounds; gate logs are stored.
	var rs struct {
		Rounds []struct {
			Round      int
			Feedback   []api.Feedback
			Outcome    *api.Outcome
			Gate       *api.GateSummary
			Persisting []api.Diagnostic
			Fixed      []api.Diagnostic
		}
	}
	d.do("GET", "/api/repos/app/tasks/c/rounds", nil, &rs)
	if len(rs.Rounds) != 2 || len(rs.Rounds[1].Feedback) == 0 || len(rs.Rounds[1].Persisting) == 0 || rs.Rounds[0].Outcome == nil ||
		!strings.Contains(rs.Rounds[0].Outcome.Message, "I added C.") {
		t.Fatalf("rounds %+v", rs)
	}
	var log string
	for _, g := range rs.Rounds[0].Gate.Gates {
		if g.Gate == "unit" {
			if len(g.Artifacts) == 0 {
				t.Fatalf("go.test verdict has no artifacts: %+v", g)
			}
			log = d.text("/api/repos/app/artifacts/" + g.Artifacts[0].Digest)
		}
	}
	if !strings.Contains(log, "TestC") {
		t.Fatalf("gate log: %q", log)
	}
	if c := d.do("GET", "/api/repos/app/artifacts/sha256:"+strings.Repeat("0", 64), nil, &map[string]any{}); c != 404 {
		t.Fatalf("missing artifact %d", c)
	}

	// A hint resumes the escalated task in its worktree with another round.
	if c := d.do("POST", "/api/repos/app/tasks/c/hint", map[string]string{"text": "C must return 2; fix the test"}, &map[string]any{}); c != 200 {
		t.Fatal("hint", c)
	}
	tk = waitPhase(t, a, "c", api.PhaseIntegrated, "")
	if tk.Status.Attempt != 1 || tk.Status.PatchRound != 2 {
		t.Fatalf("resumed: a%d r%d", tk.Status.Attempt, tk.Status.PatchRound)
	}
	prompts, _ := os.ReadFile(filepath.Join(h.log, "c.log"))
	if !strings.Contains(string(prompts), "C must return 2; fix the test") || !strings.Contains(string(prompts), "TestC") {
		t.Fatalf("round 2 prompt lacks the hint or the gate feedback:\n%s", prompts)
	}
	// The merge-result diff of the integrated commit.
	if c := d.do("GET", "/api/repos/app/tasks/c/diff?merge="+tk.Status.IntegratedCommit, nil, &df); c != 200 || !strings.Contains(df.Patch, "pkg/c.go") {
		t.Fatalf("merge diff %d", c)
	}
	d.do("GET", "/api/repos/app/tasks/c/rounds", nil, &rs)
	if last := rs.Rounds[len(rs.Rounds)-1]; len(last.Fixed) == 0 || !last.Gate.Passed {
		t.Fatalf("round 2 should fix the failure: %+v", last)
	}

	// Rollback: round 1 broke the build; continue from round 0's snapshot.
	d.do("POST", "/api/repos/app/tasks", map[string]any{"id": "r", "intent": "add R", "scope": []string{"pkg/**"}, "max_attempts": 1, "max_rounds": 1}, &cr)
	tk = waitPhase(t, a, "r", api.PhaseEscalated, "")
	if len(tk.Status.Snapshots) < 2 {
		t.Fatalf("snapshots %+v", tk.Status.Snapshots)
	}
	first := tk.Status.Snapshots[0].Seq
	if c := d.do("POST", "/api/repos/app/tasks/r/rollback", map[string]int{"seq": 999}, &map[string]any{}); c != 400 {
		t.Fatalf("bad seq %d", c)
	}
	if c := d.do("POST", "/api/repos/app/tasks/r/rollback", map[string]int{"seq": first}, &map[string]any{}); c != 200 {
		t.Fatal("rollback", c)
	}
	tk = waitPhase(t, a, "r", api.PhaseIntegrated, "")
	if got := h.repo.Git("show", "main:pkg/r.go"); !strings.Contains(got, "return 2") {
		t.Fatalf("integrated r.go is not round 0's: %s", got)
	}

	// The live stream replays a task's events.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", d.srv.URL+"/api/repos/app/tasks/c/live", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 4<<20)
	sawMsg, sawTask := false, false
	for sc.Scan() && !(sawMsg && sawTask) {
		l := sc.Text()
		sawMsg = sawMsg || strings.Contains(l, `"kind":"message"`) && strings.Contains(l, "Fixed the test")
		sawTask = sawTask || l == "event: task"
	}
	resp.Body.Close()
	if !sawMsg || !sawTask {
		t.Fatalf("live: message %v task %v", sawMsg, sawTask)
	}

	// Monitoring endpoints see the work.
	var st struct {
		Totals  map[string]int
		Quality struct {
			Finished, FirstRoundPass int
			Escalations              []struct{ Name string }
			FailingGates             []struct{ Name string } `json:"failing_gates"`
			Supervisors              []struct {
				Supervisor string
				Decisions  int
			}
		}
		Cost struct{ Tokens int64 }
	}
	d.do("GET", "/api/stats?days=7", nil, &st)
	if st.Totals["integrated"] != 2 || st.Totals["merged_today"] != 2 || st.Quality.Finished != 2 || len(st.Quality.Escalations) == 0 ||
		len(st.Quality.FailingGates) == 0 || len(st.Quality.Supervisors) == 0 {
		t.Fatalf("stats %+v", st)
	}
	var sr struct {
		Tasks []struct{ ID string }
		Total int
	}
	d.do("GET", "/api/search?q=add+r&phase=Integrated", nil, &sr)
	if sr.Total != 1 || sr.Tasks[0].ID != "r" {
		t.Fatalf("search %+v", sr)
	}
	var hl struct {
		Findings []struct{ Level, Check string }
		DBBytes  int64 `json:"db_bytes"`
	}
	d.do("GET", "/api/repos/app/health", nil, &hl)
	if len(hl.Findings) == 0 || hl.DBBytes == 0 {
		t.Fatalf("health %+v", hl)
	}
	var audit struct{ Entries []ui.AuditEntry }
	d.do("GET", "/api/audit", nil, &audit)
	if len(audit.Entries) < 4 || audit.Entries[len(audit.Entries)-1].Detail != "create" {
		t.Fatalf("audit %+v", audit.Entries)
	}
}

// Pausing holds new tasks in Pending; a pending task can be edited (a new
// generation); bulk cancel; GC through the controller.
func TestDashboardPauseEditBulkGC(t *testing.T) {
	plan := map[string]map[string]any{
		"e": {"*": map[string]any{"write": map[string]string{"pkg/e.go": goFile("E")}}},
	}
	h := newHarness(t, plan, 2)
	a := h.open(context.Background())
	defer a.Close()
	stop := runController(t, a)
	defer stop()
	d := newDash(t, a)
	if c := d.do("POST", "/api/repos/app/pause", map[string]string{"reason": "release freeze"}, &map[string]any{}); c != 200 {
		t.Fatal("pause", c)
	}
	for _, id := range []string{"e", "x1", "x2"} {
		d.do("POST", "/api/repos/app/tasks", map[string]any{"id": id, "intent": "first version of " + id, "scope": []string{"docs/**"}}, &map[string]any{})
	}
	tk := waitPhase(t, a, "e", api.PhasePending, "")
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(tk.Status.Message, "paused") && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		tk, _ = a.Store.GetTask(context.Background(), "e")
	}
	if !strings.Contains(tk.Status.Message, "paused by") || tk.Status.Attempt != 0 {
		t.Fatalf("paused task: %+v", tk.Status)
	}
	var ov struct{ Repos []ui.Repo }
	d.do("GET", "/api/overview", nil, &ov)
	if ov.Repos[0].Paused == nil || ov.Repos[0].Paused.Reason != "release freeze" {
		t.Fatalf("overview paused %+v", ov.Repos[0])
	}
	// Edit while pending: new intent and scope.
	if c := d.do("POST", "/api/repos/app/tasks/e/edit", map[string]any{"task": map[string]any{"intent": "add E", "scope": []string{"pkg/**"}}}, &map[string]any{}); c != 200 {
		t.Fatal("edit", c)
	}
	deadline = time.Now().Add(10 * time.Second)
	for tk.Spec.Generation == 0 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		tk, _ = a.Store.GetTask(context.Background(), "e")
	}
	if tk.Spec.Generation != 1 || tk.Spec.Intent != "add E" || tk.Spec.Scope[0] != "pkg/**" || tk.Status.Phase != api.PhasePending {
		t.Fatalf("edited %+v %+v", tk.Spec, tk.Status)
	}
	// Bulk cancel the other two; an unknown task is reported, not fatal.
	var br struct {
		Queued  int
		Results []struct {
			ID     string
			Queued bool
		}
	}
	d.do("POST", "/api/bulk", map[string]any{"action": "cancel", "tasks": []map[string]string{{"repo": "app", "id": "x1"}, {"repo": "app", "id": "x2"}, {"repo": "app", "id": "nope"}}}, &br)
	if br.Queued != 2 || br.Results[2].Queued {
		t.Fatalf("bulk %+v", br)
	}
	waitPhase(t, a, "x1", api.PhaseCancelled, "")
	waitPhase(t, a, "x2", api.PhaseCancelled, "")
	d.do("POST", "/api/repos/app/resume", nil, &map[string]any{})
	waitPhase(t, a, "e", api.PhaseIntegrated, "")

	// GC: plan, then run through the controller (snapshot refs of finished
	// tasks are kept for 1 day, so nothing goes yet; the command applies).
	var gc struct {
		SnapshotRefs []string `json:"snapshot_refs"`
	}
	if c := d.do("GET", "/api/repos/app/gc?days=1", nil, &gc); c != 200 {
		t.Fatal("gc plan", c)
	}
	if c := d.do("POST", "/api/repos/app/gc", map[string]int{"days": 1}, &map[string]any{}); c != 200 {
		t.Fatal("gc", c)
	}
	deadline = time.Now().Add(10 * time.Second)
	for {
		evs, _ := a.Store.ListEvents(context.Background(), "", 0, 0)
		if len(evs) > 0 && evs[len(evs)-1].Kind == "gc" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("gc did not run")
		}
		time.Sleep(100 * time.Millisecond)
	}
	// Slots view (standalone: derived from phases and the config).
	var sl struct {
		Repos []struct {
			Parallel int
			Held     []string
		}
	}
	d.do("GET", "/api/slots", nil, &sl)
	if len(sl.Repos) != 1 || sl.Repos[0].Parallel < 1 {
		t.Fatalf("slots %+v", sl)
	}
}

// A planner task: the agent writes a plan; spec.validate rejects the first
// one (a cycle) and the planner fixes it; a person edits and submits it from
// the dashboard; the planned tasks run in dependency order.
func TestDashboardPlanner(t *testing.T) {
	h := newHarness(t, nil, 2)
	a := h.open(context.Background())
	defer a.Close()
	stop := runController(t, a)
	defer stop()
	d := newDash(t, a)
	d.do("POST", "/api/repos/app/pause", nil, &map[string]any{})
	var pr struct{ ID string }
	if c := d.do("POST", "/api/repos/app/plans", map[string]any{"goal": "Add features A and B; B builds on A."}, &pr); c != 200 || !strings.HasPrefix(pr.ID, "plan-") {
		t.Fatalf("plan %d %+v", c, pr)
	}
	cyc := `{"tasks":[{"id":"fa","intent":"Add feature A to pkg with a test","scope":["pkg/a*"],"after":["fb"]},{"id":"fb","intent":"Add feature B to pkg with a test","scope":["pkg/b*"],"after":["fa"]}]}`
	good := `{"notes":"A first.","tasks":[{"id":"fa","title":"Add A","intent":"Add feature A to pkg with a test","scope":["pkg/a*"]},{"id":"fb","title":"Add B","intent":"Add feature B to pkg with a test","scope":["pkg/b*"],"after":["fa"]}]}`
	writePlan(t, h, map[string]map[string]any{
		pr.ID: {"r0": map[string]any{"write": map[string]string{"spine-plan.json": cyc}}, "r1": map[string]any{"write": map[string]string{"spine-plan.json": good}}},
		"fa":  {"*": map[string]any{"write": map[string]string{"pkg/a.go": goFile("A")}}},
		"fb":  {"*": map[string]any{"write": map[string]string{"pkg/b.go": goFile("B")}}},
	})
	d.do("POST", "/api/repos/app/resume", nil, &map[string]any{})
	tk := waitPhase(t, a, api.TaskID(pr.ID), api.PhaseEscalated, "")
	if tk.Status.Reason != "PlanReady" || tk.Status.Plan == nil || len(tk.Status.Plan.Tasks) != 2 || tk.Status.PatchRound != 1 {
		t.Fatalf("plan task %+v", tk.Status)
	}
	prompts, _ := os.ReadFile(filepath.Join(h.log, pr.ID+".log"))
	if !strings.Contains(string(prompts), "spine-kind: plan") || !strings.Contains(string(prompts), "dependency cycle") {
		t.Fatalf("planner prompts:\n%s", prompts)
	}
	if s, _ := a.Store.ListLeases(context.Background(), storage.LeaseReady); len(s) != 0 {
		t.Fatalf("planner kept a worktree: %+v", s)
	}
	// Validate an edited plan with a mistake, then submit the fixed one.
	p := *tk.Status.Plan
	p.Tasks[1].After = []string{"ghost"}
	var vr struct {
		OK     bool
		Issues []struct{ Level, Message string }
	}
	d.do("POST", "/api/repos/app/plans/validate", map[string]any{"plan": p}, &vr)
	if vr.OK || !strings.Contains(vr.Issues[0].Message, "ghost") {
		t.Fatalf("validate %+v", vr)
	}
	if c := d.do("POST", "/api/repos/app/plans/submit", map[string]any{"plan": p, "plan_task": pr.ID}, &map[string]any{}); c != 422 {
		t.Fatalf("invalid plan submitted: %d", c)
	}
	// Edits saved during review survive leaving the page.
	saved := p
	saved.Notes = "edited during review"
	if c := d.do("POST", "/api/repos/app/plans/save", map[string]any{"plan": saved, "plan_task": pr.ID}, &map[string]any{}); c != 200 {
		t.Fatalf("save %d", c)
	}
	for deadline := time.Now().Add(10 * time.Second); ; {
		x, _ := a.Store.GetTask(context.Background(), api.TaskID(pr.ID))
		if x.Status.Plan != nil && x.Status.Plan.Notes == "edited during review" && x.Status.Phase == api.PhaseEscalated {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("saved plan not stored: %+v", x.Status.Plan)
		}
		time.Sleep(50 * time.Millisecond)
	}
	p.Tasks[1].After = []string{"fa"}
	p.Tasks[1].Title = "Add B (edited)"
	var sub struct{ Submitted []string }
	if c := d.do("POST", "/api/repos/app/plans/submit", map[string]any{"plan": p, "plan_task": pr.ID}, &sub); c != 200 || len(sub.Submitted) != 2 || sub.Submitted[0] != "fa" {
		t.Fatalf("submit %d %+v", c, sub)
	}
	waitPhase(t, a, api.TaskID(pr.ID), api.PhasePlanned, "")
	var g struct {
		Nodes []struct {
			ID    string
			Deps  []string
			State string
		}
	}
	d.do("GET", "/api/graph", nil, &g)
	found := false
	for _, n := range g.Nodes {
		if n.ID == "fb" && len(n.Deps) == 1 && n.Deps[0] == "app/fa" {
			found = true
		}
	}
	if !found {
		t.Fatalf("graph %+v", g)
	}
	fa := waitPhase(t, a, "fa", api.PhaseIntegrated, "")
	fb := waitPhase(t, a, "fb", api.PhaseIntegrated, "")
	if fb.Spec.Title != "Add B (edited)" || !fb.Status.StartedAt.After(fa.Status.StartedAt) {
		t.Fatalf("fb %+v", fb.Spec)
	}
}
