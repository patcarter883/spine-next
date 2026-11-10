// Command spine is the Spine v2 CLI.
//
//	spine run "intent" [flags]   submit a task (runs a controller inline if none is running)
//	spine controller             run the controller until interrupted
//	spine status ID | list | logs ID
//	spine approve ID | retry ID | cancel ID
//	spine doctor | recover | gc | version
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/patcarter883/spine-next/internal/app"
	"github.com/patcarter883/spine-next/internal/config"
	"github.com/patcarter883/spine-next/internal/doctor"
	"github.com/patcarter883/spine-next/internal/inbox"
	"github.com/patcarter883/spine-next/internal/storage"
	"github.com/patcarter883/spine-next/internal/worktree"
)

var version = "0.1.0-mvp"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	app.Version = version
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "init":
		err = runInit(ctx, args)
	case "run":
		err = runRun(ctx, args)
	case "ui":
		err = runUI(ctx, args)
	case "repos":
		err = runRepos(ctx, args)
	case "supervisor":
		err = runSupervisor(ctx, args)
	case "controller":
		err = runController(ctx, args)
	case "status":
		err = runStatus(ctx, args)
	case "list", "ls":
		err = runList(ctx, args)
	case "logs":
		err = runLogs(ctx, args)
	case "approve":
		err = runCommand(ctx, inbox.Approve, args)
	case "retry":
		err = runCommand(ctx, inbox.Retry, args)
	case "cancel":
		err = runCommand(ctx, inbox.Cancel, args)
	case "hint":
		err = runCommand(ctx, inbox.Hint, args)
	case "accept":
		err = runAccept(ctx, args)
	case "rollback":
		err = runCommand(ctx, inbox.Rollback, args)
	case "pause":
		err = runPause(ctx, true, args)
	case "resume":
		err = runPause(ctx, false, args)
	case "plan":
		err = runPlan(ctx, args)
	case "gates":
		err = runGates(ctx, args)
	case "modules":
		err = runModules(ctx, args)
	case "survey":
		err = runSurvey(ctx, args)
	case "users":
		err = runUsers(ctx, args)
	case "doctor":
		err = runDoctor(ctx, args)
	case "recover":
		err = runRecover(ctx, args)
	case "gc":
		err = runGC(ctx, args)
	case "version":
		fmt.Println("spine", version)
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "spine: unknown command %q\n", cmd)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "spine:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: spine <command> [flags]

commands:
  init        scaffold .spine/config.yaml and .spine/gates.yaml for this repository
  run         submit a task: spine run [--scope GLOB]... [--base BRANCH] [--require-approval] "intent"
              (runs a controller inline until the task finishes if none is running)
  ui          web dashboard on http://127.0.0.1:8765 [--read-only] [--addr A --tls-cert C --tls-key K]
  users       add NAME [--role viewer|operator|admin] | list | remove NAME: dashboard sign-in
  gates       check [REV] [--file F]: dry-run the gates on main (or REV) with no agent
  survey      refresh the repository context (.spine/modules.yaml, .spine/context.md) through a task
  modules     print the derived module map [--write] [--render N]
  plan        "goal": a planner agent proposes tasks to review | validate FILE | submit FILE
  repos       add [PATH] [--name N] | list | remove NAME: repositories the supervisor runs
  supervisor  run | status | install: one process for every registered repository,
              with supervisor-wide agent/gate limits and one dashboard (~/.config/spine/supervisor.yaml)
  controller  run the controller for this repository until interrupted
  status ID   show a task            list [--all]   list tasks
  logs ID     show a task's events (-f to follow)
  approve ID  integrate a task held for approval
  retry ID    give an escalated or failed task another attempt
  cancel ID   stop a task and release its worktree
  hint ID "text"       give the next repair round a hint (resumes an escalated task)
  accept ID            do what the supervisor proposed for an escalated task
  rollback ID SNAPSHOT continue a stopped task from one of its snapshots
  pause [reason] | resume   stop/allow starting new tasks in this repository
  doctor    check environment, store and repository invariants
  recover   repair state after a crash
  gc        checkpoint the SQLite WAL; --days N also removes old snapshot refs,
            stray worktrees and tool-cache entries (--dry-run lists them)
  version   print the version
