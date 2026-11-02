package worktree

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type snapshotIntent struct {
	Seq  int    `json:"seq"`
	Ref  string `json:"ref"`
	Path string `json:"path"`
}

// Snapshot records the worktree's full state (tracked and untracked files,
// minus ignored ones) as a commit on refs/spine/snap/<task>/<seq>. It builds
// the tree in a temporary index file inside the worktree's private git dir,
// so the worktree's own index and files are never touched (RFC §6.2).
func (m *Manager) Snapshot(ctx context.Context, l *Lease, label string) (*Snapshot, error) {
	lk, err := m.lockPath(ctx, l.Path)
	if err != nil {
		return nil, err
	}
	defer lk.release()
	return m.snapshotLocked(ctx, l, label)
}

func (m *Manager) snapshotLocked(ctx context.Context, l *Lease, label string) (*Snapshot, error) {
	if !m.isLeasePath(l.Path) {
		return nil, ErrPrimaryTree
	}
	gitDir, err := m.git1(ctx, l.Path, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return nil, err
	}
	head, err := m.git1(ctx, l.Path, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return nil, err
	}
	seq, err := m.cfg.Store.NextSnapshotSeq(ctx, l.TaskID)
	if err != nil {
		return nil, err
	}
	ref := fmt.Sprintf("%s%s/%d", SnapshotRefPrefix, l.TaskID, seq)
	opID, err := m.cfg.Store.BeginOp(ctx, l.TaskID, "snapshot", snapshotIntent{Seq: seq, Ref: ref, Path: l.Path})
	if err != nil {
		return nil, err
	}

	idx := filepath.Join(gitDir, snapIndexName)
	_ = os.Remove(idx)
	defer os.Remove(idx)
	env := []string{"GIT_INDEX_FILE=" + idx}
	if _, err := m.git(ctx, l.Path, env, "read-tree", head); err != nil {
		return nil, err
	}
	if _, err := m.git(ctx, l.Path, env, "add", "-A", "--", "."); err != nil {
		return nil, err
	}
	treeOut, err := m.git(ctx, l.Path, env, "write-tree")
	if err != nil {
		return nil, err
	}
	tree := strings.TrimSpace(treeOut)
	msg := fmt.Sprintf("spine snapshot %s #%d (%s)", l.TaskID, seq, label)
	// Hidden snapshot commits are never signed: signing is the operator's
	// policy for commits that land, and a signing service outage must not
	// stop snapshots (seen in practice: commit.gpgsign with a remote signer).
	commit, err := m.git1(ctx, l.Path, "commit-tree", "--no-gpg-sign", tree, "-p", head, "-m", msg)
	if err != nil {
		return nil, err
	}
	if err := m.createRef(ctx, ref, commit); err != nil {
		return nil, err
	}
	snap := Snapshot{TaskID: l.TaskID, Seq: seq, Ref: ref, CommitOID: commit, TreeOID: tree, Label: label}
	if err := m.cfg.Store.CompleteSnapshot(ctx, opID, snap); err != nil {
		return nil, err
	}
	return &snap, nil
}

// snapIndexName is the temporary index used by Snapshot. Recover deletes a
// leftover one (and its .lock) after a crash.
const snapIndexName = "spine-snap.idx"

// createRef creates ref at oid, failing if ref already exists.
func (m *Manager) createRef(ctx context.Context, ref, oid string) error {
	rl, err := m.lockRefs(ctx)
	if err != nil {
		return err
	}
	defer rl.release()
	_, err = m.git(ctx, m.primary, nil, "update-ref", ref, oid, "")
	return err
}

// Rollback restores the worktree's files to a snapshot of the same task. HEAD
// and the worktree's index are left as they were (index = HEAD), so files
// that were untracked when the snapshot was taken are untracked again.
func (m *Manager) Rollback(ctx context.Context, l *Lease, to *Snapshot) error {
	if to.TaskID != l.TaskID {
		return ErrForeignSnapshot
	}
	if !m.isLeasePath(l.Path) {
		return ErrPrimaryTree
	}
	lk, err := m.lockPath(ctx, l.Path)
	if err != nil {
		return err
	}
	defer lk.release()
	if _, err := m.git(ctx, l.Path, nil, "read-tree", "--reset", "-u", to.TreeOID); err != nil {
		return err
	}
	if _, err := m.git(ctx, l.Path, nil, "clean", "-fdq"); err != nil {
		return err
	}
	_, err = m.git(ctx, l.Path, nil, "reset", "-q")
	return err
}

func splitLines(s string) []string {
	return strings.Split(strings.TrimRight(s, "\n"), "\n")
}
