package worktree

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/patcarter883/spine-next/internal/storage"
	"github.com/patcarter883/spine-next/third_party/gascity/pidutil"
	"github.com/patcarter883/spine-next/third_party/gascity/processgroup"
)

// RecoveryReport says what Recover changed.
type RecoveryReport struct {
	ProcsReaped        int      // recorded groups still alive, killed
	ProcsExited        int      // recorded groups already gone, closed
	StrayProcsKilled   []int    // unrecorded processes running inside Root
	SnapshotsRecovered int      // intents whose ref landed: row written
	SnapshotsAborted   int      // intents whose ref never landed
	MergesCompleted    int      // ref had moved: op closed (checkout synced)
	MergesAborted      int      // ref had not moved: op aborted
	Rescued            []string // rescue refs written while syncing a checkout
	LeasesDiscarded    int      // Requested leases reversed
	LeasesTornDown     int      // Draining leases finished
	LocksRemoved       []string // stale git *.lock files removed
	LocksReported      []string // *.lock files left alone (not provably ours)
	OrphanDirs         []string // directories under Root with no live lease
}

// Recover reconciles durable intents against the repository after a crash
// (RFC §6.1). It must run before any new work is accepted, while this process
// holds the store's exclusive lock. Order matters: live processes are killed
// first (an orphan may still be writing), then stale locks are cleared, then
// operation intents and leases are resolved. Every step is idempotent; errors
// are collected and the remaining steps still run.
func (m *Manager) Recover(ctx context.Context) (RecoveryReport, error) {
	var rep RecoveryReport
	var errs []error
	st := m.cfg.Store

	pending, err := st.PendingOps(ctx)
	if err != nil {
		return rep, err
	}

	// 0. Orphans of the crashed supervisor (a git it ran, an agent) may still
	// be writing. Nothing below is safe until they are gone.
	rep.StrayProcsKilled = append(rep.StrayProcsKilled, m.killOrphans()...)

	// 1. Processes.
	running, err := st.ListProcs(ctx, storage.ProcRunning)
	errs = append(errs, err)
	for _, p := range running {
		if pidutil.AliveWithStartTime(p.PGID, p.StartTime) {
			_ = processgroup.Terminate(p.PGID, 2*time.Second, processgroup.Options{})
			_ = syscall.Kill(-p.PGID, syscall.SIGKILL)
			errs = append(errs, st.SetProcState(ctx, p.ID, storage.ProcReaped))
			rep.ProcsReaped++
		} else {
			errs = append(errs, st.SetProcState(ctx, p.ID, storage.ProcExited))
			rep.ProcsExited++
		}
	}
	// A process started but not yet recorded (crash inside the OnStart window)
	// is found by its cwd: anything running inside Root is Spine's.
	rep.StrayProcsKilled = append(rep.StrayProcsKilled, killProcsUnder(m.cfg.Root)...)
	waitGone(rep.StrayProcsKilled, 5*time.Second)
	// Only now is nothing of ours still running: any lock older than this
	// instant belongs to a dead process. (Taking it earlier let an orphaned
	// agent's git create a lock after the cut-off; found by the chaos harness.)
	boot := time.Now()

	// 2. Stale locks and temporary indexes.
	syncing := map[string]bool{} // git dirs of checkouts with an unfinished sync
	for _, op := range pending {
		if op.Kind != "merge" {
			continue
		}
		var in mergeIntent
		if json.Unmarshal(op.Intent, &in) == nil && in.Checkout != "" {
			if gd, err := m.git1(ctx, in.Checkout, "rev-parse", "--absolute-git-dir"); err == nil {
				syncing[filepath.Clean(gd)] = true
			}
		}
	}
	removed, reported, err := m.clearStaleLocks(ctx, boot, syncing)
	rep.LocksRemoved, rep.LocksReported = removed, reported
	errs = append(errs, err)

	// 3. Operation intents.
	for _, op := range pending {
		switch op.Kind {
		case "snapshot":
			ok, err := m.recoverSnapshot(ctx, op)
			errs = append(errs, err)
			if err == nil && ok {
				rep.SnapshotsRecovered++
			} else if err == nil {
				rep.SnapshotsAborted++
			}
		case "merge":
			done, rescued, err := m.recoverMerge(ctx, op)
			errs = append(errs, err)
			if rescued != "" {
				rep.Rescued = append(rep.Rescued, rescued)
			}
			if err == nil && done {
				rep.MergesCompleted++
			} else if err == nil {
				rep.MergesAborted++
			}
		default:
			errs = append(errs, st.FinishOp(ctx, op.ID, storage.OpAborted))
		}
	}

	// 4. Leases.
	leases, err := st.ListLeases(ctx)
	errs = append(errs, err)
	live := map[string]bool{}
	for _, l := range leases {
		switch l.State {
		case storage.LeaseRequested:
			if err := m.discardRequested(ctx, l); err != nil {
				errs = append(errs, err)
				live[l.Path] = true
			} else {
				rep.LeasesDiscarded++
			}
		case storage.LeaseDraining:
			if err := m.teardownLocked(ctx, &l); err != nil {
				errs = append(errs, err)
				live[l.Path] = true
			} else {
				rep.LeasesTornDown++
			}
		case storage.LeaseReady:
			live[l.Path] = true
		}
	}
	if _, err := m.git(ctx, m.primary, nil, "worktree", "prune"); err != nil {
		errs = append(errs, err)
	}

	// 5. Report directories under Root that no lease accounts for.
	if entries, err := os.ReadDir(m.cfg.Root); err == nil {
		for _, e := range entries {
			p := filepath.Join(m.cfg.Root, e.Name())
			if !live[p] {
				rep.OrphanDirs = append(rep.OrphanDirs, p)
			}
		}
	}
	return rep, errors.Join(errs...)
}

