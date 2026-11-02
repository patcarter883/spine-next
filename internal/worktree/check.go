package worktree

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/patcarter883/spine-next/internal/storage"
	"github.com/patcarter883/spine-next/third_party/gascity/pidutil"
)

// Check verifies the repository and store invariants that must hold whenever
// no operation is in flight (after Recover, and between operations). It
// returns human-readable violations; an empty slice means healthy. The chaos
// harness (M0 exit criterion) and `spine doctor` both use it.
func (m *Manager) Check(ctx context.Context) ([]string, error) {
	var v []string
	add := func(f string, a ...any) { v = append(v, fmt.Sprintf(f, a...)) }

	// Object store integrity. Dangling objects (from interrupted commit-tree
	// or merge-tree) are expected and not reported.
	if out, code, stderr, err := m.gitRC(ctx, m.primary, nil, "fsck", "--no-progress", "--no-dangling", "--no-reflogs"); err != nil {
		return nil, err
	} else if code != 0 {
		add("git fsck failed (exit %d): %s %s", code, strings.TrimSpace(out), strings.TrimSpace(stderr))
	}

	locks, tmp := m.findLocks()
	for _, f := range locks {
		add("leftover lock file %s", f)
	}
	for _, f := range tmp {
		add("leftover temporary index %s", f)
	}

	st := m.cfg.Store
	procs, err := st.ListProcs(ctx, storage.ProcRunning)
	if err != nil {
		return nil, err
	}
	for _, p := range procs {
		if pidutil.AliveWithStartTime(p.PGID, p.StartTime) {
			add("process group %d (task %s) still running", p.PGID, p.TaskID)
		} else {
			add("process row %d (task %s) not closed", p.ID, p.TaskID)
		}
	}
	for _, pid := range procsUnder(m.cfg.Root) {
		add("process %d running inside %s", pid, m.cfg.Root)
	}

	ops, err := st.PendingOps(ctx)
	if err != nil {
		return nil, err
	}
	for _, o := range ops {
		add("pending %s op %d (task %s)", o.Kind, o.ID, o.TaskID)
	}

	registered, err := m.registered(ctx)
	if err != nil {
		return nil, err
	}
	leases, err := st.ListLeases(ctx)
	if err != nil {
		return nil, err
	}
	ready := map[string]bool{}
	for _, l := range leases {
		switch l.State {
		case storage.LeaseRequested, storage.LeaseDraining:
			add("lease %s/a%d left %s", l.TaskID, l.Attempt, l.State)
		case storage.LeaseReady:
			ready[l.Path] = true
			if _, err := os.Stat(l.Path); err != nil {
				add("ready lease %s/a%d: path missing", l.TaskID, l.Attempt)
				continue
			}
			if _, ok := registered[l.Path]; !ok {
				add("ready lease %s/a%d: worktree not registered", l.TaskID, l.Attempt)
				continue
			}
			if err := m.checkProvenance(ctx, l); err != nil {
				add("ready lease %s/a%d: %v", l.TaskID, l.Attempt, err)
			}
		}
	}
	for p := range registered {
		if m.isLeasePath(p) && !ready[p] {
			add("registered worktree %s has no Ready lease", p)
		}
	}
	if entries, err := os.ReadDir(m.cfg.Root); err == nil {
		for _, e := range entries {
			if p := m.cfg.Root + string(os.PathSeparator) + e.Name(); !ready[p] {
				add("directory %s under root has no Ready lease", p)
			}
		}
	}

	snaps, err := st.ListSnapshots(ctx, "")
	if err != nil {
		return nil, err
	}
	for _, s := range snaps {
		oid, ok, err := m.revParse(ctx, m.primary, s.Ref)
		if err != nil {
			return nil, err
		}
		if !ok || oid != s.CommitOID {
			add("snapshot %s: ref is %q, recorded %s", s.Ref, oid, s.CommitOID)
			continue
		}
		if tree, err := m.git1(ctx, m.primary, "rev-parse", s.CommitOID+"^{tree}"); err != nil || tree != s.TreeOID {
			add("snapshot %s: tree mismatch", s.Ref)
		}
	}
	return v, nil
}
