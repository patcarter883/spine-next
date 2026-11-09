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
	"sort"
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
	Context      Context
}

// Context configures the repository context given to agents.
type Context struct {
	NotesTokens   int     // cap for .spine/context.md (default 1200)
	ModulesTokens int     // cap for the module map in planner prompts (default 1500)
	StaleAfter    int     // commits after which the files count as stale (default 200)
	Experiment    float64 // fraction of tasks run without context, for measurement (0..1)
}

// Supervisor selects how failed rounds are decided (RFC §3.5).
type Supervisor struct {
	Type string // "rules" (default) or "llm"
	// Provider names a provider in Pi's models.json to ask instead of the
	// Anthropic API (llm; requires Model).
	Provider string
	Model    string        // llm: default claude-opus-5-5
	Effort   string        // llm: low|medium|high|xhigh|max (default medium)
	Timeout  time.Duration // llm: per decision (default 2m)
}

type rawAdapter struct {
	Protocol     string            `yaml:"protocol"`
	Version      string            `yaml:"version"`
	Model        string            `yaml:"model"`
	Provider     string            `yaml:"provider"`
	Args         []string          `yaml:"args"`
	ToolTimeout  string            `yaml:"tool_timeout"`
	MaxTurns     int               `yaml:"max_turns"`
	Container    *agent.Container  `yaml:"container"`
	Argv         []string          `yaml:"argv"`
	Env          map[string]string `yaml:"env"`
	PassEnv      []string          `yaml:"pass_env"`
	Mode         *string           `yaml:"mode"`
	Permissions  string            `yaml:"permissions"`
	StallTimeout string            `yaml:"stall_timeout"`
	Context      *bool             `yaml:"context"`
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
	Context      struct {
		NotesTokens   int     `yaml:"notes_tokens"`
		ModulesTokens int     `yaml:"modules_tokens"`
		StaleAfter    int     `yaml:"stale_after"`
		Experiment    float64 `yaml:"experiment"`
	} `yaml:"context"`
	Supervisor struct {
		Type     string `yaml:"type"`
		Provider string `yaml:"provider"`
		Model    string `yaml:"model"`
		Effort   string `yaml:"effort"`
		Timeout  string `yaml:"timeout"`
	} `yaml:"supervisor"`
}

