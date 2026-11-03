package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/patcarter883/spine-next/internal/api"
	"github.com/patcarter883/spine-next/internal/auth"
	"github.com/patcarter883/spine-next/internal/config"
	"github.com/patcarter883/spine-next/internal/gate"
	"github.com/patcarter883/spine-next/internal/gitro"
	"github.com/patcarter883/spine-next/internal/inbox"
	"github.com/patcarter883/spine-next/internal/plan"
	"github.com/patcarter883/spine-next/internal/supervisor"
	"github.com/patcarter883/spine-next/internal/worktree"
)

// runUsers manages dashboard users: add NAME [--role R] | list | remove NAME.
func runUsers(ctx context.Context, args []string) error {
	st := auth.Store{Path: filepath.Join(supervisor.Home(), "users.yaml")}
	if len(args) == 0 {
		args = []string{"list"}
	}
	switch args[0] {
	case "add":
		fs := flag.NewFlagSet("users add", flag.ContinueOnError)
		role := fs.String("role", "operator", "viewer (read), operator (task actions) or admin (repositories, settings)")
		pos, err := parseInterspersed(fs, args[1:])
		if err != nil {
			return err
		}
		if len(pos) != 1 {
			return errors.New("users add NAME [--role viewer|operator|admin]")
		}
		r, err := auth.ParseRole(*role)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(supervisor.Home(), 0o755); err != nil {
			return err
		}
		tok, err := st.Add(pos[0], r)
		if err != nil {
			return err
		}
		fmt.Printf("user %s (%s) added. Token, shown once:\n\n  %s\n\nSign in to the dashboard with it, or send it as `Authorization: Bearer <token>`.\nThe dashboard now requires sign-in.\n", pos[0], r, tok)
		return nil
	case "list", "ls":
		us, err := st.Load()
		if err != nil {
			return err
		}
		if len(us) == 0 {
			fmt.Println("no users: the dashboard serves only loopback addresses, without sign-in")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tROLE\tCREATED")
		for _, u := range us {
			fmt.Fprintf(w, "%s\t%s\t%s\n", u.Name, u.Role, u.Created.Format("2006-01-02"))
		}
		return w.Flush()
	case "remove", "rm":
		if len(args) != 2 {
			return errors.New("users remove NAME")
		}
		return st.Remove(args[1])
	}
	return fmt.Errorf("users: unknown subcommand %q (add, list, remove)", args[0])
}

// runPause pauses or resumes a repository: no new task starts while paused.
func runPause(ctx context.Context, pause bool, args []string) error {
	fs := flag.NewFlagSet("pause", flag.ContinueOnError)
	repo := fs.String("repo", ".", "repository")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	cd, err := worktree.ResolveCommonDir(*repo)
	if err != nil {
		return err
	}
	if !pause {
		if err := inbox.Resume(inbox.Dir(cd)); err != nil {
			return err
		}
		fmt.Println("resumed")
		return nil
	}
	if err := inbox.Pause(inbox.Dir(cd), inbox.PauseInfo{By: os.Getenv("USER"), Reason: strings.Join(pos, " ")}); err != nil {
		return err
	}
	fmt.Println("paused: running tasks finish; no new task starts until `spine resume`")
	return nil
}

// runPlan: spine plan "goal" submits a planner task; spine plan validate
// FILE checks a plan; spine plan submit FILE validates and submits it.
func runPlan(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("plan", flag.ContinueOnError)
	repo := fs.String("repo", ".", "repository")
	adapter := fs.String("agent", "", "planner agent (default: config agent)")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		return errors.New(`plan "goal" | plan validate FILE | plan submit FILE`)
	}
	primary, err := worktree.PrimaryOf(ctx, *repo)
	if err != nil {
		return err
	}
	cfg, err := config.Load(primary)
	if err != nil {
		return err
	}
	running, db, cd, err := controllerRunning(*repo)
	if err != nil {
		return err
	}
	if pos[0] == "validate" || pos[0] == "submit" {
		if len(pos) != 2 {
			return fmt.Errorf("plan %s FILE", pos[0])
		}
		b, err := os.ReadFile(pos[1])
		if err != nil {
			return err
		}
		var p api.Plan
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&p); err != nil {
			return fmt.Errorf("%s: %w", pos[1], err)
		}
		st, err := openRead(ctx, *repo)
		if err != nil {
			return err
		}
		defer st.Close()
		env := plan.Env{Agents: cfg.AdapterNames(), TaskPhase: func(id string) (api.Phase, bool) {
			t, err := st.GetTask(ctx, api.TaskID(id))
			return t.Status.Phase, err == nil
		}}
		gc, gerr := func() (gate.Config, error) {
			c, ok, err := gate.LoadConfig(ctx, primary, cfg.Base)
			if !ok && err == nil {
				return gate.DefaultConfig(true), nil
			}
			return c, err
		}()
		if gerr == nil {
			env.Gates = gc.Order
		}
		if out, err := (gitro.Repo{Dir: primary}).Run(ctx, "ls-tree", "-r", "-z", "--name-only", cfg.Base); err == nil {
			for _, f := range strings.Split(string(out), "\x00") {
				if f != "" {
					env.Files = append(env.Files, f)
				}
			}
		}
		issues := plan.Validate(p, env)
		for _, i := range issues {
			fmt.Println(i)
		}
		if plan.HasErrors(issues) {
			return errors.New("the plan has errors")
		}
		if pos[0] == "validate" {
			fmt.Printf("ok: %d tasks\n", len(p.Tasks))
			return nil
		}
		for _, t := range p.Tasks {
			in := config.TaskInput{Title: t.Title, Intent: t.Intent, Scope: t.Scope, After: t.After, Agent: t.Agent,
				RequireApproval: t.RequireApproval, MaxAttempts: t.MaxAttempts, MaxRounds: t.MaxRounds, MaxTokens: t.MaxTokens,
				CreatedBy: os.Getenv("USER")}
			for _, n := range t.Gates {
				g := gc.Gates[n]
				g.Name = n
				in.Acceptance = append(in.Acceptance, g)
			}
			spec, err := cfg.NewSpec(in)
			if err != nil {
				return fmt.Errorf("%s: %w", t.ID, err)
			}
			if _, err := inbox.Write(inbox.Dir(cd), inbox.Command{Kind: inbox.Submit, Task: api.TaskID(t.ID), Spec: &spec, By: os.Getenv("USER")}); err != nil {
				return err
			}
			fmt.Printf("submitted %s\n", t.ID)
		}
		if !running {
			fmt.Println("no controller is running; tasks start when one does")
		}
		return nil
	}
	goal := strings.TrimSpace(strings.Join(pos, " "))
	spec, err := cfg.NewSpec(config.TaskInput{Intent: goal, Title: "Plan: " + firstLine(goal), Agent: *adapter, MaxAttempts: 1, MaxRounds: 2,
		CreatedBy: os.Getenv("USER")})
	if err != nil {
		return err
	}
	spec.Kind = api.KindPlan
	id := api.TaskID("plan-" + string(api.NewTaskID(goal)))
	if len(id) > 64 {
		id = id[:64]
	}
	if _, err := inbox.Write(inbox.Dir(cd), inbox.Command{Kind: inbox.Submit, Task: id, Spec: &spec, By: os.Getenv("USER")}); err != nil {
		return err
	}
	fmt.Printf("submitted planner task %s; review the plan in the dashboard (Plan view) or with `spine status %s`\n", id, id)
	if !running {
		fmt.Println("no controller is running; it starts when one does")
		return nil
	}
	return follow(ctx, db, id)
}