func (m *Manager) recoverSnapshot(ctx context.Context, op storage.Op) (bool, error) {
	var in snapshotIntent
	if err := json.Unmarshal(op.Intent, &in); err != nil {
		return false, m.cfg.Store.FinishOp(ctx, op.ID, storage.OpAborted)
	}
	commit, ok, err := m.revParse(ctx, m.primary, in.Ref)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, m.cfg.Store.FinishOp(ctx, op.ID, storage.OpAborted)
	}
	tree, err := m.git1(ctx, m.primary, "rev-parse", commit+"^{tree}")
	if err != nil {
		return false, err
	}
	return true, m.cfg.Store.CompleteSnapshot(ctx, op.ID, storage.Snapshot{
		TaskID: op.TaskID, Seq: in.Seq, Ref: in.Ref, CommitOID: commit, TreeOID: tree, Label: "recovered",
	})
}

func (m *Manager) recoverMerge(ctx context.Context, op storage.Op) (bool, string, error) {
	var in mergeIntent
	if err := json.Unmarshal(op.Intent, &in); err != nil {
		return false, "", m.cfg.Store.FinishOp(ctx, op.ID, storage.OpAborted)
	}
	cur, _, err := m.revParse(ctx, m.primary, in.Ref)
	if err != nil {
		return false, "", err
	}
	if cur != in.New {
		// The CAS never happened (or someone else moved the ref): our commit
		// is unreferenced and the caller retries from a fresh pre-flight.
		return false, "", m.cfg.Store.FinishOp(ctx, op.ID, storage.OpAborted)
	}
	rescued := ""
	if in.Checkout != "" {
		if _, err := os.Stat(in.Checkout); err == nil {
			synced, err := m.checkoutAt(ctx, in.Checkout, in.New)
			if err != nil {
				return false, "", err
			}
			if !synced {
				if rescued, err = m.syncCheckout(ctx, in.Checkout, in.Old, in.New); err != nil {
					return false, rescued, err
				}
			}
		}
	}
	return true, rescued, m.cfg.Store.FinishOp(ctx, op.ID, storage.OpDone)
}

// checkoutAt reports whether a checkout's index and tracked files match commit.
func (m *Manager) checkoutAt(ctx context.Context, checkout, commit string) (bool, error) {
	_, code1, _, err := m.gitRC(ctx, checkout, nil, "diff-index", "--quiet", "--cached", commit, "--")
	if err != nil {
		return false, err
	}
	_, code2, _, err := m.gitRC(ctx, checkout, nil, "diff-files", "--quiet")
	if err != nil {
		return false, err
	}
	return code1 == 0 && code2 == 0, nil
}

