// Package supervisor runs Spine for several repositories from one process
// (`spine supervisor`). Storage stays per repository: each repository keeps
// its own database, writer lock, inbox, worktrees and recovery in its own
// git common dir, exactly as with a standalone `spine controller`. The
// supervisor adds a registry of repositories, a controller per repository
// with panic isolation and restart backoff, supervisor-wide limits on agent
// and gate runs, cross-repository dependencies, and one dashboard.
package supervisor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/patcarter883/spine-next/internal/worktree"
)

// Home returns the per-user Spine directory: $SPINE_HOME, else
// <user config dir>/spine.
func Home() string {
	if h := os.Getenv("SPINE_HOME"); h != "" {
		return h
	}
	d, err := os.UserConfigDir()
	if err != nil {
		d = filepath.Join(os.TempDir(), "spine-config")
	}
	return filepath.Join(d, "spine")
}

// Repo is a registered repository.
type Repo struct {
	Name string `yaml:"name" json:"name"`
	Path string `yaml:"path" json:"path"` // primary checkout
}

// Registry is the repos.yaml file.
type Registry struct{ Path string }

// DefaultRegistry is Home()/repos.yaml.
func DefaultRegistry() Registry { return Registry{Path: filepath.Join(Home(), "repos.yaml")} }

type registryFile struct {
	Repos []Repo `yaml:"repos"`
}

var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ErrUnknownRepo is returned for names not in the registry.
var ErrUnknownRepo = errors.New("supervisor: unknown repository")

// Load reads the registry (absent = empty), sorted by name.
func (r Registry) Load() ([]Repo, error) {
	b, err := os.ReadFile(r.Path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f registryFile
	if err := yaml.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("supervisor: %s: %w", r.Path, err)
	}
	sort.Slice(f.Repos, func(i, j int) bool { return f.Repos[i].Name < f.Repos[j].Name })
	return f.Repos, nil
}

func (r Registry) save(repos []Repo) error {
	if err := os.MkdirAll(filepath.Dir(r.Path), 0o755); err != nil {
		return err
	}
	b, err := yaml.Marshal(registryFile{Repos: repos})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(r.Path), ".repos-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()
	return os.Rename(tmp.Name(), r.Path)
}

// Add registers the repository containing dir under name (default: the
// primary checkout's directory name). The primary checkout is stored, so a
// path inside a linked worktree registers the repository, not the worktree.
func (r Registry) Add(ctx context.Context, dir, name string) (Repo, error) {
	primary, err := worktree.PrimaryOf(ctx, dir)
	if err != nil {
		return Repo{}, fmt.Errorf("supervisor: %s is not a git repository: %w", dir, err)
	}
	if name == "" {
		name = filepath.Base(primary)
	}
	if !nameRe.MatchString(name) {
		return Repo{}, fmt.Errorf("supervisor: repository name %q must match %s", name, nameRe)
	}
	repos, err := r.Load()
	if err != nil {
		return Repo{}, err
	}
	for _, x := range repos {
		if x.Name == name {
			if x.Path == primary {
				return x, nil // already registered
			}
			return Repo{}, fmt.Errorf("supervisor: name %q is already used by %s (pick another with --name)", name, x.Path)
		}
		if x.Path == primary {
			return Repo{}, fmt.Errorf("supervisor: %s is already registered as %q", primary, x.Name)
		}
	}
	repo := Repo{Name: name, Path: primary}
	return repo, r.save(append(repos, repo))
}

// Remove unregisters a repository by name. Its database and worktrees are
// untouched; a later `spine controller` or re-registration resumes them.
func (r Registry) Remove(name string) error {
	repos, err := r.Load()
	if err != nil {
		return err
	}
	out := repos[:0]
	found := false
	for _, x := range repos {
		if x.Name == name {
			found = true
			continue
		}
		out = append(out, x)
	}
	if !found {
		return fmt.Errorf("%w: %s", ErrUnknownRepo, name)
	}
	return r.save(out)
}

// Config is Home()/supervisor.yaml.
type Config struct {
	Addr      string        // dashboard listen address
	MaxAgents int           // concurrent agent runs across all repositories
	MaxGates  int           // concurrent gate evaluations across all repositories
	Reload    time.Duration // registry poll interval
}

// LoadConfig reads Home()/supervisor.yaml with defaults.
func LoadConfig() (Config, error) {
	c := Config{Addr: "127.0.0.1:8765", MaxAgents: 4, MaxGates: 2, Reload: 2 * time.Second}
	b, err := os.ReadFile(filepath.Join(Home(), "supervisor.yaml"))
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	var raw struct {
		Addr      string `yaml:"addr"`
		MaxAgents int    `yaml:"max_agents"`
		MaxGates  int    `yaml:"max_gates"`
	}
	if err := yaml.Unmarshal(b, &raw); err != nil {
		return c, fmt.Errorf("supervisor.yaml: %w", err)
	}
	if raw.Addr != "" {
		c.Addr = raw.Addr
	}
	if raw.MaxAgents > 0 {
		c.MaxAgents = raw.MaxAgents
	}
	if raw.MaxGates > 0 {
		c.MaxGates = raw.MaxGates
	}
	return c, nil
}
