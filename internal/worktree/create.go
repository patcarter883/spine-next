package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/patcarter883/spine-next/internal/exec"
	"github.com/patcarter883/spine-next/internal/storage"
	gcwt "github.com/patcarter883/spine-next/third_party/gascity/worktree"
)

// Create cuts a worktree for (task, attempt) at the commit base resolves to,
// runs provisioning hooks and returns a Ready lease. It is idempotent: a Ready
// lease is returned as-is and a Requested one (from a crash) is finished.
func (m *Manager) Create(ctx context.Context, task string, attempt int, base string) (*Lease, error) {
	if err := validateTask(task, attempt); err != nil {
		return nil, err
	}
	path := m.LeasePath(task, attempt)
	lk, err := m.lockPath(ctx, path)
	if err != nil {
		return nil, err
	}
	defer lk.release()

	l, err := m.cfg.Store.GetLease(ctx, task, attempt)
	switch {
	case err == nil && l.State == storage.LeaseReady:
		return &l, nil
	case err == nil && (l.State == storage.LeaseDraining || l.State == storage.LeaseReleased):
		return nil, fmt.Errorf("%w: %s attempt %d is %s", ErrLeaseReleased, task, attempt, l.State)
	case err != nil && !errors.Is(err, storage.ErrNotFound):
		return nil, err
	}

	baseOID := l.BaseOID
	if baseOID == "" {
		oid, ok, err := m.revParse(ctx, m.primary, base+"^{commit}")
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("worktree: base %q does not resolve to a commit", base)
		}
		baseOID = oid
	}
	l = Lease{
		TaskID: task, Attempt: attempt, Path: path, Branch: LeaseBranch(task, attempt),
		BaseOID: baseOID, State: storage.LeaseRequested, OwnerPID: os.Getpid(),
	}
	// Intent before effect: the Requested row exists before git runs.
	if err := m.cfg.Store.PutLease(ctx, l); err != nil {
		return nil, err
	}

	rep, err := m.ensure(ctx, l)
	if err != nil {
		return nil, errors.Join(err, m.discardRequested(ctx, l))
	}
	if rep.Provenance != nil {
		l.AttemptID = rep.Provenance.AttemptID
	}
	for _, h := range m.cfg.Hooks {
		res, err := m.Run(ctx, &l, exec.Cmd{Argv: h.Argv, Dir: path, Timeout: h.Timeout})
		if err == nil && !res.OK() {
			err = fmt.Errorf("%w: %v exit %d: %s", ErrProvisionHook, h.Argv, res.ExitCode, truncate(res.Stderr, 500))
		}
		if err != nil {
			return nil, errors.Join(err, m.discardRequested(ctx, l))
		}
	}
	l.State = storage.LeaseReady
	if err := m.cfg.Store.PutLease(ctx, l); err != nil {
		return nil, err
	}
	return &l, nil
}

func (m *Manager) ensure(ctx context.Context, l Lease) (gcwt.Report, error) {
	rl, err := m.lockRefs(ctx)
	if err != nil {
		return gcwt.Report{}, err
	}
	defer rl.release()
	return gcwt.Ensure(gcwt.Spec{
		RepoDir: m.primary, Path: l.Path, Root: m.cfg.Root, Branch: l.Branch, Base: l.BaseOID,
		BeadID: l.TaskID, StoreRef: "spine", Creator: "spine", Owner: "spine",
		Generation: strconv.Itoa(l.Attempt), Lifecycle: gcwt.LifecycleActive,
	})
}

