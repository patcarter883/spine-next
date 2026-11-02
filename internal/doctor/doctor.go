// Package doctor implements `spine doctor`: environment and repository checks
// that must pass before a controller is allowed to start (RFC 0001 §6.2, §7.1).
package doctor

import (
	"context"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"syscall"

	"github.com/patcarter883/spine-next/internal/storage"
	"github.com/patcarter883/spine-next/internal/worktree"
)

// Level is a finding's severity.
type Level string

const (
	OK   Level = "ok"
	Warn Level = "warn"
	Fail Level = "fail"
)

// Finding is one check result.
type Finding struct {
	Level   Level
	Check   string
	Message string
}

// MinGit is the oldest git with `merge-tree --write-tree` (RFC §6.2).
var MinGit = [3]int{2, 38, 0}

// Run performs every check against the repository containing repoDir.
func Run(ctx context.Context, repoDir string) []Finding {
	var fs []Finding
	add := func(l Level, c, f string, a ...any) { fs = append(fs, Finding{l, c, fmt.Sprintf(f, a...)}) }

	if v, err := gitVersion(); err != nil {
		add(Fail, "git", "git not runnable: %v", err)
		return fs
	} else if less(v, MinGit) {
		add(Fail, "git", "git %d.%d.%d is older than %d.%d (merge-tree --write-tree)", v[0], v[1], v[2], MinGit[0], MinGit[1])
	} else {
		add(OK, "git", "git %d.%d.%d", v[0], v[1], v[2])
	}

	cd, err := worktree.ResolveCommonDir(repoDir)
	if err != nil {
		add(Fail, "repo", "%s is not a git repository: %v", repoDir, err)
		return fs
	}
	add(OK, "repo", "common dir %s", cd)

	dbPath := worktree.DatabasePath(cd)
	if name, network := networkFS(filepath.Dir(cd)); network {
		add(Fail, "filesystem", "%s is on %s; SQLite WAL is unsafe on network filesystems", cd, name)
	} else {
		add(OK, "filesystem", "%s", name)
	}

	st, err := storage.Open(ctx, dbPath, storage.Options{LockWait: 0})
	if err != nil {
		if err == storage.ErrLocked {
			add(Warn, "store", "database is held by a running controller; skipping store and invariant checks")
			return fs
		}
		add(Fail, "store", "cannot open %s: %v", dbPath, err)
		return fs
	}
	defer st.Close()
	if mode, err := st.JournalMode(ctx); err != nil || mode != "wal" {
		add(Fail, "store", "journal_mode=%q err=%v", mode, err)
	} else if err := st.QuickCheck(ctx); err != nil {
		add(Fail, "store", "%v", err)
	} else {
		add(OK, "store", "%s (WAL, quick_check ok)", dbPath)
	}
	if fi, err := os.Stat(dbPath + "-wal"); err == nil && fi.Size() > 64<<20 {
		add(Warn, "store", "WAL is %d MiB; a long-lived reader may be starving checkpoints (run spine gc)", fi.Size()>>20)
	}

	m, err := worktree.New(ctx, worktree.Config{RepoDir: repoDir, Store: st})
	if err != nil {
		add(Fail, "worktree", "%v", err)
		return fs
	}
	add(OK, "worktree", "root %s", m.Root())
	v, err := m.Check(ctx)
	switch {
	case err != nil:
		add(Fail, "invariants", "%v", err)
	case len(v) > 0:
		for _, s := range v {
			add(Fail, "invariants", "%s (run spine recover)", s)
		}
	default:
		add(OK, "invariants", "fsck, locks, processes, leases and snapshots consistent")
	}
	return fs
}

var versionRe = regexp.MustCompile(`(\d+)\.(\d+)(?:\.(\d+))?`)

func gitVersion() ([3]int, error) {
	out, err := osexec.Command("git", "version").Output()
	if err != nil {
		return [3]int{}, err
	}
	m := versionRe.FindStringSubmatch(string(out))
	if m == nil {
		return [3]int{}, fmt.Errorf("unparseable %q", out)
	}
	var v [3]int
	for i := 0; i < 3; i++ {
		v[i], _ = strconv.Atoi(m[i+1])
	}
	return v, nil
}

func less(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// networkFS classifies the filesystem holding dir by statfs magic.
func networkFS(dir string) (string, bool) {
	var s syscall.Statfs_t
	if err := syscall.Statfs(dir, &s); err != nil {
		return "unknown (" + err.Error() + ")", false
	}
	switch uint32(s.Type) {
	case 0x6969:
		return "nfs", true
	case 0x517B:
		return "smb", true
	case 0xFF534D42:
		return "cifs", true
	case 0xFE534D42:
		return "smb2", true
	case 0x65735546:
		return "fuse (possibly sshfs)", true
	case 0x01021997:
		return "9p", true
	default:
		return fmt.Sprintf("local (0x%x)", uint32(s.Type)), false
	}
}
