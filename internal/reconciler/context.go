package reconciler

import (
	"context"
	"os"
	"path/filepath"

	"github.com/patcarter883/spine-next/internal/agent"
	"github.com/patcarter883/spine-next/internal/api"
	"github.com/patcarter883/spine-next/internal/modmap"
	"github.com/patcarter883/spine-next/internal/repoctx"
)

// ContextInfo records what repository context an agent run received (on
// its agent_start event), so runs with and without it can be compared.
type ContextInfo struct {
	Notes       bool `json:"notes"`             // .spine/context.md included
	NotesTokens int  `json:"notes_tokens"`      // its size
	Modules     bool `json:"modules"`           // module map included (planners)
	Control     bool `json:"control,omitempty"` // experiment control arm: withheld on purpose
	Available   bool `json:"available"`         // the base has either file
}

// addContext puts the base's repository context into a prompt: the summary
// for adapters with context on, the module map for planners. Tasks in the
// experiment's control arm get neither.
func (c *Controller) addContext(ctx context.Context, t *api.Task, a agent.Adapter, base string, p *agent.Prompt) ContextInfo {
	var info ContextInfo
	rc, err := repoctx.Load(ctx, c.WT.Primary(), base, c.Config.Context.NotesTokens)
	if err != nil {
		c.Logf("repository context for %s: %v", t.ID, err)
		return info
	}
	info.Available = rc.Notes != "" || rc.Modules != nil
	if t.Spec.Kind == api.KindSurvey {
		// The survey rewrites the summary: it always sees the current one.
		p.Survey, p.NotesTokens, p.Notes = true, c.Config.Context.NotesTokens, rc.Notes
		return info
	}
	if info.Available && repoctx.Control(string(t.ID), c.Config.Context.Experiment) {
		info.Control = true
		return info
	}
	if a.Context && rc.Notes != "" {
		p.Notes, info.Notes, info.NotesTokens = rc.Notes, true, repoctx.Tokens(rc.Notes)
	}
	if p.Plan && rc.Modules != nil {
		p.Modules, info.Modules = rc.Modules.Render(c.Config.Context.ModulesTokens), true
	}
	return info
}

// prepareSurvey writes the freshly derived module map into a survey task's
// worktree (keeping the base's labels and caveats), so the agent only adds
// labels, caveats and the summary.
func (c *Controller) prepareSurvey(ctx context.Context, t *api.Task, worktree, base string) error {
	prev, _, _ := modmap.Load(ctx, c.WT.Primary(), base)
	m, err := modmap.Derive(ctx, c.WT.Primary(), base, prev, modmap.Options{})
	if err != nil {
		return err
	}
	b, err := m.Marshal()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(worktree, ".spine"), 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(worktree, modmap.Path), b, 0o644)
}
