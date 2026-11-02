// Package config loads .spine/config.yaml: operator settings for the
// controller (agent adapters, parallelism, default budget, base branch,
// provisioning hooks). It is read from the primary checkout's working tree,
// which tasks never write; gates are read from the base commit instead
// (internal/gate).
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/patcarter883/spine-next/internal/agent"
	"github.com/patcarter883/spine-next/internal/api"
	"github.com/patcarter883/spine-next/internal/worktree"
)

// Path is the config file location relative to the repository root.
const Path = ".spine/config.yaml"

// Config is the parsed file with defaults applied.
type Config struct {
	Agent        string                   // default adapter name
	Adapters     map[string]agent.Adapter // by name
	Parallel     int                      // concurrent tasks holding a worktree
	Base         string                   // default base ref (full)
	Budget       api.Budget
	Hooks        []worktree.Hook
	Identity     worktree.Identity
	WorktreeRoot string
	Resync       time.Duration
	Supervisor   Supervisor
}

// Supervisor selects how failed rounds are decided (RFC §3.5).
type Supervisor struct {
	Type    string        // "rules" (default) or "llm"
	Model   string        // llm: default claude-opus-5-5
	Effort  string        // llm: low|medium|high|xhigh|max (default medium)
	Timeout time.Duration // llm: per decision (default 2m)
}

type rawAdapter struct {
	Argv         []string          `yaml:"argv"`
	Env          map[string]string `yaml:"env"`
	PassEnv      []string          `yaml:"pass_env"`
	Mode         *string           `yaml:"mode"`
	Permissions  string            `yaml:"permissions"`
	StallTimeout string            `yaml:"stall_timeout"`
}

type rawBudget struct {
	MaxAttempts    int    `yaml:"max_attempts"`
	MaxPatchRounds int    `yaml:"max_patch_rounds"`
	MaxWallClock   string `yaml:"max_wall_clock"`
	MaxTokens      int64  `yaml:"max_tokens"`
	PerRunTimeout  string `yaml:"per_run_timeout"`
}

type rawHook struct {
	Argv    []string `yaml:"argv"`
	Timeout string   `yaml:"timeout"`
}

type raw struct {
	Agent        string                `yaml:"agent"`
	Adapters     map[string]rawAdapter `yaml:"adapters"`
	Parallel     int                   `yaml:"parallel"`
	Base         string                `yaml:"base"`
	Budget       rawBudget             `yaml:"budget"`
	Hooks        []rawHook             `yaml:"hooks"`
	Identity     worktree.Identity     `yaml:"identity"`
	WorktreeRoot string                `yaml:"worktree_root"`
	Resync       string                `yaml:"resync"`
	Supervisor   struct {
		Type    string `yaml:"type"`
		Model   string `yaml:"model"`
		Effort  string `yaml:"effort"`
		Timeout string `yaml:"timeout"`
	} `yaml:"supervisor"`
}

// Default returns the configuration used when no file exists.
func Default() Config {
	return Config{
		Agent:      "claude",
		Adapters:   map[string]agent.Adapter{"claude": agent.ClaudeAdapter()},
		Parallel:   4,
		Base:       "refs/heads/main",
		Budget:     api.Budget{}.WithDefaults(),
		Resync:     15 * time.Second,
		Supervisor: Supervisor{Type: "rules"},
	}
}

// Load reads repoRoot/.spine/config.yaml (absent = defaults).
func Load(repoRoot string) (Config, error) {
	b, err := os.ReadFile(filepath.Join(repoRoot, Path))
	if os.IsNotExist(err) {
		return Default(), nil
	}
	if err != nil {
		return Config{}, err
	}
	return Parse(b)
}

func dur(s, field string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("config: %s: bad duration %q", field, s)
	}
	return d, nil
}

