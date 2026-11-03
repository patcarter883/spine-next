package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"text/tabwriter"
	"time"

	"github.com/patcarter883/spine-next/internal/supervisor"
	"github.com/patcarter883/spine-next/internal/ui"
)

func runRepos(ctx context.Context, args []string) error {
	if len(args) == 0 {
		args = []string{"list"}
	}
	reg := supervisor.DefaultRegistry()
	switch args[0] {
	case "add":
		fs := flag.NewFlagSet("repos add", flag.ContinueOnError)
		name := fs.String("name", "", "name in the supervisor (default: the checkout's directory name)")
		pos, err := parseInterspersed(fs, args[1:])
		if err != nil {
			return err
		}
		dir := "."
		if len(pos) > 0 {
			dir = pos[0]
		}
		r, err := reg.Add(ctx, dir, *name)
		if err != nil {
			return err
		}
		fmt.Printf("registered %s → %s\n", r.Name, r.Path)
		fmt.Println("a running supervisor picks it up within seconds; otherwise start one with `spine supervisor`")
		return nil
	case "remove", "rm":
		if len(args) != 2 {
			return errors.New("repos remove: give a repository name")
		}
		if err := reg.Remove(args[1]); err != nil {
			return err
		}
		fmt.Printf("unregistered %s (its tasks, database and worktrees are untouched)\n", args[1])
		return nil
	case "list", "ls":
		repos, err := reg.Load()
		if err != nil {
			return err
		}
		if len(repos) == 0 {
			fmt.Println("no repositories registered; add one with `spine repos add [path]`")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tPATH")
		for _, r := range repos {
			fmt.Fprintf(w, "%s\t%s\n", r.Name, r.Path)
		}
		return w.Flush()
	}
	return fmt.Errorf("repos: unknown subcommand %q (add, list, remove)", args[0])
}

func runSupervisor(ctx context.Context, args []string) error {
	sub := "run"
	if len(args) > 0 && args[0] != "" && args[0][0] != '-' {
		sub, args = args[0], args[1:]
	}
	cfg, err := supervisor.LoadConfig()
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("supervisor "+sub, flag.ContinueOnError)
	addr := fs.String("addr", cfg.Addr, "dashboard address (loopback by default: it has no authentication)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg.Addr = *addr
	switch sub {
	case "run":
		return supervisorRun(ctx, cfg)
	case "status":
		return supervisorStatus(ctx, cfg.Addr)
	case "install":
		return supervisorInstall()
	}
	return fmt.Errorf("supervisor: unknown subcommand %q (run, status, install)", sub)
}

type supStatus struct {
	Repos  []supervisor.RepoStatus `json:"repos"`
	Limits map[string]any          `json:"limits"`
}

func supervisorRun(ctx context.Context, cfg supervisor.Config) error {
	s := &supervisor.Supervisor{Registry: supervisor.DefaultRegistry(), Config: cfg, Logf: logf}
	src := &supervisor.UISource{Sup: s}
	defer src.Close()
	mux := http.NewServeMux()
	mux.Handle("/", (&ui.Server{Source: src, Multi: true}).Handler())
	mux.HandleFunc("GET /api/supervisor", func(w http.ResponseWriter, r *http.Request) {
		st := supStatus{Repos: s.Status(), Limits: map[string]any{"max_agents": cfg.MaxAgents, "max_gates": cfg.MaxGates}}
		if l := s.Limits(); l != nil {
			cur, _ := l.Usage()
			st.Limits["in_use"] = cur
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(st)
	})
	srv := &http.Server{Addr: cfg.Addr, Handler: mux}
	errc := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()
	defer srv.Close()
	repos, _ := s.Registry.Load()
	logf("supervisor: %d repositories, max %d agents / %d gates; dashboard http://%s", len(repos), cfg.MaxAgents, cfg.MaxGates, cfg.Addr)
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	select {
	case err := <-done:
		if errors.Is(err, supervisor.ErrAlreadyRunning) {
			return errors.New("a spine supervisor is already running for this user (`spine supervisor status`)")
		}
		return err
	case err := <-errc:
		return fmt.Errorf("dashboard: %w", err)
	}
}

func supervisorStatus(ctx context.Context, addr string) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://"+addr+"/api/supervisor", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("no supervisor answering at %s (start one with `spine supervisor`)", addr)
	}
	defer resp.Body.Close()
	var st supStatus
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tSTATE\tRESTARTS\tSINCE\tPATH")
	for _, r := range st.Repos {
		fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\n", r.Name, r.State, r.Restarts, time.Since(r.Since).Round(time.Second), r.Path)
		if r.Error != "" {
			fmt.Fprintf(w, "\t  %s\n", firstLine(r.Error))
		}
	}
	w.Flush()
	fmt.Printf("\nlimits: %v\ndashboard: http://%s\n", st.Limits, addr)
	return nil
}

// supervisorInstall writes a systemd user unit so the supervisor starts on
// login. It does not enable it: that is printed for the operator to run.
func supervisorInstall() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cfgDir, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	unitDir := filepath.Join(cfgDir, "systemd", "user")
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		return err
	}
	unit := fmt.Sprintf(`[Unit]
Description=Spine supervisor (all registered repositories)

[Service]
ExecStart=%s supervisor run
Restart=on-failure
RestartSec=5
# Agents need credentials and toolchains from your environment; add them here
# or in an EnvironmentFile, e.g.:
# EnvironmentFile=%%h/.config/spine/env

[Install]
WantedBy=default.target
`, exe)
	p := filepath.Join(unitDir, "spine-supervisor.service")
	if err := os.WriteFile(p, []byte(unit), 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %s\nenable it with:\n  systemctl --user daemon-reload && systemctl --user enable --now spine-supervisor\n", p)
	return nil
}

// parseInterspersed parses flags that may appear before or after positional
// arguments (Go's flag package stops at the first positional one).
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}
