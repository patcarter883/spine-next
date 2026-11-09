package supervisor

import (
	"context"
	"fmt"
	"sync"

	"github.com/patcarter883/spine-next/internal/api"
	"github.com/patcarter883/spine-next/internal/app"
	"github.com/patcarter883/spine-next/internal/inbox"
	"github.com/patcarter883/spine-next/internal/storage"
	"github.com/patcarter883/spine-next/internal/ui"
)

// UISource serves the dashboard from every supervised repository. It opens
// each database read-only and independently of the controller, so the
// dashboard keeps working (from durable state) while a repository's
// controller restarts or is broken. It also implements ui.Admin.
type UISource struct {
	Sup    *Supervisor
	mu     sync.Mutex
	stores map[string]*storage.Store // by path
}

// Repos implements ui.Source.
func (u *UISource) Repos(ctx context.Context) []ui.Repo {
	sts := u.Sup.Status()
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.stores == nil {
		u.stores = map[string]*storage.Store{}
	}
	out := make([]ui.Repo, 0, len(sts))
	for _, s := range sts {
		r := ui.Repo{Name: s.Name, Path: s.Path, State: string(s.State), Error: s.Error, Restarts: s.Restarts, Since: s.Since}
		db, cd, err := app.DBPath(s.Path)
		if err != nil {
			if r.Error == "" {
				r.Error = err.Error()
			}
			out = append(out, r)
			continue
		}
		r.InboxDir, r.CommonDir = inbox.Dir(cd), cd
		st := u.stores[s.Path]
		if st == nil {
			if st, err = storage.OpenReadOnly(ctx, db); err == nil {
				u.stores[s.Path] = st
			}
		}
		r.Store = st
		out = append(out, r)
	}
	return out
}

// Close releases the read-only stores.
func (u *UISource) Close() {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, st := range u.stores {
		st.Close()
	}
	u.stores = nil
}

// AddRepo implements ui.Admin.
func (u *UISource) AddRepo(ctx context.Context, path, name string) error {
	if path == "" {
		return fmt.Errorf("a path is required")
	}
	if _, err := u.Sup.Registry.Add(ctx, path, name); err != nil {
		return err
	}
	u.Sup.mu.Lock()
	sctx := u.Sup.ctx
	u.Sup.mu.Unlock()
	if sctx != nil {
		u.Sup.sync(sctx)
	}
	return nil
}

// RemoveRepo implements ui.Admin. The repository's data is kept.
func (u *UISource) RemoveRepo(name string) error {
	if err := u.Sup.Registry.Remove(name); err != nil {
		return err
	}
	u.Sup.mu.Lock()
	sctx := u.Sup.ctx
	u.Sup.mu.Unlock()
	if sctx != nil {
		u.Sup.sync(sctx)
	}
	return nil
}

// RestartRepo implements ui.Admin.
func (u *UISource) RestartRepo(name string) error { return u.Sup.Restart(name) }

// SetParallel implements ui.Admin.
func (u *UISource) SetParallel(name string, n int) error { return u.Sup.SetParallel(name, n) }

// RepoSlots implements ui.Admin.
func (u *UISource) RepoSlots(name string) ([]api.TaskID, int, bool) { return u.Sup.RepoSlots(name) }

// Slots implements ui.Admin.
func (u *UISource) Slots() ui.Slots {
	l := u.Sup.Limits()
	if l == nil {
		return ui.Slots{Max: map[string]int{}, InUse: map[string]int{}, Peak: map[string]int{}, Holders: []ui.SlotHolder{}, Waiting: []ui.SlotHolder{}}
	}
	st := l.State()
	conv := func(ws []reconcilerWaiter) []ui.SlotHolder {
		out := make([]ui.SlotHolder, 0, len(ws))
		for _, w := range ws {
			out = append(out, ui.SlotHolder{Repo: w.Repo, Task: w.Task, Kind: w.Kind, Since: w.Since})
		}
		return out
	}
	return ui.Slots{Max: st.Max, InUse: st.InUse, Peak: st.Peak, Holders: conv(st.Holders), Waiting: conv(st.Waiting)}
}

// SetLimits implements ui.Admin.
func (u *UISource) SetLimits(maxAgents, maxGates int) error {
	l := u.Sup.Limits()
	if l == nil {
		return fmt.Errorf("the supervisor is not running")
	}
	l.Resize("agent", maxAgents)
	l.Resize("gate", maxGates)
	u.Sup.Config.MaxAgents, u.Sup.Config.MaxGates = maxAgents, maxGates
	return nil
}