// discardRequested reverses a lease that never became Ready. No agent ever
// ran in it, so forced removal loses nothing; the branch is deleted only if it
// still points at the base commit.
func (m *Manager) discardRequested(ctx context.Context, l Lease) error {
	if !m.isLeasePath(l.Path) {
		return fmt.Errorf("worktree: refusing to discard non-lease path %s", l.Path)
	}
	rl, err := m.lockRefs(ctx)
	if err != nil {
		return err
	}
	defer rl.release()
	var errs []error
	if registered, err := m.isRegistered(ctx, l.Path); err != nil {
		errs = append(errs, err)
	} else if registered && validCheckout(l.Path) {
		if _, err := m.git(ctx, m.primary, nil, "worktree", "remove", "--force", "--force", l.Path); err != nil {
			errs = append(errs, err)
		}
	}
	// A killed `git worktree add` can leave a registered path with no .git
	// file, which `worktree remove` rejects; the directory is deleted and the
	// registration pruned below.
	if _, statErr := os.Stat(l.Path); statErr == nil {
		errs = append(errs, os.RemoveAll(l.Path))
	}
	errs = append(errs, m.purgeRegistration(l.Path))
	if oid, ok, err := m.revParse(ctx, m.primary, "refs/heads/"+l.Branch); err != nil {
		errs = append(errs, err)
	} else if ok && oid == l.BaseOID {
		if _, err := m.git(ctx, m.primary, nil, "update-ref", "-d", "refs/heads/"+l.Branch, oid); err != nil {
			errs = append(errs, err)
		}
	}
	if _, err := m.git(ctx, m.primary, nil, "worktree", "prune"); err != nil {
		errs = append(errs, err)
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	l.State = storage.LeaseReleased
	return m.cfg.Store.PutLease(ctx, l)
}

func (m *Manager) isRegistered(ctx context.Context, path string) (bool, error) {
	wts, err := m.registered(ctx)
	if err != nil {
		return false, err
	}
	_, ok := wts[path]
	return ok, nil
}

// registered returns registered worktree paths → branch ("" if detached).
func (m *Manager) registered(ctx context.Context) (map[string]string, error) {
	out, err := m.git(ctx, m.primary, nil, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	res := map[string]string{}
	cur := ""
	for _, line := range splitLines(out) {
		switch {
		case len(line) > 9 && line[:9] == "worktree ":
			cur = line[9:]
			res[cur] = ""
		case len(line) > 7 && line[:7] == "branch " && cur != "":
			res[cur] = line[7:]
		}
	}
	return res, nil
}

// purgeRegistration deletes $GIT_COMMON_DIR/worktrees/<id> entries left for
// path by an interrupted `git worktree add` or `remove`. `git worktree add`
// locks a new entry ("initializing") until it finishes, and `git worktree
// prune` skips locked entries, so a kill mid-add leaves an entry prune never
// clears (found by the chaos harness). Callers hold the ref lock, and run only
// after Recover has killed orphans, so an initializing entry cannot be live.
func (m *Manager) purgeRegistration(path string) error {
	dir := filepath.Join(m.commonDir, "worktrees")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	base := filepath.Base(path)
	var errs []error
	for _, e := range entries {
		entry := filepath.Join(dir, e.Name())
		if b, err := os.ReadFile(filepath.Join(entry, "gitdir")); err == nil {
			if filepath.Dir(strings.TrimSpace(string(b))) == path {
				errs = append(errs, os.RemoveAll(entry))
			}
			continue
		}
		// No gitdir file yet: an add that died before writing it. Git names
		// the entry after the path's basename, plus digits on collision.
		rest, ok := strings.CutPrefix(e.Name(), base)
		if !ok || strings.Trim(rest, "0123456789") != "" {
			continue
		}
		// The lock reason is a translated string (_("initializing") in
		// builtin/worktree.c), so presence of the file is what counts.
		if _, err := os.Stat(filepath.Join(entry, "locked")); err == nil {
			errs = append(errs, os.RemoveAll(entry))
		}
	}
	return errors.Join(errs...)
}

// validCheckout reports whether path is a usable linked worktree: its .git
// file names a gitdir that exists. It does not run git, so it cannot be
// fooled by git walking up into an enclosing repository.
func validCheckout(path string) bool {
	b, err := os.ReadFile(filepath.Join(path, ".git"))
	if err != nil {
		return false
	}
	gd, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir: ")
	if !ok {
		return false
	}
	if !filepath.IsAbs(gd) {
		gd = filepath.Join(path, gd)
	}
	fi, err := os.Stat(gd)
	return err == nil && fi.IsDir()
}

func truncate(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n]) + "…"
	}
	return string(b)
}
