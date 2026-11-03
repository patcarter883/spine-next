package supervisor

import (
	"context"
	"sync"

	"github.com/patcarter883/spine-next/internal/app"
	"github.com/patcarter883/spine-next/internal/inbox"
	"github.com/patcarter883/spine-next/internal/storage"
	"github.com/patcarter883/spine-next/internal/ui"
)

// UISource serves the dashboard from every supervised repository. It opens
// each database read-only and independently of the controller, so the
// dashboard keeps working (from durable state) while a repository's
// controller restarts or is broken.
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
		r := ui.Repo{Name: s.Name, Path: s.Path, State: string(s.State), Error: s.Error, Restarts: s.Restarts}
		db, cd, err := app.DBPath(s.Path)
		if err != nil {
			if r.Error == "" {
				r.Error = err.Error()
			}
			out = append(out, r)
			continue
		}
		r.InboxDir = inbox.Dir(cd)
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
