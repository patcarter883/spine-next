// Package worktree is Spine's WorktreeManager (RFC 0001 §3.2, §6).
//
// It owns every git operation that mutates repository state. Creation goes
// through Gas City's transactional worktree.Ensure (vendored under
// third_party/gascity), which gives fail-closed creation, attempt-ID
// provenance and rollback of partial creates. Everything else — snapshots,
// rollback, merge pre-flight, compare-and-swap integration, teardown and crash
// recovery — is Spine's and uses git plumbing only:
//
//   - Every effect is preceded by a durable intent (a lease row or an ops row)
//     in internal/storage, so Recover can finish or reverse it after kill -9.
//   - Snapshots use a temporary index file and never touch a worktree's index.
//   - Integration is merge-tree --write-tree plus update-ref <new> <old>.
//   - The primary checkout is never switched, staged or reset by Spine except
//     to move a checked-out integration branch forward (see merge.go).
package worktree

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/patcarter883/spine-next/internal/storage"
	gcgit "github.com/patcarter883/spine-next/third_party/gascity/git"
)

// Lease and Snapshot are the durable records the manager hands out.
type (
	Lease    = storage.Lease
	Snapshot = storage.Snapshot
)

// Errors returned by the manager.
var (
	ErrInvalidTask     = errors.New("worktree: invalid task id")
	ErrPrimaryTree     = errors.New("worktree: refusing to operate on the primary worktree")
	ErrLeaseHeld       = errors.New("worktree: lease has live processes")
	ErrLeaseReleased   = errors.New("worktree: lease already released")
	ErrStaleAttempt    = errors.New("worktree: provenance attempt id mismatch")
	ErrUnreachable     = errors.New("worktree: HEAD has commits reachable from no ref")
	ErrBaseMoved       = errors.New("worktree: base ref moved during merge")
	ErrConflict        = errors.New("worktree: merge has conflicts")
	ErrCheckoutDirty   = errors.New("worktree: integration branch is checked out with uncommitted changes")
	ErrProvisionHook   = errors.New("worktree: provisioning hook failed")
	ErrForeignSnapshot = errors.New("worktree: snapshot belongs to another task")
	ErrBrokenWorktree  = errors.New("worktree: checkout is not a valid git worktree")
)

// Hook is a provisioning command run inside a new worktree before its lease
// becomes Ready (RFC §1: sandbox_setup hooks).
type Hook struct {
	Argv    []string
	Timeout time.Duration
}

// Identity is the author/committer for Spine-created commits.
type Identity struct{ Name, Email string }

// Config configures a Manager.
type Config struct {
	// RepoDir is any checkout of the repository.
	RepoDir string
	// Root holds Spine worktrees as direct children. It must lie outside
	// every registered worktree. Default: DefaultRoot(commonDir).
	Root     string
	Store    *storage.Store
	Hooks    []Hook
	Identity Identity
	// LockWait bounds every lock acquisition (default 30s).
	LockWait time.Duration
	// GitTimeout bounds each git invocation (default 2m).
	GitTimeout time.Duration
}

// Manager implements the RFC §3.2 interface.
type Manager struct {
	cfg       Config
	commonDir string
	primary   string // primary worktree path (toplevel of the main checkout)

	refMu  sync.Mutex
	pathMu sync.Mutex
	paths  map[string]*sync.Mutex
}

var taskIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ResolveCommonDir returns the absolute git common dir for repoDir.
func ResolveCommonDir(repoDir string) (string, error) {
	cd, err := gcgit.New(repoDir).CommonDir()
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(cd) {
		cd = filepath.Join(repoDir, cd)
	}
	return filepath.Abs(cd)
}

// DatabasePath is where Spine keeps its database: inside the git common dir,
// outside every working tree (RFC §7, §7.1 rule 5).
func DatabasePath(commonDir string) string {
	return filepath.Join(commonDir, "spine", "spine.db")
}

