package worktree

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/patcarter883/spine-next/internal/api"
	"github.com/patcarter883/spine-next/internal/gitro"
	"github.com/patcarter883/spine-next/internal/storage"
)

// GCReport lists what garbage collection would remove (or removed).
type GCReport struct {
	Days int       `json:"days"`
	At   time.Time `json:"at"`
	// SnapshotRefs of tasks that finished more than Days ago (or no longer
	// exist). Their commits stay reachable from integrated history when the
	// task landed.
	SnapshotRefs []string `json:"snapshot_refs"`
	// StrayWorktrees are directories under the worktree root that no live
	// lease owns.
	StrayWorktrees []string `json:"stray_worktrees"`
	ToolCacheRows  int64    `json:"tool_cache_rows"`
	ToolCacheBytes int64    `json:"tool_cache_bytes"`
	OldCacheRows   int64    `json:"old_cache_rows"`
	OldCacheBytes  int64    `json:"old_cache_bytes"`
	ArtifactBytes  int64    `json:"artifact_bytes"`
	// Errors are items that could not be removed (Apply only).
	Errors []string `json:"errors,omitempty"`
}

// PlanGC works out what GC would remove, reading the repository and a
// (possibly read-only) store. It changes nothing.
func PlanGC(ctx context.Context, primary, root string, st *storage.Store, days int) (GCReport, error) {
	if days <= 0 {
		days = 14
	}
	rep := GCReport{Days: days, At: time.Now().UTC(), SnapshotRefs: []string{}, StrayWorktrees: []string{}}
	cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	tasks, err := st.ListTasks(ctx)
	if err != nil {
		return rep, err
	}
	byID := map[string]api.Task{}
	for _, t := range tasks {
		byID[string(t.ID)] = t
	}
	out, err := (gitro.Repo{Dir: primary}).Run(ctx, "for-each-ref", "--format=%(refname)", SnapshotRefPrefix)
	if err != nil {
		return rep, err
	}
	for _, ref := range strings.Fields(string(out)) {
		rest := strings.TrimPrefix(ref, SnapshotRefPrefix)
		task, _, _ := strings.Cut(rest, "/")
		t, ok := byID[task]
		if !ok || (t.Status.Phase.Done() && t.UpdatedAt.Before(cutoff)) {
			rep.SnapshotRefs = append(rep.SnapshotRefs, ref)
		}
	}
	leases, err := st.ListLeases(ctx, storage.LeaseRequested, storage.LeaseReady, storage.LeaseDraining)
	if err != nil {
		return rep, err
	}
	live := map[string]bool{}
	for _, l := range leases {
		live[filepath.Clean(l.Path)] = true
	}
	if ents, err := os.ReadDir(root); err == nil {
		for _, e := range ents {
			p := filepath.Join(root, e.Name())
			if !e.IsDir() || live[p] || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			if fi, err := os.Stat(p); err == nil && time.Since(fi.ModTime()) < time.Hour {
				continue // may be a lease being created right now
			}
			rep.StrayWorktrees = append(rep.StrayWorktrees, p)
		}
	}
	sort.Strings(rep.StrayWorktrees)
	rep.ToolCacheRows, rep.ToolCacheBytes, rep.OldCacheRows, rep.OldCacheBytes, err = st.ToolCacheStats(ctx, cutoff)
	if err != nil {
		return rep, err
	}
	if cd, err := ResolveCommonDir(primary); err == nil {
		rep.ArtifactBytes = dirSize(filepath.Join(cd, "spine", "artifacts"))
	}
	return rep, nil
}

func dirSize(dir string) int64 {
	var n int64
	filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if fi, err := d.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}

// GC removes what PlanGC reports, re-checking each item under the locks the
// rest of the manager uses. The store must be the writer.
func (m *Manager) GC(ctx context.Context, days int) (GCReport, error) {
	rep, err := PlanGC(ctx, m.primary, m.cfg.Root, m.cfg.Store, days)
	if err != nil {
		return rep, err
	}
	if len(rep.SnapshotRefs) > 0 {
		rl, err := m.lockRefs(ctx)
		if err != nil {
			return rep, err
		}
		for _, ref := range rep.SnapshotRefs {
			if !strings.HasPrefix(ref, SnapshotRefPrefix) {
				continue
			}
			if _, err := m.git(ctx, m.primary, nil, "update-ref", "-d", ref); err != nil {
				rep.Errors = append(rep.Errors, ref+": "+err.Error())
			}
		}
		rl.release()
	}
	for _, p := range rep.StrayWorktrees {
		if !m.isLeasePath(p) {
			continue
		}
		lk, err := m.lockPath(ctx, p)
		if err != nil {
			rep.Errors = append(rep.Errors, p+": "+err.Error())
			continue
		}
		leases, err := m.cfg.Store.ListLeases(ctx, storage.LeaseRequested, storage.LeaseReady, storage.LeaseDraining)
		owned := err != nil
		for _, l := range leases {
			owned = owned || filepath.Clean(l.Path) == p
		}
		if !owned {
			if err := os.RemoveAll(p); err != nil {
				rep.Errors = append(rep.Errors, p+": "+err.Error())
			}
		}
		lk.release()
	}
	m.git(ctx, m.primary, nil, "worktree", "prune")
	cutoff := time.Now().Add(-time.Duration(rep.Days) * 24 * time.Hour)
	if _, err := m.cfg.Store.PruneToolCache(ctx, cutoff); err != nil {
		rep.Errors = append(rep.Errors, "tool cache: "+err.Error())
	}
	if err := m.cfg.Store.Checkpoint(ctx); err != nil && !errors.Is(err, context.Canceled) {
		rep.Errors = append(rep.Errors, "checkpoint: "+err.Error())
	}
	return rep, nil
}