// Default returns the configuration used when no file exists.
func Default() Config {
	return Config{
		Agent:      "claude",
		Adapters:   map[string]agent.Adapter{"claude": agent.ClaudeAdapter(), "pi": agent.PiAdapter()},
		Parallel:   4,
		Base:       "refs/heads/main",
		Budget:     api.Budget{}.WithDefaults(),
		Resync:     15 * time.Second,
		Supervisor: Supervisor{Type: "rules"},
		Context:    Context{NotesTokens: 1200, ModulesTokens: 1500, StaleAfter: 200},
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
		a := agent.Adapter{Name: name, Argv: ra.Argv, Env: ra.Env, PassEnv: ra.PassEnv, Permissions: ra.Permissions,
			Protocol: ra.Protocol, Version: ra.Version, Model: ra.Model, Provider: ra.Provider, Args: ra.Args,
			MaxTurns: ra.MaxTurns, Container: ra.Container}
		if a.Protocol == "pi-rpc" && len(a.Argv) == 0 {
			a.Argv = []string{"pi"}
		}
		if base, ok := c.Adapters[name]; ok && (a.Protocol == "" || a.Protocol == base.Protocol) { // overlay on a built-in
			if a.Protocol == "" {
				a.Protocol = base.Protocol
			}
			if a.Version == "" {
				a.Version = base.Version
			}
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
		a.Context = a.Protocol == "pi-rpc"
		if ra.Context != nil {
			a.Context = *ra.Context
		}
		d, err := dur(ra.StallTimeout, "adapters."+name+".stall_timeout")
		if err != nil {
			return Config{}, err
		}
		if d > 0 {
			a.StallTimeout = d
		}
		if a.ToolTimeout, err = dur(ra.ToolTimeout, "adapters."+name+".tool_timeout"); err != nil {
			return Config{}, err
		}
		switch a.Protocol {
		case "", "acp":
		case "pi-rpc":
			if a.Version == "" {
				return Config{}, fmt.Errorf("config: adapter %q: protocol pi-rpc requires a pinned version (e.g. version: 1.5.0)", name)
			}
		default:
			return Config{}, fmt.Errorf("config: adapter %q: protocol must be acp or pi-rpc", name)
		}
		if a.Container != nil && a.Container.Image == "" {
			return Config{}, fmt.Errorf("config: adapter %q: container.image is required", name)
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
	if r.Context.NotesTokens > 0 {
		c.Context.NotesTokens = r.Context.NotesTokens
	}
	if r.Context.ModulesTokens > 0 {
		c.Context.ModulesTokens = r.Context.ModulesTokens
	}
	if r.Context.StaleAfter > 0 {
		c.Context.StaleAfter = r.Context.StaleAfter
	}
	if r.Context.Experiment < 0 || r.Context.Experiment > 1 {
		return Config{}, fmt.Errorf("config: context.experiment must be between 0 and 1")
	}
	c.Context.Experiment = r.Context.Experiment
	sv := r.Supervisor
	switch sv.Type {
	case "", "rules":
		c.Supervisor = Supervisor{Type: "rules"}
	case "llm":
		c.Supervisor = Supervisor{Type: "llm", Provider: sv.Provider, Model: sv.Model, Effort: sv.Effort}
		if sv.Provider != "" && sv.Model == "" {
			return Config{}, fmt.Errorf("config: supervisor.provider %q needs supervisor.model", sv.Provider)
		}
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

// TaskInput is what a person asks for when creating a task (CLI flags or
// the dashboard form). Zero fields take the repository's defaults.
type TaskInput struct {
	Title           string        `json:"title"`
	Intent          string        `json:"intent"`
	Base            string        `json:"base"`  // branch or full ref
	Scope           []string      `json:"scope"` // path globs; empty = everything
	After           []string      `json:"after"` // "task" or "repo:task"
	Agent           string        `json:"agent"` // adapter name
	RequireApproval bool          `json:"require_approval"`
	TestsOnly       bool          `json:"tests_only"`
	MaxAttempts     int           `json:"max_attempts"`
	MaxRounds       int           `json:"max_rounds"`
	MaxTokens       int64         `json:"max_tokens"`
	Gates           []string      `json:"gates"`    // subset of the base's gates; empty = all
	Template        string        `json:"template"` // preset the input came from (recorded on the spec)
	RunTimeout      time.Duration `json:"-"`
	// Acceptance is Gates resolved against the base's gates file by the
	// caller (NewSpec has no repository access).
	Acceptance []api.GateRef `json:"-"`
	CreatedBy  string        `json:"-"`
}

// NewSpec builds and validates a task spec from input and this
// repository's defaults. The CLI and the dashboard share it.
func (c Config) NewSpec(in TaskInput) (api.Spec, error) {
	in.Intent = strings.TrimSpace(in.Intent)
	if in.Intent == "" {
		return api.Spec{}, fmt.Errorf("the task needs an intent")
	}
	spec := api.Spec{Title: strings.TrimSpace(in.Title), Intent: in.Intent, BaseRef: c.Base, Budget: c.Budget}
	if b := strings.TrimSpace(in.Base); b != "" {
		spec.BaseRef = FullRef(b)
	}
	for _, g := range in.Scope {
		if g = strings.TrimSpace(g); g != "" {
			spec.Scope = append(spec.Scope, g)
		}
	}
	for _, d := range in.After {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		if !api.ValidDependency(api.TaskID(d)) {
			return api.Spec{}, fmt.Errorf("dependency %q is not a task id or repo:task", d)
		}
		spec.DependsOn = append(spec.DependsOn, api.TaskID(d))
	}
	if a := strings.TrimSpace(in.Agent); a != "" {
		if _, ok := c.Adapters[a]; !ok {
			return api.Spec{}, fmt.Errorf("unknown agent %q (configured: %s)", a, strings.Join(c.AdapterNames(), ", "))
		}
		spec.Adapter = a
	}
	if in.MaxAttempts < 0 || in.MaxRounds < 0 {
		return api.Spec{}, fmt.Errorf("attempts and rounds must not be negative")
	}
	if in.MaxAttempts > 0 {
		spec.Budget.MaxAttempts = in.MaxAttempts
	}
	if in.MaxRounds > 0 {
		spec.Budget.MaxPatchRounds = in.MaxRounds
	}
	if in.RunTimeout > 0 {
		spec.Budget.PerRunTimeout = in.RunTimeout
	}
	if in.MaxTokens < 0 {
		return api.Spec{}, fmt.Errorf("max tokens must not be negative")
	}
	if in.MaxTokens > 0 {
		spec.Budget.MaxTokens = in.MaxTokens
	}
	spec.Acceptance = in.Acceptance
	spec.Template = strings.TrimSpace(in.Template)
	spec.CreatedBy = in.CreatedBy
	spec.Escalation.RequireApprovalBeforeMerge = in.RequireApproval
	spec.TestsOnly = in.TestsOnly
	return spec, api.ValidateSpec(spec)
}

// PiAdapter is the adapter Pi settings apply to: the default agent if it is
// a Pi one, else the one named pi.
func (c Config) PiAdapter() agent.Adapter {
	if a := c.Adapters[c.Agent]; a.Protocol == "pi-rpc" {
		return a
	}
	if a, ok := c.Adapters["pi"]; ok && a.Protocol == "pi-rpc" {
		return a
	}
	return agent.PiAdapter()
}

// AdapterNames lists configured agent adapters, sorted.
func (c Config) AdapterNames() []string {
	var out []string
	for n := range c.Adapters {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// SurveySpec is a survey task: refresh .spine/modules.yaml (labels,
// caveats) and .spine/context.md, checked by the scope, nonempty and
// spine.context gates and held for approval.
func (c Config) SurveySpec(focus, agentName, who string) (api.Spec, error) {
	spec, err := c.NewSpec(TaskInput{Title: "Survey: refresh repository context", Intent: strings.TrimSpace(focus + "\n"),
		Agent: agentName, MaxAttempts: 1, MaxRounds: 2, CreatedBy: who, RequireApproval: true,
		Scope: []string{".spine/modules.yaml", ".spine/context.md"}})
	if err != nil && strings.Contains(err.Error(), "intent") {
		spec, err = c.NewSpec(TaskInput{Title: "Survey: refresh repository context", Intent: "Survey the repository.",
			Agent: agentName, MaxAttempts: 1, MaxRounds: 2, CreatedBy: who, RequireApproval: true,
			Scope: []string{".spine/modules.yaml", ".spine/context.md"}})
	}
	if err != nil {
		return spec, err
	}
	spec.Kind = api.KindSurvey
	spec.Acceptance = []api.GateRef{
		{Name: "scope", Tool: "diff.scope", Policy: api.Absolute},
		{Name: "nonempty", Tool: "diff.nonempty", Policy: api.Absolute},
		{Name: "context", Tool: "spine.context", Policy: api.Absolute, Args: map[string]any{"notes_tokens": c.Context.NotesTokens}},
	}
	return spec, nil
}
