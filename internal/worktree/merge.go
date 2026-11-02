package worktree

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// MergeResult describes a pre-flight or an integration.
type MergeResult struct {
	Clean      bool
	BaseTip    string   // base commit the merge was computed against
	ResultTree string   // merge-tree --write-tree output
	Conflicts  []string // conflicted paths when !Clean
	NewHead    string   // commit written to the base ref (Merge only)
	Rescued    string   // rescue ref, if an interrupted checkout sync saved operator state
}

// Preflight computes merging snap onto the current tip of baseRef with
// merge-tree. It has no side effects on any worktree, index or ref (it may
// write unreferenced objects, which fsck reports only as dangling).
func (m *Manager) Preflight(ctx context.Context, snap *Snapshot, baseRef string) (*MergeResult, error) {
	tip, ok, err := m.revParse(ctx, m.primary, baseRef+"^{commit}")
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("worktree: base %q does not resolve", baseRef)
	}
	return m.preflightAt(ctx, snap.CommitOID, tip)
}

func (m *Manager) preflightAt(ctx context.Context, commit, tip string) (*MergeResult, error) {
	out, code, stderr, err := m.gitRC(ctx, m.primary, nil, "merge-tree", "--write-tree", "--name-only", "--no-messages", tip, commit)
	if err != nil {
		return nil, err
	}
	lines := splitLines(out)
	res := &MergeResult{BaseTip: tip}
	switch code {
	case 0:
		res.Clean, res.ResultTree = true, strings.TrimSpace(lines[0])
	case 1:
		res.ResultTree = strings.TrimSpace(lines[0])
		for _, l := range lines[1:] {
			if l == "" {
				break
			}
			res.Conflicts = append(res.Conflicts, l)
		}
	default:
		return nil, &GitError{Args: []string{"merge-tree"}, ExitCode: code, Stderr: stderr}
	}
	return res, nil
}

type mergeIntent struct {
	Ref      string `json:"ref"`
	Old      string `json:"old"`
	New      string `json:"new"`
	Checkout string `json:"checkout,omitempty"` // non-lease worktree with Ref checked out
}

// Merge integrates snap into baseRef (a full ref name, e.g. refs/heads/main)
// as a single squash commit, using compare-and-swap on the ref. It returns
// ErrBaseMoved if another integration won the race (the caller re-runs
// Preflight and the gate against the new tip), and ErrConflict if the merge
// is not clean.
//
// If baseRef is checked out in a non-Spine worktree (typically the operator's
// primary checkout), that checkout must have no uncommitted changes to
// tracked files; after the ref moves, its index and files are moved forward
// with a two-tree read-tree, exactly as a fast-forward checkout would.
func (m *Manager) Merge(ctx context.Context, snap *Snapshot, baseRef, msg string) (*MergeResult, error) {
	return m.MergeAt(ctx, snap, baseRef, "", msg)
}

// MergeAt is Merge, but it fails with ErrBaseMoved unless baseRef is at
// expectTip when the merge is computed (empty = any tip). The reconciler
// passes the tip its verdict was computed against, so the tree that lands is
// exactly the tree that was gated.
func (m *Manager) MergeAt(ctx context.Context, snap *Snapshot, baseRef, expectTip, msg string) (*MergeResult, error) {
	if !strings.HasPrefix(baseRef, "refs/") {
		return nil, fmt.Errorf("worktree: base ref %q must be a full ref name", baseRef)
	}
	rl, err := m.lockRefs(ctx)
	if err != nil {
		return nil, err
	}
	defer rl.release()

	old, ok, err := m.revParse(ctx, m.primary, baseRef)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("worktree: base ref %q does not exist", baseRef)
	}
	if expectTip != "" && old != expectTip {
		return nil, ErrBaseMoved
	}
	res, err := m.preflightAt(ctx, snap.CommitOID, old)
	if err != nil {
		return nil, err
	}
	if !res.Clean {
		return res, ErrConflict
	}
	checkout, err := m.checkoutOf(ctx, baseRef)
	if err != nil {
		return nil, err
	}
	if checkout != "" {
		if m.isLeasePath(checkout) {
			return nil, fmt.Errorf("worktree: %s is checked out in a Spine lease (%s)", baseRef, checkout)
		}
		st, err := m.git(ctx, checkout, nil, "status", "--porcelain", "--untracked-files=no")
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(st) != "" {
			return res, fmt.Errorf("%w: %s at %s", ErrCheckoutDirty, baseRef, checkout)
		}
	}
	newHead, err := m.git1(ctx, m.primary, "commit-tree", res.ResultTree, "-p", old, "-m", msg)
	if err != nil {
		return nil, err
	}
	opID, err := m.cfg.Store.BeginOp(ctx, snap.TaskID, "merge", mergeIntent{Ref: baseRef, Old: old, New: newHead, Checkout: checkout})
	if err != nil {
		return nil, err
	}
	if _, err := m.git(ctx, m.primary, nil, "update-ref", "-m", "spine: "+msg, baseRef, newHead, old); err != nil {
		var ge *GitError
		if errors.As(err, &ge) {
			if cur, _, _ := m.revParse(ctx, m.primary, baseRef); cur != old {
				return nil, errors.Join(ErrBaseMoved, m.finish(ctx, opID, false))
			}
		}
		return nil, err // op stays pending; Recover resolves it from the ref
	}
	if checkout != "" {
		rescued, err := m.syncCheckout(ctx, checkout, old, newHead)
		if err != nil {
			return nil, err // ref moved; op pending; Recover finishes the sync
		}
		res.Rescued = rescued
	}
	res.NewHead = newHead
	return res, m.finish(ctx, opID, true)
}

