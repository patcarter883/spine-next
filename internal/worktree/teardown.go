package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/patcarter883/spine-next/internal/storage"
	"github.com/patcarter883/spine-next/third_party/gascity/pidutil"
	gcwt "github.com/patcarter883/spine-next/third_party/gascity/worktree"
)

// Teardown removes a lease's worktree and branch and keeps its snapshot refs.
// It fails closed, in the order of Gas City's worktree.Cleanup:
//   - a recorded process of the lease is still alive → ErrLeaseHeld
//   - provenance attempt id does not match the lease → ErrStaleAttempt
//
// Uncommitted work, and commits reachable only from the lease branch, are
// first captured in a snapshot, so removal never orphans anything; only then
// is the worktree cleaned and removed with a non-forced `git worktree remove`.
// Idempotent; safe to call again after a crash (the lease is Draining).
func (m *Manager) Teardown(ctx context.Context, l *Lease) error {
	lk, err := m.lockPath(ctx, l.Path)
	if err != nil {
		return err
	}
	defer lk.release()
	return m.teardownLocked(ctx, l)
}

func (m *Manager) teardownLocked(ctx context.Context, l *Lease) error {
	if !m.isLeasePath(l.Path) {
		return ErrPrimaryTree
	}
	cur, err := m.cfg.Store.GetLease(ctx, l.TaskID, l.Attempt)
	if err != nil {
		return err
	}
	if cur.State == storage.LeaseReleased {
		return nil
	}
	if cur.State == storage.LeaseRequested {
		return m.discardRequested(ctx, cur)
	}
	if live, err := m.liveProcs(ctx, cur.TaskID, cur.Attempt); err != nil {
		return err
	} else if len(live) > 0 {
		return fmt.Errorf("%w: %d running", ErrLeaseHeld, len(live))
	}
	wasDraining := cur.State == storage.LeaseDraining
	_, statErr := os.Stat(cur.Path)
	if statErr == nil && !validCheckout(cur.Path) && !wasDraining {
		// Only a crash inside `git worktree remove` (after the pre-removal
		// snapshot) legitimately leaves a broken checkout. Anything else is
		// not ours to delete.
		return fmt.Errorf("%w: %s", ErrBrokenWorktree, cur.Path)
	}
	cur.State = storage.LeaseDraining
	if err := m.cfg.Store.PutLease(ctx, cur); err != nil {
		return err
	}

	registered, err := m.isRegistered(ctx, cur.Path)
	if err != nil {
		return err
	}
	if statErr == nil && registered && validCheckout(cur.Path) {
		if err := m.checkProvenance(ctx, cur); err != nil {
			return err
		}
		if err := m.captureBeforeRemove(ctx, &cur); err != nil {
			return err
		}
		if _, err := m.git(ctx, cur.Path, nil, "reset", "-q", "--hard"); err != nil {
			return err
		}
		if _, err := m.git(ctx, cur.Path, nil, "clean", "-ffdxq"); err != nil {
			return err
		}
		gitDir, err := m.git1(ctx, cur.Path, "rev-parse", "--absolute-git-dir")
		if err != nil {
			return err
		}
		removeQuiet(filepath.Join(gitDir, snapIndexName))
		if err := m.withRefs(ctx, func() error {
			_, err := m.git(ctx, m.primary, nil, "worktree", "remove", cur.Path)
			return err
		}); err != nil {
			return err
		}
	} else if statErr == nil {
		// Unregistered or broken directory from a half-finished remove of a
		// Draining lease. Its pre-removal snapshot already exists.
		if err := os.RemoveAll(cur.Path); err != nil {
			return err
		}
	}
	if err := m.withRefs(ctx, func() error { return m.purgeRegistration(cur.Path) }); err != nil {
		return err
	}

	if err := m.withRefs(ctx, func() error {
		if oid, ok, err := m.revParse(ctx, m.primary, "refs/heads/"+cur.Branch); err != nil {
			return err
		} else if ok {
			if err := m.requireReachableElsewhere(ctx, oid, cur.Branch); err != nil {
				return err
			}
			if _, err := m.git(ctx, m.primary, nil, "update-ref", "-d", "refs/heads/"+cur.Branch, oid); err != nil {
				return err
			}
		}
		_, err := m.git(ctx, m.primary, nil, "worktree", "prune")
		return err
	}); err != nil {
		return err
	}
	cur.State = storage.LeaseReleased
	if err := m.cfg.Store.PutLease(ctx, cur); err != nil {
		return err
	}
	*l = cur
	return nil
}

func (m *Manager) withRefs(ctx context.Context, fn func() error) error {
	rl, err := m.lockRefs(ctx)
	if err != nil {
		return err
	}
	defer rl.release()
	return fn()
}

func (m *Manager) checkProvenance(ctx context.Context, l Lease) error {
	if l.AttemptID == "" {
		return nil // crashed before the id was recorded; the lease row is the authority
	}
	gitDir, err := m.git1(ctx, l.Path, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return err
	}
	prov, err := gcwt.ReadProvenanceAt(gitDir)
	if err != nil {
		return fmt.Errorf("worktree: reading provenance of %s: %w", l.Path, err)
	}
	if prov.AttemptID != l.AttemptID {
		return fmt.Errorf("%w: lease %s, on disk %s", ErrStaleAttempt, l.AttemptID, prov.AttemptID)
	}
	return nil
}

// captureBeforeRemove snapshots the worktree if it has uncommitted changes or
// if HEAD would become unreachable once the lease branch is deleted.
func (m *Manager) captureBeforeRemove(ctx context.Context, l *Lease) error {
	st, err := m.git(ctx, l.Path, nil, "status", "--porcelain")
	if err != nil {
		return err
	}
	head, err := m.git1(ctx, l.Path, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	unreachable, err := m.unreachableWithout(ctx, head, l.Branch)
	if err != nil {
		return err
	}
	if strings.TrimSpace(st) == "" && !unreachable {
		return nil
	}
	_, err = m.snapshotLocked(ctx, l, "teardown")
	return err
}

// unreachableWithout reports whether oid has commits reachable from no ref
// other than refs/heads/<branch>. Snapshot refs count as reachability.
func (m *Manager) unreachableWithout(ctx context.Context, oid, branch string) (bool, error) {
	out, err := m.git(ctx, m.primary, nil, "rev-list", "-n1", oid, "--not",
		"--exclude=refs/heads/"+branch, "--branches", "--tags", "--remotes", "--glob=refs/spine")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

func (m *Manager) requireReachableElsewhere(ctx context.Context, oid, branch string) error {
	un, err := m.unreachableWithout(ctx, oid, branch)
	if err != nil {
		return err
	}
	if un {
		return fmt.Errorf("%w: refs/heads/%s at %s", ErrUnreachable, branch, oid)
	}
	return nil
}

func (m *Manager) liveProcs(ctx context.Context, task string, attempt int) ([]storage.Proc, error) {
	procs, err := m.cfg.Store.ListProcs(ctx, storage.ProcRunning)
	if err != nil {
		return nil, err
	}
	var live []storage.Proc
	for _, p := range procs {
		if p.TaskID == task && p.Attempt == attempt && pidutil.AliveWithStartTime(p.PGID, p.StartTime) {
			live = append(live, p)
		}
	}
	return live, nil
}

func removeQuiet(path string) {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		_ = err
	}
}
