package worktree

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// ErrLockTimeout means a lock could not be acquired within Config.LockWait. A
// hung holder becomes an infrastructure fault, not a stalled controller
// (RFC §6.2 rule 0; contrast Gas City's blocking flock, lock_unix.go:109).
var ErrLockTimeout = errors.New("worktree: lock wait exceeded")

type heldLock struct {
	mu *sync.Mutex
	f  *os.File
}

func (h heldLock) release() {
	if h.f != nil {
		_ = syscall.Flock(int(h.f.Fd()), syscall.LOCK_UN)
		_ = h.f.Close()
	}
	h.mu.Unlock()
}

// Lock files are named *.flock so they are never mistaken for git *.lock
// files, and are never unlinked: unlinking would let one process delete a file
// another is blocked on (the Gas City lock.go:34 rule).
func (m *Manager) flockFile(name string) string {
	return filepath.Join(m.commonDir, "spine", "locks", name+".flock")
}

func (m *Manager) acquire(ctx context.Context, mu *sync.Mutex, file string) (heldLock, error) {
	deadline := time.Now().Add(m.cfg.LockWait)
	for !mu.TryLock() {
		if time.Now().After(deadline) {
			return heldLock{}, fmt.Errorf("%w: %s (in-process)", ErrLockTimeout, file)
		}
		if err := sleepCtx(ctx, 5*time.Millisecond); err != nil {
			return heldLock{}, err
		}
	}
	f, err := os.OpenFile(file, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		mu.Unlock()
		return heldLock{}, err
	}
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return heldLock{mu: mu, f: f}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			f.Close()
			mu.Unlock()
			return heldLock{}, err
		}
		if time.Now().After(deadline) {
			f.Close()
			mu.Unlock()
			return heldLock{}, fmt.Errorf("%w: %s", ErrLockTimeout, file)
		}
		if err := sleepCtx(ctx, 10*time.Millisecond); err != nil {
			f.Close()
			mu.Unlock()
			return heldLock{}, err
		}
	}
}

// lockRefs serializes operations on shared refs and worktree registration
// (§6.2 rule 2).
func (m *Manager) lockRefs(ctx context.Context) (heldLock, error) {
	return m.acquire(ctx, &m.refMu, m.flockFile("refs"))
}

// lockPath serializes all operations on one worktree path (§6.2 rule 0).
func (m *Manager) lockPath(ctx context.Context, path string) (heldLock, error) {
	m.pathMu.Lock()
	mu, ok := m.paths[path]
	if !ok {
		mu = &sync.Mutex{}
		m.paths[path] = mu
	}
	m.pathMu.Unlock()
	sum := sha256.Sum256([]byte(filepath.Clean(path)))
	return m.acquire(ctx, mu, m.flockFile("wt-"+hex.EncodeToString(sum[:12])))
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
