package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sexec "github.com/patcarter883/spine-next/internal/exec"
	"github.com/patcarter883/spine-next/internal/storage"
)

type fixture struct {
	t    *testing.T
	repo string
	m    *Manager
	st   *storage.Store
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	repo := t.TempDir()
	run(t, repo, "init", "-q", "-b", "main")
	write(t, filepath.Join(repo, "a.txt"), "one\n")
	write(t, filepath.Join(repo, ".gitignore"), "*.log\n")
	run(t, repo, "add", ".")
	run(t, repo, "commit", "-qm", "init")
	return openFixture(t, repo, filepath.Join(t.TempDir(), "wt"))
}

func openFixture(t *testing.T, repo, root string) *fixture {
	t.Helper()
	ctx := context.Background()
	cd, err := ResolveCommonDir(repo)
	if err != nil {
		t.Fatal(err)
	}
	st, err := storage.Open(ctx, DatabasePath(cd), storage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	m, err := New(ctx, Config{RepoDir: repo, Root: root, Store: st, LockWait: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, repo: repo, m: m, st: st}
}

func write(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func (f *fixture) healthy() {
	f.t.Helper()
	v, err := f.m.Check(context.Background())
	if err != nil {
		f.t.Fatal(err)
	}
	if len(v) > 0 {
		f.t.Fatalf("invariants violated:\n  %s", strings.Join(v, "\n  "))
	}
}

func TestRootInsidePrimaryRejected(t *testing.T) {
	f := newFixture(t)
	_, err := New(context.Background(), Config{RepoDir: f.repo, Root: filepath.Join(f.repo, "wt"), Store: f.st})
	if err == nil {
		t.Fatal("root inside the primary checkout must be rejected")
	}
}

func TestLifecycle(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	l, err := f.m.Create(ctx, "t1", 1, "main")
	if err != nil {
		t.Fatal(err)
	}
	if l.State != storage.LeaseReady || l.AttemptID == "" {
		t.Fatalf("lease %+v", l)
	}
	again, err := f.m.Create(ctx, "t1", 1, "main")
	if err != nil || again.Path != l.Path || again.AttemptID != l.AttemptID {
		t.Fatalf("Create not idempotent: %+v %v", again, err)
	}
	f.healthy()

	// Work: modify tracked, add untracked, add ignored.
	write(t, filepath.Join(l.Path, "a.txt"), "two\n")
	write(t, filepath.Join(l.Path, "new/b.txt"), "b\n")
	write(t, filepath.Join(l.Path, "build.log"), "ignored\n")
	idxBefore := run(t, l.Path, "ls-files", "--stage")

	s1, err := f.m.Snapshot(ctx, l, "post-worker")
	if err != nil {
		t.Fatal(err)
	}
	if got := run(t, l.Path, "ls-files", "--stage"); got != idxBefore {
		t.Fatal("Snapshot modified the worktree index")
	}
	if got := run(t, f.repo, "show", s1.CommitOID+":new/b.txt"); got != "b" {
		t.Fatalf("untracked file not captured: %q", got)
	}
	if _, code := gitCode(f.repo, "cat-file", "-e", s1.CommitOID+":build.log"); code == 0 {
		t.Fatal("ignored file captured")
	}

	// More work, then roll back to s1.
	write(t, filepath.Join(l.Path, "a.txt"), "three\n")
	write(t, filepath.Join(l.Path, "junk.txt"), "x\n")
	if err := f.m.Rollback(ctx, l, s1); err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(l.Path, "a.txt")) != "two\n" || read(t, filepath.Join(l.Path, "new/b.txt")) != "b\n" {
		t.Fatal("rollback did not restore snapshot content")
	}
	if _, err := os.Stat(filepath.Join(l.Path, "junk.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("rollback kept a file not in the snapshot")
	}
	if !strings.Contains(run(t, l.Path, "status", "--porcelain"), "?? new/") {
		t.Fatal("files untracked at snapshot time should be untracked again")
	}

	pre, err := f.m.Preflight(ctx, s1, "refs/heads/main")
	if err != nil || !pre.Clean {
		t.Fatalf("preflight %+v %v", pre, err)
	}
	res, err := f.m.Merge(ctx, s1, "refs/heads/main", "t1: integrate")
	if err != nil {
		t.Fatal(err)
	}
	if run(t, f.repo, "rev-parse", "main") != res.NewHead {
		t.Fatal("main not advanced")
	}
	// The primary checkout had main checked out: it must be moved forward, clean.
	if st := run(t, f.repo, "status", "--porcelain"); st != "" {
		t.Fatalf("primary checkout left dirty after merge:\n%s", st)
	}
	if read(t, filepath.Join(f.repo, "a.txt")) != "two\n" {
		t.Fatal("primary files not synced")
	}

	if err := f.m.Teardown(ctx, l); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(l.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("worktree dir survived teardown")
	}
	if _, code := gitCode(f.repo, "rev-parse", "--verify", "-q", "refs/heads/"+l.Branch); code == 0 {
		t.Fatal("branch survived teardown")
	}
	if _, code := gitCode(f.repo, "rev-parse", "--verify", "-q", s1.Ref); code != 0 {
		t.Fatal("snapshot ref lost in teardown")
	}
	if err := f.m.Teardown(ctx, l); err != nil {
		t.Fatalf("teardown not idempotent: %v", err)
	}
	f.healthy()
}

func TestTeardownCapturesUnsnapshottedWorkAndAgentCommits(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	l, err := f.m.Create(ctx, "t2", 1, "main")
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(l.Path, "c.txt"), "agent commit\n")
	run(t, l.Path, "add", "c.txt")
	run(t, l.Path, "commit", "-qm", "agent")
	agentHead := run(t, l.Path, "rev-parse", "HEAD")
	write(t, filepath.Join(l.Path, "d.txt"), "uncommitted\n")
	if err := f.m.Teardown(ctx, l); err != nil {
		t.Fatal(err)
	}
	snaps, _ := f.st.ListSnapshots(ctx, "t2")
	if len(snaps) != 1 {
		t.Fatalf("want 1 teardown snapshot, got %d", len(snaps))
	}
	if got := run(t, f.repo, "show", snaps[0].CommitOID+":d.txt"); got != "uncommitted" {
		t.Fatal("uncommitted work lost")
	}
	if _, code := gitCode(f.repo, "merge-base", "--is-ancestor", agentHead, snaps[0].CommitOID); code != 0 {
		t.Fatal("agent commit not reachable from snapshot")
	}
	f.healthy()
}

func TestMergeConflictAndBaseMoved(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	l1, _ := f.m.Create(ctx, "c1", 1, "main")
	l2, _ := f.m.Create(ctx, "c2", 1, "main")
	write(t, filepath.Join(l1.Path, "a.txt"), "from c1\n")
	write(t, filepath.Join(l2.Path, "a.txt"), "from c2\n")
	s1, _ := f.m.Snapshot(ctx, l1, "x")
	s2, _ := f.m.Snapshot(ctx, l2, "x")
	if _, err := f.m.Merge(ctx, s1, "refs/heads/main", "c1"); err != nil {
		t.Fatal(err)
	}
	res, err := f.m.Merge(ctx, s2, "refs/heads/main", "c2")
	if !errors.Is(err, ErrConflict) || len(res.Conflicts) != 1 || res.Conflicts[0] != "a.txt" {
		t.Fatalf("want conflict on a.txt, got %+v %v", res, err)
	}
	for _, l := range []*Lease{l1, l2} {
		if err := f.m.Teardown(ctx, l); err != nil {
			t.Fatal(err)
		}
	}
	f.healthy()
}

func TestMergeRefusesDirtyCheckout(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	l, _ := f.m.Create(ctx, "d1", 1, "main")
	write(t, filepath.Join(l.Path, "z.txt"), "z\n")
	s, _ := f.m.Snapshot(ctx, l, "x")
	write(t, filepath.Join(f.repo, "a.txt"), "operator edit\n")
	before := run(t, f.repo, "rev-parse", "main")
	if _, err := f.m.Merge(ctx, s, "refs/heads/main", "d1"); !errors.Is(err, ErrCheckoutDirty) {
		t.Fatalf("err=%v", err)
	}
	if run(t, f.repo, "rev-parse", "main") != before || read(t, filepath.Join(f.repo, "a.txt")) != "operator edit\n" {
		t.Fatal("dirty checkout was touched")
	}
}

func TestHookFailureDiscardsLease(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.m.cfg.Hooks = []Hook{{Argv: []string{"sh", "-c", "echo nope >&2; exit 7"}}}
	if _, err := f.m.Create(ctx, "h1", 1, "main"); !errors.Is(err, ErrProvisionHook) {
		t.Fatalf("err=%v", err)
	}
	l, _ := f.st.GetLease(ctx, "h1", 1)
	if l.State != storage.LeaseReleased {
		t.Fatalf("state=%s", l.State)
	}
	f.healthy()
}

func TestTeardownRefusesLiveProcess(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	l, _ := f.m.Create(ctx, "p1", 1, "main")
	started := make(chan struct{})
	done := make(chan struct{})
	runCtx, cancel := context.WithCancel(ctx)
	go func() {
		defer close(done)
		_, _ = f.m.Run(runCtx, l, sexec.Cmd{Argv: []string{"sleep", "30"}, Grace: 100 * time.Millisecond,
			OnStart: func(sexec.Started) error { close(started); return nil }})
	}()
	<-started
	time.Sleep(50 * time.Millisecond) // let RecordProc commit
	if err := f.m.Teardown(ctx, l); !errors.Is(err, ErrLeaseHeld) {
		t.Fatalf("err=%v", err)
	}
	cancel()
	<-done
	if err := f.m.Teardown(ctx, l); err != nil {
		t.Fatal(err)
	}
	f.healthy()
}

func TestRecoverResolvesInterruptedState(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	l, _ := f.m.Create(ctx, "r1", 1, "main")
	write(t, filepath.Join(l.Path, "w.txt"), "w\n")

	// Simulated crashes: a Requested lease, a Draining lease, a snapshot whose
	// ref landed but whose row did not, a stale ref lock and an orphan process.
	half := Lease{TaskID: "r2", Attempt: 1, Path: f.m.LeasePath("r2", 1), Branch: LeaseBranch("r2", 1),
		BaseOID: run(t, f.repo, "rev-parse", "main"), State: storage.LeaseRequested}
	_ = f.st.PutLease(ctx, half)
	run(t, f.repo, "worktree", "add", "-q", "-b", half.Branch, half.Path, half.BaseOID)

	ref := SnapshotRefPrefix + "r1/7"
	op, _ := f.st.BeginOp(ctx, "r1", "snapshot", snapshotIntent{Seq: 7, Ref: ref, Path: l.Path})
	_ = op
	run(t, f.repo, "update-ref", ref, run(t, f.repo, "rev-parse", "main"))

	lockFile := filepath.Join(f.m.CommonDir(), "refs", "heads", "main.lock")
	write(t, lockFile, "")
	past := time.Now().Add(-time.Minute)
	_ = os.Chtimes(lockFile, past, past)

	cmd := exec.Command("sleep", "60")
	cmd.Dir = l.Path
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	// Close the stdin/out the test harness may hold so the reaper can finish.
	go cmd.Wait()

	rep, err := f.m.Recover(ctx)
	if err != nil {
		t.Fatalf("recover: %v (%+v)", err, rep)
	}
	if rep.LeasesDiscarded != 1 || rep.SnapshotsRecovered != 1 || len(rep.LocksRemoved) != 1 || len(rep.StrayProcsKilled) != 1 {
		t.Fatalf("report %+v", rep)
	}
	time.Sleep(100 * time.Millisecond)
	f.healthy()
}

func gitCode(dir string, args ...string) (string, int) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if ee, ok := err.(*exec.ExitError); ok {
		return string(out), ee.ExitCode()
	}
	return string(out), 0
}

// A `git worktree add` killed mid-way leaves a registration whose checkout has
// no .git file; `git worktree remove` rejects it (found by the chaos harness).
func TestRecoverBrokenCheckout(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	base := run(t, f.repo, "rev-parse", "main")

	half := Lease{TaskID: "b1", Attempt: 1, Path: f.m.LeasePath("b1", 1), Branch: LeaseBranch("b1", 1),
		BaseOID: base, State: storage.LeaseRequested}
	_ = f.st.PutLease(ctx, half)
	run(t, f.repo, "worktree", "add", "-q", "-b", half.Branch, half.Path, base)
	if err := os.Remove(filepath.Join(half.Path, ".git")); err != nil {
		t.Fatal(err)
	}

	// A Draining lease whose removal was interrupted after its snapshot.
	l, err := f.m.Create(ctx, "b2", 1, "main")
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(l.Path, "w.txt"), "w\n")
	if _, err := f.m.Snapshot(ctx, l, "pre-remove"); err != nil {
		t.Fatal(err)
	}
	l.State = storage.LeaseDraining
	_ = f.st.PutLease(ctx, *l)
	_ = os.Remove(filepath.Join(l.Path, ".git"))

	// A Ready lease with a broken checkout must be refused, not deleted.
	r, _ := f.m.Create(ctx, "b3", 1, "main")
	_ = os.Remove(filepath.Join(r.Path, ".git"))
	if err := f.m.Teardown(ctx, r); !errors.Is(err, ErrBrokenWorktree) {
		t.Fatalf("teardown of broken Ready lease: %v", err)
	}
	if _, err := os.Stat(r.Path); err != nil {
		t.Fatal("broken Ready lease directory was deleted")
	}
	_ = os.RemoveAll(r.Path)
	_ = f.st.PutLease(ctx, Lease{TaskID: r.TaskID, Attempt: r.Attempt, Path: r.Path, Branch: r.Branch,
		BaseOID: r.BaseOID, AttemptID: r.AttemptID, State: storage.LeaseReleased})

	rep, err := f.m.Recover(ctx)
	if err != nil {
		t.Fatalf("recover: %v (%+v)", err, rep)
	}
	if rep.LeasesDiscarded != 1 || rep.LeasesTornDown != 1 {
		t.Fatalf("report %+v", rep)
	}
}

// `git worktree add` locks its new entry while initializing and `prune` skips
// locked entries, so a kill mid-add strands one (found by the chaos harness).
func TestRecoverPurgesInitializingRegistration(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	base := run(t, f.repo, "rev-parse", "main")
	for i, withGitdir := range []bool{true, false} {
		task := fmt.Sprintf("i%d", i)
		l := Lease{TaskID: task, Attempt: 1, Path: f.m.LeasePath(task, 1), Branch: LeaseBranch(task, 1),
			BaseOID: base, State: storage.LeaseRequested}
		_ = f.st.PutLease(ctx, l)
		entry := filepath.Join(f.m.CommonDir(), "worktrees", filepath.Base(l.Path))
		write(t, filepath.Join(entry, "locked"), "initializing")
		if withGitdir {
			write(t, filepath.Join(entry, "gitdir"), filepath.Join(l.Path, ".git")+"\n")
			write(t, filepath.Join(entry, "HEAD"), "")
		}
	}
	if rep, err := f.m.Recover(ctx); err != nil || rep.LeasesDiscarded != 2 {
		t.Fatalf("recover: %v %+v", err, rep)
	}
	f.healthy()
}
