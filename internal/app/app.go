// Package app wires Spine's components for one repository: store, worktree
// manager, tool registry, gate engine, agent runner and controller. The CLI
// and the end-to-end tests share it.
package app

import (
	"context"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"os"
	"path/filepath"

	"github.com/patcarter883/spine-next/internal/agent"
	"github.com/patcarter883/spine-next/internal/api"
	"github.com/patcarter883/spine-next/internal/artifacts"
	"github.com/patcarter883/spine-next/internal/config"
	"github.com/patcarter883/spine-next/internal/gate"
	"github.com/patcarter883/spine-next/internal/reconciler"
	"github.com/patcarter883/spine-next/internal/storage"
	"github.com/patcarter883/spine-next/internal/tools"
	"github.com/patcarter883/spine-next/internal/tools/ast"
	"github.com/patcarter883/spine-next/internal/tools/build"
	"github.com/patcarter883/spine-next/internal/tools/builtin"
	"github.com/patcarter883/spine-next/internal/worktree"
)

// Version is set by the CLI.
var Version = "dev"

// Options adjusts Open.
type Options struct {
	Config  *config.Config // nil = load .spine/config.yaml
	Logf    func(string, ...any)
	Workers int
	// Set by the multi-repo supervisor.
	Repo       string
	Limits     reconciler.Limiter
	ResolveDep func(ctx context.Context, repo string, id api.TaskID) (api.Phase, error)
	// Supervisor overrides the configured decision supervisor (tests).
	Supervisor reconciler.Supervisor
}

// App is an opened repository. It holds the database writer lock.
type App struct {
	Repo       string
	CommonDir  string
	Config     config.Config
	Store      *storage.Store
	WT         *worktree.Manager
	Registry   *tools.Registry
	Gate       *gate.Engine
	Runner     *agent.Runner
	Controller *reconciler.Controller
}

// Paths for a repository.
func DBPath(repo string) (string, string, error) {
	cd, err := worktree.ResolveCommonDir(repo)
	if err != nil {
		return "", "", err
	}
	return worktree.DatabasePath(cd), cd, nil
}

// Open opens repo for control (takes the writer lock).
func Open(ctx context.Context, repo string, o Options) (*App, error) {
	db, cd, err := DBPath(repo)
	if err != nil {
		return nil, err
	}
	st, err := storage.Open(ctx, db, storage.Options{})
	if err != nil {
		return nil, err
	}
	a := &App{Repo: repo, CommonDir: cd, Store: st}
	fail := func(err error) (*App, error) { st.Close(); return nil, err }

	primary, err := worktree.PrimaryOf(ctx, repo)
	if err != nil {
		return fail(err)
	}
	if o.Config != nil {
		a.Config = *o.Config
	} else if a.Config, err = config.Load(primary); err != nil {
		return fail(err)
	}
	if a.WT, err = worktree.New(ctx, worktree.Config{RepoDir: repo, Store: st, Root: a.Config.WorktreeRoot,
		Hooks: a.Config.Hooks, Identity: a.Config.Identity}); err != nil {
		return fail(err)
	}
	cache, _ := os.UserCacheDir()
	if cache == "" {
		cache = os.TempDir()
	}
	scratch := filepath.Join(cache, "spine", "scratch")
	a.Registry = builtin.New(tools.Options{Cache: tools.StoreCache{S: st}},
		&build.Runner{ScratchDir: scratch, Artifacts: &artifacts.Store{Dir: filepath.Join(cd, "spine", "artifacts")}})
	a.Gate = &gate.Engine{Reg: a.Registry, ScratchDir: scratch}
	a.Runner = &agent.Runner{Procs: a.WT, Version: Version}
	sup := o.Supervisor
	if sup == nil && a.Config.Supervisor.Type == "llm" {
		// Credentials come from the environment (ANTHROPIC_API_KEY, an
		// `ant auth login` profile, ...), resolved by the SDK.
		sup = &reconciler.LLMSupervisor{Client: anthropic.NewClient(), Model: a.Config.Supervisor.Model,
			Effort: a.Config.Supervisor.Effort, Timeout: a.Config.Supervisor.Timeout}
	}
	a.Controller = reconciler.New(reconciler.Deps{Store: st, WT: a.WT, Gate: a.Gate, Runner: a.Runner, Config: a.Config,
		Supervisor: sup, Logf: o.Logf, Workers: o.Workers, ParseAvailable: ast.Available(),
		Limits: o.Limits, ResolveDep: o.ResolveDep, Repo: o.Repo})
	return a, nil
}

// Close releases the database.
func (a *App) Close() error { return a.Store.Close() }
