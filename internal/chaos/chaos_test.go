// Package chaos is the M0 crash-recovery harness (RFC 0001 §10, M0).
//
// TestChaos repeatedly starts a child "controller" process that performs a
// random stream of worktree operations — create, agent edits and commits,
// long-running agent processes, snapshot, merge into a checked-out main,
// rollback, teardown — and SIGKILLs it at a random moment. After each kill
// the parent recovers exactly as a restarting controller would and asserts:
//
//   - git fsck is clean and no *.lock files or temporary indexes remain
//   - no process survives inside the repository or the worktree root
//   - every lease is Ready or Released, and matches what git has registered
//   - every snapshot the child acknowledged still exists with the same commit
//   - every merge the child acknowledged is contained in main
//   - the operator's primary checkout is clean and at main
//
// SPINE_CHAOS_ITERS sets the iteration count (default 40; the M0 exit
// criterion is 1000) and SPINE_CHAOS_SEED the base seed.
package chaos

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	sexec "github.com/patcarter883/spine-next/internal/exec"
	"github.com/patcarter883/spine-next/internal/storage"
	"github.com/patcarter883/spine-next/internal/worktree"
)

func envInt(k string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(k)); err == nil {
		return v
	}
	return def
}

func git(t testing.TB, dir string, args ...string) string {
	t.Helper()
	cmd := osexec.Command("git", append([]string{"-c", "user.name=op", "-c", "user.email=op@x"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func gitOK(dir string, args ...string) bool {
	cmd := osexec.Command("git", args...)
	cmd.Dir = dir
	return cmd.Run() == nil
}

func open(t testing.TB, repo, root string) (*worktree.Manager, *storage.Store) {
	t.Helper()
	ctx := context.Background()
	cd, err := worktree.ResolveCommonDir(repo)
	if err != nil {
		t.Fatal(err)
	}
	st, err := storage.Open(ctx, worktree.DatabasePath(cd), storage.Options{LockWait: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	m, err := worktree.New(ctx, worktree.Config{RepoDir: repo, Root: root, Store: st, LockWait: 10 * time.Second})
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	return m, st
}

func TestChaos(t *testing.T) {
	if os.Getenv("SPINE_CHAOS_CHILD") != "" {
		t.Skip("child mode")
	}
	iters := envInt("SPINE_CHAOS_ITERS", 40)
	seed := envInt("SPINE_CHAOS_SEED", int(time.Now().UnixNano()%1e9))
	t.Logf("iterations=%d seed=%d", iters, seed)
	rng := rand.New(rand.NewPCG(uint64(seed), 0x5917e))

	repo := t.TempDir()
	root := filepath.Join(t.TempDir(), "wt")
	git(t, repo, "init", "-q", "-b", "main")
	for i := 0; i < 6; i++ {
		_ = os.WriteFile(filepath.Join(repo, fmt.Sprintf("f%d.txt", i)), []byte("base\n"), 0o644)
	}
	_ = os.WriteFile(filepath.Join(repo, "shared.txt"), []byte("base\n"), 0o644)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-qm", "init")

	var acked struct {
		sync.Mutex
		snaps  map[string]string // ref → commit
		merges []string
	}
	acked.snaps = map[string]string{}
	stats := map[string]int{}

	for i := 0; i < iters; i++ {
		cmd := osexec.Command(os.Args[0], "-test.run=^TestChaosChild$", "-test.count=1")
		cmd.Env = append(os.Environ(), "SPINE_CHAOS_CHILD=1", "SPINE_CHAOS_REPO="+repo,
			"SPINE_CHAOS_ROOT="+root, "SPINE_CHAOS_CHILD_SEED="+strconv.Itoa(seed*1000+i))
		out, _ := cmd.StdoutPipe()
		cmd.Stderr = nil
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		readDone := make(chan struct{})
		go func() {
			defer close(readDone)
			sc := bufio.NewScanner(out)
			for sc.Scan() {
				f := strings.Fields(sc.Text())
				acked.Lock()
				switch {
				case len(f) == 3 && f[0] == "ACK-SNAP":
					acked.snaps[f[1]] = f[2]
				case len(f) == 2 && f[0] == "ACK-MERGE":
					acked.merges = append(acked.merges, f[1])
				case len(f) == 2 && f[0] == "OP":
					stats[f[1]]++
				}
				acked.Unlock()
			}
		}()
		// Kill somewhere in a 30–700 ms window: child start-up and store open
		// take a few ms, so most kills land mid-operation.
		time.Sleep(time.Duration(30+rng.IntN(670)) * time.Millisecond)
		_ = cmd.Process.Signal(syscall.SIGKILL)
		_ = cmd.Wait()
		<-readDone

		m, st := open(t, repo, root)
		ctx := context.Background()
		rep, err := m.Recover(ctx)
		if err != nil {
			st.Close()
			t.Fatalf("iteration %d: recover: %v\nreport: %+v", i, err, rep)
		}
		stats["recover.procs_reaped"] += rep.ProcsReaped + len(rep.StrayProcsKilled)
		stats["recover.locks_removed"] += len(rep.LocksRemoved)
		stats["recover.snapshots_recovered"] += rep.SnapshotsRecovered
		stats["recover.merges_completed"] += rep.MergesCompleted
		stats["recover.leases_discarded"] += rep.LeasesDiscarded
		stats["recover.leases_torn_down"] += rep.LeasesTornDown
		stats["recover.rescued"] += len(rep.Rescued)

		var v []string
		if vs, err := m.Check(ctx); err != nil {
			t.Fatalf("iteration %d: check: %v", i, err)
		} else {
			v = append(v, vs...)
		}
		for _, l := range rep.LocksReported {
			v = append(v, "lock left in place: "+l)
		}
		acked.Lock()
		for ref, commit := range acked.snaps {
			cmd := osexec.Command("git", "rev-parse", "--verify", "-q", ref)
			cmd.Dir = repo
			got, _ := cmd.Output()
			if strings.TrimSpace(string(got)) != commit {
				v = append(v, fmt.Sprintf("acknowledged snapshot %s lost (want %s, got %q)", ref, commit, strings.TrimSpace(string(got))))
			}
		}
		for _, c := range acked.merges {
			if !gitOK(repo, "merge-base", "--is-ancestor", c, "refs/heads/main") {
				v = append(v, "acknowledged merge "+c+" not in main")
			}
		}
		acked.Unlock()
		if s := git(t, repo, "status", "--porcelain", "--untracked-files=no"); s != "" {
			v = append(v, "primary checkout dirty:\n"+s)
		}
		if git(t, repo, "symbolic-ref", "HEAD") != "refs/heads/main" {
			v = append(v, "primary checkout no longer on main")
		}
		if len(v) > 0 {
			st.Close()
			t.Fatalf("iteration %d (seed %d): invariants violated:\n  %s\nrecovery report: %+v", i, seed, strings.Join(v, "\n  "), rep)
		}
		st.Close()
	}
	acked.Lock()
	t.Logf("acknowledged: %d snapshots, %d merges", len(acked.snaps), len(acked.merges))
	acked.Unlock()
	t.Logf("operations started / recovery actions: %v", stats)
}

// TestChaosChild is the process the harness kills. It never returns normally.
func TestChaosChild(t *testing.T) {
	if os.Getenv("SPINE_CHAOS_CHILD") == "" {
		t.Skip("only run by TestChaos")
	}
	repo, root := os.Getenv("SPINE_CHAOS_REPO"), os.Getenv("SPINE_CHAOS_ROOT")
	rng := rand.New(rand.NewPCG(uint64(envInt("SPINE_CHAOS_CHILD_SEED", 1)), 7))
	m, st := open(t, repo, root)
	defer st.Close()
	ctx := context.Background()
	say := func(f string, a ...any) { fmt.Fprintf(os.Stdout, f+"\n", a...) }

	ready := func() []storage.Lease {
		ls, err := st.ListLeases(ctx, storage.LeaseReady)
		if err != nil {
			t.Fatal(err)
		}
		return ls
	}
	pick := func() *storage.Lease {
		ls := ready()
		if len(ls) == 0 {
			return nil
		}
		l := ls[rng.IntN(len(ls))]
		return &l
	}
	// agent runs a command in a lease the way an agent CLI would, without the
	// parent-death signal so a kill leaves it orphaned for Recover to find.
	agent := func(l *storage.Lease, argv ...string) {
		_, _ = m.Run(ctx, l, sexec.Cmd{Argv: argv, Env: append(os.Environ(),
			"GIT_AUTHOR_NAME=agent", "GIT_AUTHOR_EMAIL=a@x", "GIT_COMMITTER_NAME=agent", "GIT_COMMITTER_EMAIL=a@x"),
			DisableParentDeathSignal: true, Timeout: 5 * time.Second})
	}

	for n := 0; ; n++ {
		switch op := rng.IntN(10); {
		case op == 0 || len(ready()) == 0:
			say("OP create")
			task := fmt.Sprintf("t%d", rng.IntN(6))
			attempt := 1
			ls, _ := st.ListLeases(ctx)
			for _, l := range ls {
				if l.TaskID == task && l.Attempt >= attempt {
					attempt = l.Attempt + 1
					if l.State == storage.LeaseReady {
						attempt = l.Attempt
					}
				}
			}
			if _, err := m.Create(ctx, task, attempt, "main"); err != nil && !errors.Is(err, worktree.ErrLeaseReleased) {
				t.Fatalf("create: %v", err)
			}
		case op <= 2:
			say("OP edit")
			l := pick()
			file := "f" + strings.TrimPrefix(l.TaskID, "t") + ".txt"
			if rng.IntN(5) == 0 {
				file = "shared.txt" // occasional cross-task conflicts
			}
			_ = os.WriteFile(filepath.Join(l.Path, file), []byte(fmt.Sprintf("%s %d %d\n", l.TaskID, n, rng.Int())), 0o644)
			_ = os.WriteFile(filepath.Join(l.Path, fmt.Sprintf("new-%s-%d.txt", l.TaskID, n)), []byte("x\n"), 0o644)
			if rng.IntN(3) == 0 {
				say("OP agent-commit")
				agent(l, "sh", "-c", "git add -A && git commit -qm agent-work")
			}
		case op == 3:
			say("OP agent-run")
			if l := pick(); l != nil {
				agent(l, "sleep", fmt.Sprintf("0.%d", 1+rng.IntN(4)))
			}
		case op <= 5:
			say("OP snapshot")
			if l := pick(); l != nil {
				s, err := m.Snapshot(ctx, l, "chaos")
				if err != nil {
					t.Fatalf("snapshot: %v", err)
				}
				say("ACK-SNAP %s %s", s.Ref, s.CommitOID)
			}
		case op == 6:
			say("OP merge")
			if l := pick(); l != nil {
				snaps, _ := st.ListSnapshots(ctx, l.TaskID)
				if len(snaps) > 0 {
					s := snaps[len(snaps)-1]
					res, err := m.Merge(ctx, &s, "refs/heads/main", "chaos: "+l.TaskID)
					switch {
					case err == nil:
						say("ACK-MERGE %s", res.NewHead)
					case errors.Is(err, worktree.ErrConflict), errors.Is(err, worktree.ErrBaseMoved):
					default:
						t.Fatalf("merge: %v", err)
					}
				}
			}
		case op == 7:
			say("OP rollback")
			if l := pick(); l != nil {
				snaps, _ := st.ListSnapshots(ctx, l.TaskID)
				if len(snaps) > 0 {
					s := snaps[rng.IntN(len(snaps))]
					if err := m.Rollback(ctx, l, &s); err != nil {
						t.Fatalf("rollback: %v", err)
					}
				}
			}
		default:
			say("OP teardown")
			if l := pick(); l != nil {
				if err := m.Teardown(ctx, l); err != nil && !errors.Is(err, worktree.ErrLeaseHeld) {
					t.Fatalf("teardown: %v", err)
				}
			}
		}
	}
}