// clearStaleLocks removes git *.lock files that a crashed Spine operation left
// behind. A lock is removed only if it predates this Recover and no other
// live process is working inside the repository or Root. The index.lock of a
// non-Spine checkout (the operator's) is never removed unless Spine was
// syncing that checkout when it crashed; otherwise it is reported.
func (m *Manager) clearStaleLocks(ctx context.Context, boot time.Time, syncing map[string]bool) (removed, reported []string, err error) {
	locks, tmp := m.findLocks()
	for _, f := range tmp {
		_ = os.Remove(f)
	}
	if len(locks) == 0 {
		return nil, nil, nil
	}
	busy := len(procsUnder(m.primary, m.cfg.Root, m.commonDir)) > 0
	leaseGitDirs, err := m.leaseGitDirs(ctx)
	if err != nil {
		return nil, nil, err
	}
	for _, f := range locks {
		fi, statErr := os.Stat(f)
		if statErr != nil {
			continue
		}
		dir := filepath.Dir(f)
		operatorIndex := filepath.Base(f) == "index.lock" && !leaseGitDirs[dir]
		switch {
		case busy || !fi.ModTime().Before(boot):
			reported = append(reported, f)
		case operatorIndex && !syncing[dir]:
			reported = append(reported, f)
		default:
			if os.Remove(f) == nil {
				removed = append(removed, f)
			}
		}
	}
	return removed, reported, nil
}

// gcLockDir is where vendored Gas City worktree.Ensure keeps its per-path
// flock files (third_party/gascity/worktree/lock.go).
const gcLockDir = "gc-worktree-locks"

// findLocks lists *.lock files in the common dir (refs, packed-refs, config,
// HEAD, index, worktrees/<id>/*) and leftover Spine temporary indexes.
func (m *Manager) findLocks() (locks, tmp []string) {
	_ = filepath.WalkDir(m.commonDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch p {
			// objects/ never holds *.lock; spine/ and gc-worktree-locks/ hold
			// flock files that must never be unlinked (Gas City lock.go:34).
			case filepath.Join(m.commonDir, "objects"), filepath.Join(m.commonDir, "spine"),
				filepath.Join(m.commonDir, gcLockDir):
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		switch {
		case strings.HasPrefix(name, "spine-") && strings.Contains(name, ".idx"):
			tmp = append(tmp, p)
		case strings.HasSuffix(name, ".lock"):
			locks = append(locks, p)
		}
		return nil
	})
	return locks, tmp
}

func (m *Manager) leaseGitDirs(ctx context.Context) (map[string]bool, error) {
	res := map[string]bool{}
	dir := filepath.Join(m.commonDir, "worktrees")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return res, nil
		}
		return nil, err
	}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(dir, e.Name(), "gitdir"))
		if err != nil {
			continue
		}
		wt := filepath.Dir(strings.TrimSpace(string(b))) // <worktree>/.git → <worktree>
		if m.isLeasePath(wt) {
			res[filepath.Join(dir, e.Name())] = true
		}
	}
	return res, nil
}

// procsUnder returns pids (other than this process) whose cwd is inside any dir.
func procsUnder(dirs ...string) []int {
	self := os.Getpid()
	var out []int
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == self {
			continue
		}
		cwd, err := os.Readlink(filepath.Join("/proc", e.Name(), "cwd"))
		if err != nil {
			continue
		}
		for _, d := range dirs {
			if d != "" && within(cwd, d) {
				out = append(out, pid)
				break
			}
		}
	}
	return out
}

// killOrphans kills reparented orphans (parent is init or a subreaper)
// working inside the repository, the common dir or Root. A live operator's
// git has a live parent (their shell) and is never touched. Returns the pids
// killed after waiting for them to exit.
func (m *Manager) killOrphans() []int {
	sub := pidutil.DetectUserSubreaperPID(os.Getpid())
	var killed []int
	for _, pid := range procsUnder(m.primary, m.commonDir, m.cfg.Root) {
		ppid, err := pidutil.ParentPID(pid)
		if err != nil || !pidutil.IsReparentedOrphan(ppid, sub) {
			continue
		}
		_ = syscall.Kill(pid, syscall.SIGKILL)
		killed = append(killed, pid)
	}
	waitGone(killed, 5*time.Second)
	return killed
}

// waitGone waits until none of pids is alive (zombies count as gone).
func waitGone(pids []int, limit time.Duration) {
	deadline := time.Now().Add(limit)
	for _, pid := range pids {
		for time.Now().Before(deadline) && pidutil.Alive(pid) {
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func killProcsUnder(root string) []int {
	pids := procsUnder(root)
	for _, pid := range pids {
		if pgid, err := syscall.Getpgid(pid); err == nil && pgid > 1 && pgid != syscall.Getpgrp() {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		}
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
	return pids
}
