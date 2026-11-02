package gate

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/patcarter883/spine-next/internal/api"
	"github.com/patcarter883/spine-next/internal/gitro"
)

// ConfigPath is where projects declare gates. It is always read from the
// base commit, never from the snapshot: a worker cannot weaken the gates
// that judge its own change.
const ConfigPath = ".spine/gates.yaml"

// Config is a parsed gates file.
type Config struct {
	Gates    map[string]api.GateRef
	Order    []string
	Baseline Baseline
}

// Baseline configures how the base tree is prepared for baseline-relative
// gates.
type Baseline struct {
	// Link lists ignored dependency directories (node_modules, .venv,
	// vendor) symlinked from the task worktree into materialized trees, so
	// tools that need installed dependencies can run. They are assumed to be
	// a function of lockfiles in the tree and are not part of cache keys.
	Link []string `yaml:"link"`
}

type rawGate struct {
	Tool    string         `yaml:"tool"`
	Args    map[string]any `yaml:"args"`
	Policy  string         `yaml:"policy"`
	Timeout string         `yaml:"timeout"`
}

type rawConfig struct {
	Gates    map[string]rawGate `yaml:"gates"`
	Order    []string           `yaml:"order"`
	Baseline Baseline           `yaml:"baseline"`
}

// ParseConfig parses gates.yaml content.
func ParseConfig(b []byte) (Config, error) {
	var rc rawConfig
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true)
	if err := dec.Decode(&rc); err != nil {
		return Config{}, fmt.Errorf("gate: %s: %w", ConfigPath, err)
	}
	c := Config{Gates: map[string]api.GateRef{}, Order: rc.Order, Baseline: rc.Baseline}
	for name, g := range rc.Gates {
		ref := api.GateRef{Name: name, Tool: g.Tool, Args: normalizeYAML(g.Args)}
		switch strings.ToLower(g.Policy) {
		case "", "absolute":
			ref.Policy = api.Absolute
		case "baseline", "baseline_relative", "baseline-relative":
			ref.Policy = api.BaselineRelative
		default:
			return Config{}, fmt.Errorf("gate: %s: unknown policy %q", name, g.Policy)
		}
		if g.Timeout != "" {
			d, err := time.ParseDuration(g.Timeout)
			if err != nil || d <= 0 {
				return Config{}, fmt.Errorf("gate: %s: bad timeout %q", name, g.Timeout)
			}
			ref.Timeout = d
		}
		if ref.Tool == "" {
			return Config{}, fmt.Errorf("gate: %s: no tool", name)
		}
		c.Gates[name] = ref
	}
	if len(c.Order) == 0 { // deterministic default
		for n := range c.Gates {
			c.Order = append(c.Order, n)
		}
		sort.Strings(c.Order)
	}
	seen := map[string]bool{}
	for _, n := range c.Order {
		if _, ok := c.Gates[n]; !ok {
			return Config{}, fmt.Errorf("gate: order names unknown gate %q", n)
		}
		if seen[n] {
			return Config{}, fmt.Errorf("gate: order lists %q twice", n)
		}
		seen[n] = true
	}
	for n := range c.Gates {
		if !seen[n] {
			return Config{}, fmt.Errorf("gate: gate %q is not in order", n)
		}
	}
	return c, nil
}

// FromRefs builds a Config from a Spec's acceptance list, in order.
func FromRefs(refs []api.GateRef) (Config, error) {
	c := Config{Gates: map[string]api.GateRef{}}
	for i, r := range refs {
		if r.Name == "" {
			r.Name = fmt.Sprintf("%d-%s", i, r.Tool)
		}
		if r.Policy == "" {
			r.Policy = api.Absolute
		}
		if _, dup := c.Gates[r.Name]; dup {
			return Config{}, fmt.Errorf("gate: duplicate gate %q", r.Name)
		}
		c.Gates[r.Name] = r
		c.Order = append(c.Order, r.Name)
	}
	return c, nil
}

// LoadConfig reads the gates file at rev. ok is false if there is none.
func LoadConfig(ctx context.Context, repo, rev string) (Config, bool, error) {
	b, ok, err := gitro.Repo{Dir: repo}.Show(ctx, rev, ConfigPath)
	if err != nil || !ok {
		return Config{}, ok, err
	}
	c, err := ParseConfig(b)
	return c, true, err
}

// normalizeYAML converts YAML-decoded values to JSON-compatible ones.
func normalizeYAML(v map[string]any) map[string]any {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out map[string]any
	json.Unmarshal(b, &out)
	return out
}