// DefaultRoot returns $SPINE_WT_ROOT/<repo-hash> or, if unset, the user
// cache dir's spine/worktrees/<repo-hash>.
func DefaultRoot(commonDir string) string {
	base := os.Getenv("SPINE_WT_ROOT")
	if base == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			cache = os.TempDir()
		}
		base = filepath.Join(cache, "spine", "worktrees")
	}
	sum := sha256.Sum256([]byte(commonDir))
	return filepath.Join(base, hex.EncodeToString(sum[:6]))
}

// New validates cfg and returns a Manager.
func New(ctx context.Context, cfg Config) (*Manager, error) {
	if cfg.Store == nil {
		return nil, errors.New("worktree: Config.Store is required")
	}
	if cfg.LockWait <= 0 {
		cfg.LockWait = 30 * time.Second
	}
	if cfg.GitTimeout <= 0 {
		cfg.GitTimeout = 2 * time.Minute
	}
	if cfg.Identity.Name == "" {
		cfg.Identity = Identity{Name: "spine", Email: "spine@localhost"}
	}
	abs, err := filepath.Abs(cfg.RepoDir)
	if err != nil {
		return nil, err
	}
	cfg.RepoDir = abs
	m := &Manager{cfg: cfg, paths: map[string]*sync.Mutex{}}
	if m.commonDir, err = ResolveCommonDir(abs); err != nil {
		return nil, fmt.Errorf("worktree: %s is not a git repository: %w", abs, err)
	}
	if m.primary, err = m.primaryWorktree(ctx); err != nil {
		return nil, err
	}
	if m.cfg.Root == "" {
		m.cfg.Root = DefaultRoot(m.commonDir)
	}
	if m.cfg.Root, err = filepath.Abs(m.cfg.Root); err != nil {
		return nil, err
	}
	if within(m.cfg.Root, m.primary) {
		return nil, fmt.Errorf("worktree: root %s is inside the primary checkout %s", m.cfg.Root, m.primary)
	}
	if err := os.MkdirAll(m.cfg.Root, 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(m.commonDir, "spine", "locks"), 0o755); err != nil {
		return nil, err
	}
	return m, nil
}

// CommonDir returns the repository's git common dir.
func (m *Manager) CommonDir() string { return m.commonDir }

// Primary returns the primary worktree path.
func (m *Manager) Primary() string { return m.primary }

// Root returns the worktree root.
func (m *Manager) Root() string { return m.cfg.Root }

// Store returns the backing store.
func (m *Manager) Store() *storage.Store { return m.cfg.Store }

func (m *Manager) primaryWorktree(ctx context.Context) (string, error) {
	out, err := m.git(ctx, m.cfg.RepoDir, nil, "worktree", "list", "--porcelain")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		if p, ok := strings.CutPrefix(line, "worktree "); ok {
			return filepath.Clean(p), nil // the first entry is always the main worktree
		}
	}
	return "", errors.New("worktree: cannot determine primary worktree")
}

// LeasePath returns the worktree path for (task, attempt).
func (m *Manager) LeasePath(task string, attempt int) string {
	return filepath.Join(m.cfg.Root, task+"-a"+strconv.Itoa(attempt))
}

// LeaseBranch returns the branch name for (task, attempt).
func LeaseBranch(task string, attempt int) string {
	return "spine/" + task + "/a" + strconv.Itoa(attempt)
}

// SnapshotRefPrefix is the namespace snapshot refs live under.
const SnapshotRefPrefix = "refs/spine/snap/"

// RescueRefPrefix holds rescued operator state (see merge.go).
const RescueRefPrefix = "refs/spine/rescue/"

func within(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// isLeasePath reports whether p is a direct child of Root (never the primary).
func (m *Manager) isLeasePath(p string) bool {
	return filepath.Dir(filepath.Clean(p)) == m.cfg.Root && !within(p, m.primary)
}

func validateTask(task string, attempt int) error {
	if !taskIDRe.MatchString(task) || attempt < 1 {
		return fmt.Errorf("%w: %q attempt %d", ErrInvalidTask, task, attempt)
	}
	return nil
}