// Parse parses config content.
func Parse(b []byte) (Config, error) {
	var r raw
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true)
	if err := dec.Decode(&r); err != nil && err.Error() != "EOF" {
		return Config{}, fmt.Errorf("config: %s: %w", Path, err)
	}
	c := Default()
	if r.Agent != "" {
		c.Agent = r.Agent
	}
	for name, ra := range r.Adapters {
		a := agent.Adapter{Name: name, Argv: ra.Argv, Env: ra.Env, PassEnv: ra.PassEnv, Permissions: ra.Permissions}
		if base, ok := c.Adapters[name]; ok { // overlay on a built-in
			if len(a.Argv) == 0 {
				a.Argv = base.Argv
			}
			if a.Permissions == "" {
				a.Permissions = base.Permissions
			}
			a.Mode = base.Mode
			a.StallTimeout = base.StallTimeout
		}
		if ra.Mode != nil {
			a.Mode = *ra.Mode
		}
		d, err := dur(ra.StallTimeout, "adapters."+name+".stall_timeout")
		if err != nil {
			return Config{}, err
		}
		if d > 0 {
			a.StallTimeout = d
		}
		if len(a.Argv) == 0 {
			return Config{}, fmt.Errorf("config: adapter %q has no argv", name)
		}
		switch a.Permissions {
		case "", "worktree", "allow", "reject":
		default:
			return Config{}, fmt.Errorf("config: adapter %q: permissions must be worktree, allow or reject", name)
		}
		c.Adapters[name] = a
	}
	if _, ok := c.Adapters[c.Agent]; !ok {
		return Config{}, fmt.Errorf("config: default agent %q is not defined", c.Agent)
	}
	if r.Parallel > 0 {
		c.Parallel = r.Parallel
	}
	if r.Base != "" {
		c.Base = FullRef(r.Base)
	}
	bud := api.Budget{MaxAttempts: r.Budget.MaxAttempts, MaxPatchRounds: r.Budget.MaxPatchRounds, MaxTokens: r.Budget.MaxTokens}
	var err error
	if bud.MaxWallClock, err = dur(r.Budget.MaxWallClock, "budget.max_wall_clock"); err != nil {
		return Config{}, err
	}
	if bud.PerRunTimeout, err = dur(r.Budget.PerRunTimeout, "budget.per_run_timeout"); err != nil {
		return Config{}, err
	}
	c.Budget = bud.WithDefaults()
	for i, h := range r.Hooks {
		d, err := dur(h.Timeout, fmt.Sprintf("hooks[%d].timeout", i))
		if err != nil {
			return Config{}, err
		}
		if len(h.Argv) == 0 {
			return Config{}, fmt.Errorf("config: hooks[%d] has no argv", i)
		}
		c.Hooks = append(c.Hooks, worktree.Hook{Argv: h.Argv, Timeout: d})
	}
	c.Identity = r.Identity
	c.WorktreeRoot = r.WorktreeRoot
	if d, err := dur(r.Resync, "resync"); err != nil {
		return Config{}, err
	} else if d > 0 {
		c.Resync = d
	}
	sv := r.Supervisor
	switch sv.Type {
	case "", "rules":
		c.Supervisor = Supervisor{Type: "rules"}
	case "llm":
		c.Supervisor = Supervisor{Type: "llm", Model: sv.Model, Effort: sv.Effort}
		switch sv.Effort {
		case "", "low", "medium", "high", "xhigh", "max":
		default:
			return Config{}, fmt.Errorf("config: supervisor.effort %q is not low|medium|high|xhigh|max", sv.Effort)
		}
		if c.Supervisor.Timeout, err = dur(sv.Timeout, "supervisor.timeout"); err != nil {
			return Config{}, err
		}
	default:
		return Config{}, fmt.Errorf("config: supervisor.type must be rules or llm, not %q", sv.Type)
	}
	return c, nil
}

// FullRef turns "main" into "refs/heads/main".
func FullRef(s string) string {
	if strings.HasPrefix(s, "refs/") {
		return s
	}
	return "refs/heads/" + s
}
