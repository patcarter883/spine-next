package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/patcarter883/spine-next/internal/app"
	"github.com/patcarter883/spine-next/internal/auth"
	"github.com/patcarter883/spine-next/internal/inbox"
	"github.com/patcarter883/spine-next/internal/notify"
	"github.com/patcarter883/spine-next/internal/storage"
	"github.com/patcarter883/spine-next/internal/supervisor"
	"github.com/patcarter883/spine-next/internal/ui"
	"github.com/patcarter883/spine-next/internal/worktree"
)

// dashboardFlags are shared by `spine ui` and `spine supervisor`.
type dashboardFlags struct {
	readOnly bool
	tlsCert  string
	tlsKey   string
}

func (d *dashboardFlags) register(fs *flag.FlagSet) {
	fs.BoolVar(&d.readOnly, "read-only", false, "reject every change (approve, create, settings…), whoever asks")
	fs.StringVar(&d.tlsCert, "tls-cert", "", "serve HTTPS with this certificate (with --tls-key)")
	fs.StringVar(&d.tlsKey, "tls-key", "", "TLS private key")
}

func loopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// repoSource serves the dashboard of `spine ui` from one repository. The
// controller is the database's only writer, so a repository it has not run in
// yet has no database and the dashboard must not create one. Until the real
// database appears, an empty private placeholder stands in for it, so every
// route works and shows no tasks.
type repoSource struct {
	name, path, db, inboxDir, commonDir string

	mu             sync.Mutex // handlers call Repos concurrently
	real           *storage.Store
	placeholder    *storage.Store // kept until close: in-flight requests may hold it
	placeholderDir string
	closed         bool
}

// newRepoSource builds the source for repo and the function releasing it. It
// fails if repo is not a git repository or its database cannot be read.
func newRepoSource(ctx context.Context, repo string) (ui.Source, func() error, error) {
	db, cd, err := app.DBPath(repo)
	if err != nil {
		return nil, nil, err
	}
	primary, _ := worktree.PrimaryOf(ctx, repo)
	s := &repoSource{name: filepath.Base(primary), path: primary, db: db, inboxDir: inbox.Dir(cd), commonDir: cd}
	s.real, err = storage.OpenReadOnly(ctx, db)
	if os.IsNotExist(err) {
		s.placeholder, s.placeholderDir, err = openPlaceholder(ctx)
	}
	if err != nil {
		return nil, nil, err
	}
	return s, s.close, nil
}

// openPlaceholder creates an empty, migrated database in a private temporary
// directory and opens it read-only, like the real one.
func openPlaceholder(ctx context.Context) (st *storage.Store, dir string, err error) {
	if dir, err = os.MkdirTemp("", "spine-ui-"); err != nil {
		return nil, "", err
	}
	defer func() {
		if err != nil {
			os.RemoveAll(dir)
		}
	}()
	path := filepath.Join(dir, "placeholder.db")
	w, err := storage.Open(ctx, path, storage.Options{})
	if err != nil {
		return nil, "", err
	}
	if err = w.Close(); err != nil {
		return nil, "", err
	}
	if st, err = storage.OpenReadOnly(ctx, path); err != nil {
		return nil, "", err
	}
	return st, dir, nil
}

// Repos implements ui.Source.
func (s *repoSource) Repos(ctx context.Context) []ui.Repo {
	st, problem := s.store(ctx)
	state := "stopped"
	if s.writerHoldsLock() {
		state = "running"
	}
	return []ui.Repo{{Name: s.name, Path: s.path, State: state, Error: problem, Store: st, InboxDir: s.inboxDir, CommonDir: s.commonDir}}
}

// store returns the store to serve, switching to the real database for good
// once it can be opened, and any problem opening it.
func (s *repoSource) store(ctx context.Context) (*storage.Store, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.real != nil {
		return s.real, ""
	}
	if s.closed {
		return s.placeholder, ""
	}
	st, err := storage.OpenReadOnly(ctx, s.db)
	switch {
	case err == nil:
		s.real = st
		return st, ""
	case os.IsNotExist(err):
		return s.placeholder, ""
	default:
		return s.placeholder, err.Error()
	}
}

// writerHoldsLock is storage.IsLocked without its side effect: IsLocked
// creates the lock file if the directory exists, which an inbox command can
// have made before any controller ran. No lock file means no holder.
func (s *repoSource) writerHoldsLock() bool {
	if _, err := os.Stat(s.db + ".flock"); err != nil {
		return false
	}
	return storage.IsLocked(s.db)
}

// close releases both stores and removes the placeholder's directory.
func (s *repoSource) close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	var errs []error
	if s.real != nil {
		errs = append(errs, s.real.Close())
	}
	if s.placeholder != nil {
		errs = append(errs, s.placeholder.Close(), os.RemoveAll(s.placeholderDir))
	}
	return errors.Join(errs...)
}

// newDashboard builds the server with users, notifications and the audit
// log under the per-user home, and checks the address is safe to serve.
func newDashboard(src ui.Source, multi bool, admin ui.Admin, addr string, d dashboardFlags) (*ui.Server, error) {
	home := supervisor.Home()
	a := &auth.Authenticator{Store: auth.Store{Path: filepath.Join(home, "users.yaml")}, Secure: d.tlsCert != ""}
	if !loopback(addr) && !a.Enabled() {
		return nil, fmt.Errorf("%s is not a loopback address: add a user first (`spine users add NAME --role viewer|operator|admin`), so every request is authenticated", addr)
	}
	if (d.tlsCert == "") != (d.tlsKey == "") {
		return nil, errors.New("--tls-cert and --tls-key go together")
	}
	if !loopback(addr) && d.tlsCert == "" {
		logf("warning: serving %s over plain HTTP; tokens and session cookies can be read on the network (use --tls-cert/--tls-key or a TLS proxy)", addr)
	}
	return &ui.Server{Source: src, Multi: multi, Admin: admin, Auth: a, ReadOnly: d.readOnly, Home: home,
		Notifier: &notify.Notifier{Logf: logf}, Logf: logf}, nil
}

// serveDashboard runs srv until ctx ends.
func serveDashboard(ctx context.Context, s *ui.Server, addr string, d dashboardFlags, extra http.Handler) error {
	h := s.Handler()
	if extra != nil {
		h = extra
	}
	srv := &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 10 * time.Second}
	go s.Start(ctx)
	go func() { <-ctx.Done(); srv.Close() }()
	scheme := "http"
	if d.tlsCert != "" {
		scheme = "https"
	}
	mode := ""
	if d.readOnly {
		mode = " (read-only)"
	}
	if s.Auth.Enabled() {
		mode += " (sign-in required)"
	}
	fmt.Printf("spine dashboard on %s://%s%s\n", scheme, addr, mode)
	var err error
	if d.tlsCert != "" {
		err = srv.ListenAndServeTLS(d.tlsCert, d.tlsKey)
	} else {
		err = srv.ListenAndServe()
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
