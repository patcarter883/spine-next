package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/patcarter883/spine-next/internal/api"
	"github.com/patcarter883/spine-next/internal/app"
	"github.com/patcarter883/spine-next/internal/config"
	"github.com/patcarter883/spine-next/internal/inbox"
	"github.com/patcarter883/spine-next/internal/storage"
	"github.com/patcarter883/spine-next/internal/worktree"
)

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

// controllerRunning reports whether another process holds the store.
func controllerRunning(repo string) (bool, string, string, error) {
	db, cd, err := app.DBPath(repo)
	if err != nil {
		return false, "", "", err
	}
	return storage.IsLocked(db), db, cd, nil
}

func runRun(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	repo := fs.String("repo", ".", "repository (any checkout)")
	id := fs.String("id", "", "task id (default: derived from the intent)")
	title := fs.String("title", "", "commit title (default: first line of the intent)")
	base := fs.String("base", "", "branch to integrate into (default: config base, else main)")
	adapter := fs.String("agent", "", "agent adapter (default: config agent)")
	approve := fs.Bool("require-approval", false, "hold the verified task until `spine approve`")
	noWait := fs.Bool("no-wait", false, "submit and return without following the task")
	attempts := fs.Int("max-attempts", 0, "fresh attempts (default: config)")
	rounds := fs.Int("max-rounds", 0, "patch rounds per attempt (default: config)")
	timeout := fs.Duration("run-timeout", 0, "timeout per agent run (default: config)")
	maxTokens := fs.Int64("max-tokens", 0, "token budget for the task (default: config)")
	var scope, deps multi
	fs.Var(&scope, "scope", "path glob the task may change (repeatable; default: everything)")
	fs.Var(&deps, "after", "task id that must integrate first (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	intent := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if intent == "-" {
		b, err := readStdin()
		if err != nil {
			return err
		}
		intent = strings.TrimSpace(string(b))
	}
	if intent == "" {
		return errors.New("run: give the task intent as an argument (or - to read stdin)")
	}
	primary, err := worktree.PrimaryOf(ctx, *repo)
	if err != nil {
		return err
	}
	cfg, err := config.Load(primary)
	if err != nil {
		return err
	}
	spec, err := cfg.NewSpec(config.TaskInput{Title: *title, Intent: intent, Base: *base, Scope: scope, After: deps,
		Agent: *adapter, RequireApproval: *approve, MaxAttempts: *attempts, MaxRounds: *rounds, RunTimeout: *timeout,
		MaxTokens: *maxTokens, CreatedBy: os.Getenv("USER")})
	if err != nil {
		return err
	}
	tid := api.TaskID(*id)
	if tid == "" {
		tid = api.NewTaskID(intent)
	}
	if !api.ValidTaskID(tid) {
		return fmt.Errorf("run: task id %q must match [A-Za-z0-9][A-Za-z0-9._-]{0,63}", tid)
	}
	running, db, cd, err := controllerRunning(*repo)
	if err != nil {
		return err
	}
	if running || *noWait {
		if _, err := inbox.Write(inbox.Dir(cd), inbox.Command{Kind: inbox.Submit, Task: tid, Spec: &spec, By: os.Getenv("USER")}); err != nil {
			return err
		}
		fmt.Printf("submitted %s\n", tid)
		if !running {
			fmt.Println("no controller is running; start one with `spine controller` (or rerun without --no-wait)")
			return nil
		}
		if *noWait {
			return nil
		}
		return follow(ctx, db, tid)
	}
	// No controller: run one inline until this task finishes.
	a, err := app.Open(ctx, *repo, app.Options{Config: &cfg, Logf: logf})
	if err != nil {
		return err
	}
	defer a.Close()
	if _, err := a.Store.CreateTask(ctx, tid, spec); err != nil {
		return err
	}
	fmt.Printf("task %s\n", tid)
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Controller.Run(cctx) }()
	ferr := followStore(ctx, a.Store, tid)
	cancel()
	if err := <-done; err != nil {
		return err
	}
	return ferr
}

func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "spine: "+format+"\n", args...)
}

