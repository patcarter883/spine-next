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
  ui          web dashboard on http://127.0.0.1:8765 (read-only + approve/retry/cancel)
  repos       add [PATH] [--name N] | list | remove NAME: repositories the supervisor runs
  supervisor  run | status | install: one process for every registered repository,
              with supervisor-wide agent/gate limits and one dashboard (~/.config/spine/supervisor.yaml)
  controller  run the controller for this repository until interrupted
  status ID   show a task            list [--all]   list tasks
  logs ID     show a task's events (-f to follow)
  approve ID  integrate a task held for approval
  retry ID    give an escalated or failed task another attempt
  cancel ID   stop a task and release its worktree
  doctor    check environment, store and repository invariants
  recover   repair state after a crash
  gc        checkpoint the SQLite WAL
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
	for _, f := range append(doctor.Run(ctx, *repo), doctor.Agent(ctx, *repo, *probe)...) {
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
	repo, _, err := repoFlag("gc", args)
	if err != nil {
		return err
	}
	cd, err := worktree.ResolveCommonDir(repo)
	if err != nil {
		return err
	}
	st, err := storage.Open(ctx, worktree.DatabasePath(cd), storage.Options{})
	if err != nil {
		return err
	}
	defer st.Close()
	return st.Checkpoint(ctx)
}
