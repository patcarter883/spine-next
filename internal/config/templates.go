package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// TemplatesPath holds a repository's task presets, next to config.yaml.
const TemplatesPath = ".spine/templates.yaml"

// Template is a saved task shape ("bugfix in pkg X with tests"). Its fields
// pre-fill the new-task form; Intent may contain {{placeholders}} for the
// person to replace.
type Template struct {
	Name            string   `yaml:"name" json:"name"`
	Description     string   `yaml:"description,omitempty" json:"description,omitempty"`
	Title           string   `yaml:"title,omitempty" json:"title,omitempty"`
	Intent          string   `yaml:"intent,omitempty" json:"intent,omitempty"`
	Scope           []string `yaml:"scope,omitempty" json:"scope,omitempty"`
	Gates           []string `yaml:"gates,omitempty" json:"gates,omitempty"`
	Agent           string   `yaml:"agent,omitempty" json:"agent,omitempty"`
	RequireApproval bool     `yaml:"require_approval,omitempty" json:"require_approval,omitempty"`
	MaxAttempts     int      `yaml:"max_attempts,omitempty" json:"max_attempts,omitempty"`
	MaxRounds       int      `yaml:"max_rounds,omitempty" json:"max_rounds,omitempty"`
	MaxTokens       int64    `yaml:"max_tokens,omitempty" json:"max_tokens,omitempty"`
	// Source is "repo" (.spine/templates.yaml, shared through git) or
	// "user" (the dashboard's own file, editable from the dashboard).
	Source string `yaml:"-" json:"source"`
}

type templatesFile struct {
	Templates []Template `yaml:"templates"`
}

// ParseTemplates parses a templates file.
func ParseTemplates(b []byte) ([]Template, error) {
	var f templatesFile
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil && err.Error() != "EOF" {
		return nil, err
	}
	seen := map[string]bool{}
	for i, t := range f.Templates {
		t.Name = strings.TrimSpace(t.Name)
		if t.Name == "" {
			return nil, fmt.Errorf("templates[%d]: name is required", i)
		}
		if seen[t.Name] {
			return nil, fmt.Errorf("templates: duplicate name %q", t.Name)
		}
		seen[t.Name] = true
		if t.MaxAttempts < 0 || t.MaxRounds < 0 || t.MaxTokens < 0 {
			return nil, fmt.Errorf("templates: %q: budgets must not be negative", t.Name)
		}
		f.Templates[i] = t
	}
	return f.Templates, nil
}

// LoadTemplates reads a repository's templates (absent = none).
func LoadTemplates(repoRoot string) ([]Template, error) {
	b, err := os.ReadFile(filepath.Join(repoRoot, TemplatesPath))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	ts, err := ParseTemplates(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", TemplatesPath, err)
	}
	for i := range ts {
		ts[i].Source = "repo"
	}
	return ts, nil
}

// UserTemplates is a templates file owned by the dashboard.
type UserTemplates struct{ Path string }

// Load reads the file (absent = none).
func (u UserTemplates) Load() ([]Template, error) {
	b, err := os.ReadFile(u.Path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	ts, err := ParseTemplates(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", u.Path, err)
	}
	for i := range ts {
		ts[i].Source = "user"
	}
	return ts, nil
}

// Put adds or replaces a template by name.
func (u UserTemplates) Put(t Template) error {
	t.Name = strings.TrimSpace(t.Name)
	if t.Name == "" {
		return fmt.Errorf("a template needs a name")
	}
	ts, err := u.Load()
	if err != nil {
		return err
	}
	out := ts[:0]
	for _, x := range ts {
		if x.Name != t.Name {
			out = append(out, x)
		}
	}
	out = append(out, t)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return u.save(out)
}

// Delete removes a template; deleting a missing one is not an error.
func (u UserTemplates) Delete(name string) error {
	ts, err := u.Load()
	if err != nil {
		return err
	}
	out := ts[:0]
	for _, x := range ts {
		if x.Name != name {
			out = append(out, x)
		}
	}
	return u.save(out)
}

func (u UserTemplates) save(ts []Template) error {
	b, err := yaml.Marshal(templatesFile{Templates: ts})
	if err != nil {
		return err
	}
	return WriteFileAtomic(u.Path, b, 0o644)
}

// WriteFileAtomic writes via a temporary file and rename.
func WriteFileAtomic(path string, b []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	tmp.Close()
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}