func readStdin() ([]byte, error) {
	var b []byte
	buf := make([]byte, 32<<10)
	for {
		n, err := os.Stdin.Read(buf)
		b = append(b, buf[:n]...)
		if err != nil {
			if len(b) > 0 || err.Error() == "EOF" {
				return b, nil
			}
			return b, err
		}
	}
}

func follow(ctx context.Context, db string, id api.TaskID) error {
	var st *storage.Store
	var err error
	for i := 0; i < 50; i++ {
		if st, err = storage.OpenReadOnly(ctx, db); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		return err
	}
	defer st.Close()
	return followStore(ctx, st, id)
}

// followStore prints the task's progress until it reaches a terminal phase.
// It returns an error unless the task integrated.
func followStore(ctx context.Context, st *storage.Store, id api.TaskID) error {
	var after int64
	waited := time.Now()
	for {
		evs, err := st.ListEvents(ctx, id, after, 500)
		if err == nil {
			for _, e := range evs {
				after = e.ID
				printEvent(e)
			}
		}
		t, err := st.GetTask(ctx, id)
		if errors.Is(err, storage.ErrNotFound) && time.Since(waited) < 30*time.Second {
			// submitted through the inbox; the controller has not ingested it yet
		} else if err != nil {
			return err
		} else if t.Status.Phase.Terminal() {
			if evs, err := st.ListEvents(ctx, id, after, 500); err == nil {
				for _, e := range evs {
					printEvent(e)
				}
			}
			printStatus(t)
			if t.Status.Phase != api.PhaseIntegrated {
				return fmt.Errorf("task %s ended %s (%s)", id, t.Status.Phase, t.Status.Reason)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			fmt.Printf("\nstopped following %s; it continues if a controller runs (`spine status %s`)\n", id, id)
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func printEvent(e storage.Event) {
	ts := e.At.Local().Format("15:04:05")
	var m map[string]any
	json.Unmarshal(e.Data, &m)
	switch e.Kind {
	case "transition":
		fmt.Printf("%s  %v → %v  %v %v\n", ts, m["from"], m["to"], m["reason"], strOr(m["message"]))
	case "agent_start":
		fmt.Printf("%s  agent %v started (attempt %v, round %v)\n", ts, m["adapter"], m["attempt"], m["round"])
	case "agent":
		switch m["kind"] {
		case "tool_call":
			fmt.Printf("%s    · %v\n", ts, m["title"])
		case "permission":
			fmt.Printf("%s    · permission %v: %v (%v)\n", ts, m["status"], m["title"], m["detail"])
		}
	case "outcome":
		fmt.Printf("%s  agent finished: %v %v (%v tokens)\n", ts, m["reason"], strOr(m["stop_reason"]), m["tokens_used"])
	case "verdict":
		v, _ := m["verdict"].(map[string]any)
		where := "branch"
		if m["on_merge"] == true {
			where = "merge result"
		}
		if v["passed"] == true {
			fmt.Printf("%s  gates passed on the %s\n", ts, where)
		} else {
			fmt.Printf("%s  gate %v failed on the %s\n", ts, v["failed_gate"], where)
			if gs, ok := v["gates"].([]any); ok {
				for _, g := range gs {
					gm, _ := g.(map[string]any)
					news, _ := gm["new"].([]any)
					for i, d := range news {
						if i == 5 {
							fmt.Printf("           … %d more\n", len(news)-5)
							break
						}
						dm, _ := d.(map[string]any)
						fmt.Printf("           %v %v %v\n", dm["code"], strOr(dm["file"]), firstLine(fmt.Sprint(dm["message"])))
					}
				}
			}
		}
	case "decision":
		d, _ := m["decision"].(map[string]any)
		fmt.Printf("%s  decision: %v (%v)\n", ts, d["action"], d["reason"])
	case "integrated":
		fmt.Printf("%s  integrated %v at %v\n", ts, m["base_ref"], short(fmt.Sprint(m["commit"])))
	case "gate_error", "reconcile_error", "teardown_error":
		fmt.Printf("%s  %s: %v\n", ts, e.Kind, m["error"])
	}
}

func strOr(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(s, "\n")
	if len(l) > 120 {
		l = l[:120] + "…"
	}
	return l
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func openRead(ctx context.Context, repo string) (*storage.Store, error) {
	db, _, err := app.DBPath(repo)
	if err != nil {
		return nil, err
	}
	st, err := storage.OpenReadOnly(ctx, db)
	if os.IsNotExist(err) {
		return nil, errors.New("no spine database in this repository yet (run `spine run ...` first)")
	}
	return st, err
}

func runStatus(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	repo := fs.String("repo", ".", "repository")
	asJSON := fs.Bool("json", false, "print the task as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("status: give a task id")
	}
	st, err := openRead(ctx, *repo)
	if err != nil {
		return err
	}
	defer st.Close()
	t, err := st.GetTask(ctx, api.TaskID(fs.Arg(0)))
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(t)
	}
	printStatus(t)
	return nil
}

func printStatus(t api.Task) {
	s := t.Status
	fmt.Printf("\ntask      %s\n", t.ID)
	fmt.Printf("phase     %s", s.Phase)
	if s.Reason != "" {
		fmt.Printf(" (%s)", s.Reason)
	}
	fmt.Println()
	if s.Message != "" {
		fmt.Printf("message   %s\n", s.Message)
	}
	fmt.Printf("intent    %s\n", firstLine(t.Spec.Intent))
	fmt.Printf("base      %s\n", t.Spec.BaseRef)
	fmt.Printf("attempt   %d/%d, round %d/%d, tokens %d\n", s.Attempt, t.Spec.Budget.WithDefaults().MaxAttempts,
		s.PatchRound, t.Spec.Budget.WithDefaults().MaxPatchRounds, s.TokensUsed)
	if s.Worktree != nil && !t.Status.Phase.Done() {
		fmt.Printf("worktree  %s\n", s.Worktree.Path)
	}
	if g := s.LastGate; g != nil {
		res := "passed"
		if !g.Passed {
			res = fmt.Sprintf("failed at %s (%d new failures)", g.FailedGate, g.NewCount)
		}
		fmt.Printf("gates     %s\n", res)
	}
	if o := s.LastOutcome; o != nil {
		fmt.Printf("agent     %s %s\n", o.Reason, o.StopReason)
	}
	if s.IntegratedCommit != "" {
		fmt.Printf("commit    %s\n", s.IntegratedCommit)
	}
	if p := s.Proposal; p != nil && s.Phase == api.PhaseEscalated {
		fmt.Printf("proposal  %s", p.Kind)
		if len(p.Scope) > 0 {
			fmt.Printf(" +%s", strings.Join(p.Scope, " +"))
		}
		if p.Text != "" {
			fmt.Printf(": %s", p.Text)
		}
		fmt.Printf("\n          (spine accept %s)\n", t.ID)
	}
	var conds []string
	for _, c := range s.Conditions {
		conds = append(conds, fmt.Sprintf("%s=%v", c.Type, c.Status))
	}
	sort.Strings(conds)
	if len(conds) > 0 {
		fmt.Printf("conds     %s\n", strings.Join(conds, " "))
	}
}

func runList(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	repo := fs.String("repo", ".", "repository")
	all := fs.Bool("all", false, "include integrated, failed and cancelled tasks")
	if err := fs.Parse(args); err != nil {
		return err
	}
	st, err := openRead(ctx, *repo)
	if err != nil {
		return err
	}
	defer st.Close()
	ts, err := st.ListTasks(ctx)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tPHASE\tREASON\tATTEMPT\tROUND\tAGE")
	for _, t := range ts {
		if !*all && t.Status.Phase.Done() {
			continue
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t%s\n", t.ID, t.Status.Phase, t.Status.Reason, t.Status.Attempt, t.Status.PatchRound,
			time.Since(t.CreatedAt).Round(time.Second))
	}
	return w.Flush()
}

func runLogs(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	repo := fs.String("repo", ".", "repository")
	fol := fs.Bool("f", false, "follow until the task finishes")
	raw := fs.Bool("json", false, "print raw events")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("logs: give a task id")
	}
	st, err := openRead(ctx, *repo)
	if err != nil {
		return err
	}
	defer st.Close()
	id := api.TaskID(fs.Arg(0))
	if *fol {
		return followStore(ctx, st, id)
	}
	evs, err := st.ListEvents(ctx, id, 0, 0)
	if err != nil {
		return err
	}
	for _, e := range evs {
		if *raw {
			b, _ := json.Marshal(e)
			fmt.Println(string(b))
		} else {
			printEvent(e)
		}
	}
	return nil
}

func runCommand(ctx context.Context, kind inbox.Kind, args []string) error {
	fs := flag.NewFlagSet(string(kind), flag.ContinueOnError)
	repo := fs.String("repo", ".", "repository")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	cmd := inbox.Command{Kind: kind, By: os.Getenv("USER")}
	switch kind {
	case inbox.Hint:
		if len(pos) < 2 {
			return fmt.Errorf("hint: spine hint ID \"the bug is in X\"")
		}
		cmd.Text = strings.TrimSpace(strings.Join(pos[1:], " "))
	case inbox.Rollback:
		if len(pos) != 2 {
			return fmt.Errorf("rollback: spine rollback ID SNAPSHOT (see `spine status ID`)")
		}
		if _, err := fmt.Sscanf(strings.TrimPrefix(pos[1], "#"), "%d", &cmd.Seq); err != nil {
			return fmt.Errorf("rollback: %q is not a snapshot number", pos[1])
		}
	case inbox.Retry:
		if len(pos) > 1 {
			cmd.Text = strings.TrimSpace(strings.Join(pos[1:], " "))
		}
	default:
		if len(pos) != 1 {
			return fmt.Errorf("%s: give a task id", kind)
		}
	}
	if len(pos) == 0 {
		return fmt.Errorf("%s: give a task id", kind)
	}
	cmd.Task = api.TaskID(pos[0])
	running, _, cd, err := controllerRunning(*repo)
	if err != nil {
		return err
	}
	if _, err := inbox.Write(inbox.Dir(cd), cmd); err != nil {
		return err
	}
	fmt.Printf("%s %s: queued\n", kind, pos[0])
	if !running {
		fmt.Println("no controller is running; it applies when `spine controller` starts")
	}
	return nil
}

func runAccept(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("accept", flag.ContinueOnError)
	repo := fs.String("repo", ".", "repository")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("accept: give a task id")
	}
	st, err := openRead(ctx, *repo)
	if err != nil {
		return err
	}
	t, err := st.GetTask(ctx, api.TaskID(pos[0]))
	st.Close()
	if err != nil {
		return err
	}
	cmd, err := inbox.Accept(t, os.Getenv("USER"))
	if err != nil {
		return err
	}
	running, _, cd, err := controllerRunning(*repo)
	if err != nil {
		return err
	}
	if _, err := inbox.Write(inbox.Dir(cd), cmd); err != nil {
		return err
	}
	fmt.Printf("accept %s: queued %s\n", pos[0], cmd.Kind)
	if !running {
		fmt.Println("no controller is running; it applies when `spine controller` starts")
	}
	return nil
}

func runController(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("controller", flag.ContinueOnError)
	repo := fs.String("repo", ".", "repository")
	if err := fs.Parse(args); err != nil {
		return err
	}
	a, err := app.Open(ctx, *repo, app.Options{Logf: logf})
	if errors.Is(err, storage.ErrLocked) {
		return errors.New("another spine controller is already running for this repository")
	}
	if err != nil {
		return err
	}
	defer a.Close()
	logf("controller running for %s (agent %s, %d parallel); Ctrl-C to stop", a.WT.Primary(), a.Config.Agent, a.Config.Parallel)
	return a.Controller.Run(ctx)
}

func runUI(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("ui", flag.ContinueOnError)
	repo := fs.String("repo", ".", "repository")
	addr := fs.String("addr", "127.0.0.1:8765", "listen address (non-loopback needs users: `spine users add`)")
	var d dashboardFlags
	d.register(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	src, closeSrc, err := newRepoSource(ctx, *repo)
	if err != nil {
		return err
	}
	defer closeSrc()
	s, err := newDashboard(src, false, nil, *addr, d)
	if err != nil {
		return err
	}
	return serveDashboard(ctx, s, *addr, d, nil)
}