func (m *Manager) finish(ctx context.Context, opID int64, done bool) error {
	st := "aborted"
	if done {
		st = "done"
	}
	return m.cfg.Store.FinishOp(ctx, opID, opState(st))
}

// checkoutOf returns the worktree that has ref checked out, or "".
func (m *Manager) checkoutOf(ctx context.Context, ref string) (string, error) {
	wts, err := m.registered(ctx)
	if err != nil {
		return "", err
	}
	for path, branch := range wts {
		if branch == ref {
			return path, nil
		}
	}
	return "", nil
}

// syncCheckout moves a checkout whose branch was advanced from old to new.
// The fast path is a two-tree read-tree, which refuses to overwrite local
// changes. If that fails (the operator edited a file mid-merge, or a previous
// sync was interrupted) the checkout's tracked state is first rescued into
// refs/spine/rescue/<ns> and then reset to new, so nothing can be lost.
func (m *Manager) syncCheckout(ctx context.Context, checkout, old, newHead string) (string, error) {
	if _, err := m.git(ctx, checkout, nil, "read-tree", "-m", "-u", old, newHead); err == nil {
		return "", nil
	}
	rescued, err := m.rescueCheckout(ctx, checkout)
	if err != nil {
		return "", err
	}
	if _, err := m.git(ctx, checkout, nil, "read-tree", "--reset", "-u", newHead); err != nil {
		return rescued, err
	}
	return rescued, nil
}

// rescueCheckout records the checkout's tracked files (as on disk) as a
// commit on a rescue ref, using a temporary index. Untracked files are left
// alone on disk and are not touched by the reset that follows.
func (m *Manager) rescueCheckout(ctx context.Context, checkout string) (string, error) {
	gitDir, err := m.git1(ctx, checkout, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", err
	}
	idx := gitDir + "/spine-rescue.idx"
	env := []string{"GIT_INDEX_FILE=" + idx}
	defer removeQuiet(idx)
	head, err := m.git1(ctx, checkout, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	if _, err := m.git(ctx, checkout, env, "read-tree", head); err != nil {
		return "", err
	}
	if _, err := m.git(ctx, checkout, env, "add", "-u", "--", "."); err != nil {
		return "", err
	}
	tree, err := m.git(ctx, checkout, env, "write-tree")
	if err != nil {
		return "", err
	}
	commit, err := m.git1(ctx, checkout, "commit-tree", "--no-gpg-sign", strings.TrimSpace(tree), "-p", head, "-m", "spine rescue of "+checkout)
	if err != nil {
		return "", err
	}
	ref := fmt.Sprintf("%s%d", RescueRefPrefix, time.Now().UnixNano())
	_, err = m.git(ctx, m.primary, nil, "update-ref", ref, commit, "")
	return ref, err
}

// ProposedCommit writes (but does not reference) the commit Merge would
// create for snap on top of tip, so the gate engine can evaluate the merge
// result before integration (RFC §4.7). It fails with ErrConflict if the
// merge is not clean. The object is unreferenced until a Merge writes the
// same tree; gc may prune it later, which is harmless.
func (m *Manager) ProposedCommit(ctx context.Context, snap *Snapshot, tip, msg string) (string, *MergeResult, error) {
	res, err := m.preflightAt(ctx, snap.CommitOID, tip)
	if err != nil {
		return "", nil, err
	}
	if !res.Clean {
		return "", res, ErrConflict
	}
	c, err := m.git1(ctx, m.primary, "commit-tree", "--no-gpg-sign", res.ResultTree, "-p", tip, "-m", msg)
	return c, res, err
}

// ResolveCommit returns the commit rev points at in the primary repository.
func (m *Manager) ResolveCommit(ctx context.Context, rev string) (string, error) {
	oid, ok, err := m.revParse(ctx, m.primary, rev+"^{commit}")
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("worktree: %q does not resolve to a commit", rev)
	}
	return oid, nil
}