`)
}

func repoFlag(name string, args []string) (string, *flag.FlagSet, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	repo := fs.String("repo", ".", "repository (any checkout)")
	if err := fs.Parse(args); err != nil {
		return "", nil, err
	}
	return *repo, fs, nil
}

func runDoctor(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	repo := fs.String("repo", ".", "repository (any checkout)")
	probe := fs.Bool("agent", false, "also start the agent adapter and check its ACP handshake (no prompt is sent)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	failed := false
	fs2 := append(doctor.Run(ctx, *repo), doctor.Agent(ctx, *repo, *probe)...)
	for _, f := range append(fs2, doctor.Context(ctx, *repo)...) {
		fmt.Printf("%-4s  %-10s  %s\n", f.Level, f.Check, f.Message)
		failed = failed || f.Level == doctor.Fail
	}
	if failed {
		return fmt.Errorf("doctor found problems")
	}
	return nil
}

func openManager(ctx context.Context, repo string) (*worktree.Manager, *storage.Store, error) {
	cd, err := worktree.ResolveCommonDir(repo)
	if err != nil {
		return nil, nil, err
	}
	st, err := storage.Open(ctx, worktree.DatabasePath(cd), storage.Options{})
	if err != nil {
		return nil, nil, err
	}
	m, err := worktree.New(ctx, worktree.Config{RepoDir: repo, Store: st})
	if err != nil {
		st.Close()
		return nil, nil, err
	}
	return m, st, nil
}

func runRecover(ctx context.Context, args []string) error {
	repo, _, err := repoFlag("recover", args)
	if err != nil {
		return err
	}
	m, st, err := openManager(ctx, repo)
	if err != nil {
		return err
	}
	defer st.Close()
	rep, rerr := m.Recover(ctx)
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(rep)
	return rerr
}

func runGC(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("gc", flag.ContinueOnError)
	repo := fs.String("repo", ".", "repository (any checkout)")
	days := fs.Int("days", 0, "also remove snapshot refs of tasks finished more than N days ago, stray worktrees and older tool-cache entries")
	dry := fs.Bool("dry-run", false, "with --days: list what would be removed")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cd, err := worktree.ResolveCommonDir(*repo)
	if err != nil {
		return err
	}
	db := worktree.DatabasePath(cd)
	if *days > 0 && (*dry || storage.IsLocked(db)) {
		if !*dry {
			// The controller holds the locks GC needs: ask it.
			if _, err := inbox.Write(inbox.Dir(cd), inbox.Command{Kind: inbox.GC, Days: *days, By: os.Getenv("USER")}); err != nil {
				return err
			}
			fmt.Println("gc queued for the running controller")
			return nil
		}
		st, err := storage.OpenReadOnly(ctx, db)
		if err != nil {
			return err
		}
		defer st.Close()
		primary, err := worktree.PrimaryOf(ctx, *repo)
		if err != nil {
			return err
		}
		root := worktree.DefaultRoot(cd)
		if cfg, err := config.Load(primary); err == nil && cfg.WorktreeRoot != "" {
			root = cfg.WorktreeRoot
		}
		rep, err := worktree.PlanGC(ctx, primary, root, st, *days)
		if err != nil {
			return err
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}
	if *days > 0 {
		m, st, err := openManager(ctx, *repo)
		if err != nil {
			return err
		}
		defer st.Close()
		rep, err := m.GC(ctx, *days)
		if err != nil {
			return err
		}
		fmt.Printf("removed %d snapshot refs, %d stray worktrees, %d tool-cache entries\n", len(rep.SnapshotRefs), len(rep.StrayWorktrees), rep.OldCacheRows)
		for _, e := range rep.Errors {
			fmt.Println("  error:", e)
		}
		return nil
	}
	st, err := storage.Open(ctx, db, storage.Options{})
	if err != nil {
		return err
	}
	defer st.Close()
	return st.Checkpoint(ctx)
}
