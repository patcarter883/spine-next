// Command spine is the Spine v2 CLI. M0 ships the operational commands:
//
//	spine doctor  [--repo DIR]   check environment, store and repository invariants
//	spine recover [--repo DIR]   repair state after a crash (what a controller runs at boot)
//	spine gc      [--repo DIR]   checkpoint the SQLite WAL
//	spine version
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/patcarter883/spine-next/internal/doctor"
	"github.com/patcarter883/spine-next/internal/storage"
	"github.com/patcarter883/spine-next/internal/worktree"
)

var version = "0.0.0-m0"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
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
	repo, _, err := repoFlag("doctor", args)
	if err != nil {
		return err
	}
	failed := false
	for _, f := range doctor.Run(ctx, repo) {
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
