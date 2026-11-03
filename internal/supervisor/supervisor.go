package supervisor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/patcarter883/spine-next/internal/api"
	"github.com/patcarter883/spine-next/internal/app"
	"github.com/patcarter883/spine-next/internal/storage"
)

// State of a repository under the supervisor.
type State string

const (
	StateStarting State = "starting"
	StateRunning  State = "running"
	StateBroken   State = "broken"  // last start or run failed; retried with backoff
	StateStopped  State = "stopped" // being removed or supervisor shutting down
)

// RepoStatus is a repository's current state.
type RepoStatus struct {
	Repo
	State     State     `json:"state"`
	Error     string    `json:"error,omitempty"`
	Since     time.Time `json:"since"`
	Restarts  int       `json:"restarts"`
	NextRetry time.Time `json:"next_retry,omitempty"`
}

// Supervisor runs one controller per registered repository.
type Supervisor struct {
	Registry Registry
	Config   Config
	Logf     func(string, ...any)
	// AppOptions customizes each repository's app (tests inject agents,
	// configs and decision supervisors). It must not set Repo, Limits or
	// ResolveDep; the supervisor does.
	AppOptions func(Repo) app.Options

	limits *Limits
	mu     sync.Mutex
	repos  map[string]*runner
	ctx    context.Context
}

type runner struct {
	repo   Repo
	cancel context.CancelFunc
	done   chan struct{}
	mu     sync.Mutex
	status RepoStatus
	app    *app.App
}

// ErrAlreadyRunning means another supervisor holds the per-user lock.
var ErrAlreadyRunning = errors.New("supervisor: another spine supervisor is running for this user")

// Run supervises until ctx ends. On return every controller has stopped
// and every agent it started is gone.
func (s *Supervisor) Run(ctx context.Context) error {
	if s.Logf == nil {
		s.Logf = func(string, ...any) {}
	}
	if s.Config.Reload <= 0 {
		s.Config.Reload = 2 * time.Second
	}
	unlock, err := lockHome(filepath.Dir(s.Registry.Path))
	if err != nil {
		return err
	}
	defer unlock()
	s.limits = NewLimits(map[string]int{"agent": s.Config.MaxAgents, "gate": s.Config.MaxGates})
	s.mu.Lock()
	s.repos = map[string]*runner{}
	s.ctx = ctx
	s.mu.Unlock()

	tk := time.NewTicker(s.Config.Reload)
	defer tk.Stop()
	for {
		s.sync(ctx)
		select {
		case <-ctx.Done():
			s.stopAll()
			return nil
		case <-tk.C:
		}
	}
}

// sync starts runners for new registry entries and stops removed ones.
func (s *Supervisor) sync(ctx context.Context) {
	repos, err := s.Registry.Load()
	if err != nil {
		s.Logf("registry: %v", err)
		return
	}
	want := map[string]Repo{}
	for _, r := range repos {
		want[r.Name] = r
	}
	s.mu.Lock()
	var stop []*runner
	for name, r := range s.repos {
		if w, ok := want[name]; !ok || w.Path != r.repo.Path {
			stop = append(stop, r)
			delete(s.repos, name)
		}
	}
	for name, repo := range want {
		if _, ok := s.repos[name]; !ok {
			rctx, cancel := context.WithCancel(ctx)
			r := &runner{repo: repo, cancel: cancel, done: make(chan struct{}),
				status: RepoStatus{Repo: repo, State: StateStarting, Since: time.Now()}}
			s.repos[name] = r
			go s.run(rctx, r)
		}
	}
	s.mu.Unlock()
	for _, r := range stop {
		s.Logf("%s: unregistered; stopping", r.repo.Name)
		r.cancel()
		<-r.done
	}
}

// Restart stops a repository's controller and starts it again (it rereads
// .spine/config.yaml). Restarts done this way do not count as failures.
func (s *Supervisor) Restart(name string) error {
	s.mu.Lock()
	r := s.repos[name]
	ctx := s.ctx
	if r != nil {
		delete(s.repos, name)
	}
	s.mu.Unlock()
	if r == nil || ctx == nil {
		return fmt.Errorf("%w: %s", ErrUnknownRepo, name)
	}
	s.Logf("%s: restarting", name)
	r.cancel()
	<-r.done
	s.sync(ctx)
	return nil
}

// RepoSlots reports a running controller's slot holders.
func (s *Supervisor) RepoSlots(name string) ([]api.TaskID, int, bool) {
	s.mu.Lock()
	r := s.repos[name]
	s.mu.Unlock()
	if r == nil {
		return nil, 0, false
	}
	r.mu.Lock()
	a := r.app
	r.mu.Unlock()
	if a == nil {
		return nil, 0, false
	}
	held, par := a.Controller.SlotInfo()
	return held, par, true
}

