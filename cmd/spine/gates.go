package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/patcarter883/spine-next/internal/api"
	"github.com/patcarter883/spine-next/internal/app"
	"github.com/patcarter883/spine-next/internal/config"
	"github.com/patcarter883/spine-next/internal/gate"
	"github.com/patcarter883/spine-next/internal/inbox"
	"github.com/patcarter883/spine-next/internal/modmap"
	"github.com/patcarter883/spine-next/internal/tools/ast"
	"github.com/patcarter883/spine-next/internal/worktree"
)

// runGates: spine gates check [REV] [--file F] [--json]. Dry-runs the
// gates with no agent: the onboarding prerequisite.
func runGates(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] != "check" {
		return errors.New("gates check [REV] [--file .spine/gates.yaml] [--worktree DIR] [--json]")
	}
	fs := flag.NewFlagSet("gates check", flag.ContinueOnError)
	repo := fs.String("repo", ".", "repository")
	file := fs.String("file", "", "check this gates file instead of the base's (e.g. a draft before committing it)")
	asJSON := fs.Bool("json", false, "print JSON")
	wt := fs.String("worktree", "", "a provisioned checkout to link the gates file's baseline.link entries from (vendor, .env), as a task's worktree is")
	pos, err := parseInterspersed(fs, args[1:])
	if err != nil {
		return err
	}
	primary, err := worktree.PrimaryOf(ctx, *repo)
	if err != nil {
		return err
	}
	cfg, err := config.Load(primary)
	if err != nil {
		return err
	}
	rev := strings.TrimPrefix(cfg.Base, "refs/heads/")
	if len(pos) > 0 {
		rev = pos[0]
	}
	gc, source, err := loadCheckGates(ctx, primary, cfg.Base, *file)
	if err != nil {
		return err
	}
	_, cd, err := app.DBPath(primary)
	if err != nil {
		return err
	}
	rep, err := app.GateEngine(cd).Check(ctx, primary, rev, gc, source, *wt)
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(rep)
	} else {
		fmt.Print(rep)
	}
	if !rep.OK {
		return errors.New("some gates fail on this revision: no task could pass them")
	}
	return nil
}

func loadCheckGates(ctx context.Context, primary, base, file string) (gate.Config, string, error) {
	if file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return gate.Config{}, "", err
		}
		c, err := gate.ParseConfig(b)
		return c, file, err
	}
	c, ok, err := gate.LoadConfig(ctx, primary, base)
	if err != nil {
		return c, "", err
	}
	src := gate.ConfigPath + " at " + strings.TrimPrefix(base, "refs/heads/")
	if !ok {
		return gate.DefaultConfig(ast.Available()), "built-in defaults (no " + gate.ConfigPath + " at " + strings.TrimPrefix(base, "refs/heads/") + ")", nil
	}
	return c, src, nil
}

// runModules: spine modules [--rev R] [--write] [--json]. Derives the
// module map; --write puts it in the checkout for review (the survey task
// does this through gates and approval instead).
func runModules(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("modules", flag.ContinueOnError)
	repo := fs.String("repo", ".", "repository")
	rev := fs.String("rev", "", "revision (default: the base branch)")
	write := fs.Bool("write", false, "write .spine/modules.yaml into the checkout (keeping labels and caveats)")
	asJSON := fs.Bool("json", false, "print JSON")
	tokens := fs.Int("render", 0, "print the planner's view of the map, capped at N tokens")
	if err := fs.Parse(args); err != nil {
		return err
	}
	primary, err := worktree.PrimaryOf(ctx, *repo)
	if err != nil {
		return err
	}
	cfg, err := config.Load(primary)
	if err != nil {
		return err
	}
	r := *rev
	if r == "" {
		r = cfg.Base
	}
	prev, _, _ := modmap.Load(ctx, primary, r)
	m, err := modmap.Derive(ctx, primary, r, prev, modmap.Options{})
	if err != nil {
		return err
	}
	switch {
	case *tokens > 0:
		fmt.Print(m.Render(*tokens))
	case *asJSON:
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(m)
	case *write:
		b, err := m.Marshal()
		if err != nil {
			return err
		}
		p := filepath.Join(primary, modmap.Path)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			return err
		}
		fmt.Printf("wrote %s: %d modules, %d co-change pairs; review and commit it (or run `spine survey` to label it through a task)\n", modmap.Path, len(m.Modules), len(m.CoChange))
	default:
		b, err := m.Marshal()
		if err != nil {
			return err
		}
		os.Stdout.Write(b)
	}
	return nil
}

// runSurvey submits a survey task: onboarding stage 2, or a refresh.
func runSurvey(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("survey", flag.ContinueOnError)
	repo := fs.String("repo", ".", "repository")
	adapter := fs.String("agent", "", "agent (default: config agent)")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	primary, err := worktree.PrimaryOf(ctx, *repo)
	if err != nil {
		return err
	}
	cfg, err := config.Load(primary)
	if err != nil {
		return err
	}
	spec, err := cfg.SurveySpec(strings.Join(pos, " "), *adapter, os.Getenv("USER"))
	if err != nil {
		return err
	}
	running, db, cd, err := controllerRunning(*repo)
	if err != nil {
		return err
	}
	id := api.TaskID("survey-" + time.Now().UTC().Format("20060102-150405"))
	if _, err := inbox.Write(inbox.Dir(cd), inbox.Command{Kind: inbox.Submit, Task: id, Spec: &spec, By: os.Getenv("USER")}); err != nil {
		return err
	}
	fmt.Printf("submitted %s: it derives the module map, an agent labels it and writes .spine/context.md, the gates check both, and it waits for your approval (`spine approve %s`)\n", id, id)
	if !running {
		fmt.Println("no controller is running; it starts when one does")
		return nil
	}
	return follow(ctx, db, id)
}