func (s *Supervisor) stopAll() {
	s.mu.Lock()
	rs := make([]*runner, 0, len(s.repos))
	for _, r := range s.repos {
		rs = append(rs, r)
	}
	s.mu.Unlock()
	for _, r := range rs {
		r.cancel()
	}
	for _, r := range rs {
		<-r.done
	}
}

const (
	minBackoff = time.Second
	maxBackoff = 5 * time.Minute
	healthy    = 5 * time.Minute // a run this long resets the backoff
)

// run keeps one repository's controller alive. Each start reopens the
// repository from disk, so a restart after a panic begins from durable
// state and runs crash recovery like a fresh `spine controller`.
func (s *Supervisor) run(ctx context.Context, r *runner) {
	defer close(r.done)
	backoff := minBackoff
	for ctx.Err() == nil {
		started := time.Now()
		err := s.runOnce(ctx, r)
		if ctx.Err() != nil {
			r.set(func(st *RepoStatus) { st.State, st.Error = StateStopped, "" })
			return
		}
		if time.Since(started) > healthy {
			backoff = minBackoff
		}
		if err == nil {
			err = errors.New("controller exited unexpectedly")
		}
		s.Logf("%s: %v (retrying in %s)", r.repo.Name, firstLine(err.Error()), backoff)
		r.set(func(st *RepoStatus) {
			st.State, st.Error, st.Since = StateBroken, err.Error(), time.Now()
			st.Restarts++
			st.NextRetry = time.Now().Add(backoff)
		})
		select {
		case <-ctx.Done():
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
	r.set(func(st *RepoStatus) { st.State = StateStopped })
}

func (s *Supervisor) runOnce(ctx context.Context, r *runner) (err error) {
	defer func() {
		if p := recover(); p != nil { // a panic while opening, outside the controller's own guards
			err = fmt.Errorf("panic: %v", p)
		}
	}()
	opts := app.Options{}
	if s.AppOptions != nil {
		opts = s.AppOptions(r.repo)
	}
	opts.Repo = r.repo.Name
	opts.Limits = s.limits
	opts.ResolveDep = s.resolveDep
	if opts.Logf == nil {
		name := r.repo.Name
		opts.Logf = func(f string, a ...any) { s.Logf(name+": "+f, a...) }
	}
	a, err := app.Open(ctx, r.repo.Path, opts)
	if errors.Is(err, storage.ErrLocked) {
		return fmt.Errorf("another spine controller holds this repository (stop it, or unregister the repository): %w", err)
	}
	if err != nil {
		return err
	}
	defer a.Close()
	r.mu.Lock()
	r.app = a
	r.status.State, r.status.Error, r.status.Since, r.status.NextRetry = StateRunning, "", time.Now(), time.Time{}
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.app = nil
		r.mu.Unlock()
	}()
	s.Logf("%s: controller running (%s)", r.repo.Name, r.repo.Path)
	return a.Controller.Run(ctx)
}

func (r *runner) set(fn func(*RepoStatus)) {
	r.mu.Lock()
	fn(&r.status)
	r.mu.Unlock()
}

// Status returns every repository's state, sorted by name.
func (s *Supervisor) Status() []RepoStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]RepoStatus, 0, len(s.repos))
	for _, r := range s.repos {
		r.mu.Lock()
		out = append(out, r.status)
		r.mu.Unlock()
	}
	sortStatus(out)
	return out
}

func sortStatus(xs []RepoStatus) {
	for i := 1; i < len(xs); i++ {
		for j := i; j > 0 && xs[j].Name < xs[j-1].Name; j-- {
			xs[j], xs[j-1] = xs[j-1], xs[j]
		}
	}
}

// resolveDep answers a "repo:task" dependency from another repository's
// database. Running repositories are read through their open store; others
// with a read-only open, so a dependency on a broken repository still
// resolves from durable state.
func (s *Supervisor) resolveDep(ctx context.Context, repo string, id api.TaskID) (api.Phase, error) {
	s.mu.Lock()
	r := s.repos[repo]
	s.mu.Unlock()
	if r == nil {
		return "", fmt.Errorf("%w: %s", storage.ErrNotFound, "no registered repository "+repo)
	}
	r.mu.Lock()
	a := r.app
	r.mu.Unlock()
	if a != nil {
		t, err := a.Store.GetTask(ctx, id)
		return t.Status.Phase, err
	}
	db, _, err := app.DBPath(r.repo.Path)
	if err != nil {
		return "", err
	}
	st, err := storage.OpenReadOnly(ctx, db)
	if err != nil {
		return "", err
	}
	defer st.Close()
	t, err := st.GetTask(ctx, id)
	return t.Status.Phase, err
}

// lockHome takes the per-user supervisor lock.
func lockHome(dir string) (func(), error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "supervisor.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, ErrAlreadyRunning
	}
	return func() { f.Close() }, nil
}

func firstLine(s string) string {
	for i, c := range s {
		if c == '\n' {
			return s[:i]
		}
	}
	return s
}
