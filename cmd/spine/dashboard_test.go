package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/patcarter883/spine-next/internal/api"
	"github.com/patcarter883/spine-next/internal/storage"
	"github.com/patcarter883/spine-next/internal/testrepo"
	"github.com/patcarter883/spine-next/internal/ui"
	"github.com/patcarter883/spine-next/internal/worktree"
)

type overviewBody struct {
	Repos []struct {
		Name  string `json:"name"`
		State string `json:"state"`
		Error string `json:"error"`
	} `json:"repos"`
	Tasks []struct {
		ID string `json:"id"`
	} `json:"tasks"`
}

// dashboardOn builds the source for a repository and serves it.
func dashboardOn(t *testing.T, dir string) (*httptest.Server, *repoSource, func() error) {
	t.Helper()
	src, closeSrc, err := newRepoSource(context.Background(), dir)
	if err != nil {
		t.Fatalf("newRepoSource: %v", err)
	}
	t.Cleanup(func() { closeSrc() })
	srv := httptest.NewServer((&ui.Server{Source: src, Home: t.TempDir()}).Handler())
	t.Cleanup(srv.Close)
	return srv, src.(*repoSource), closeSrc
}

func getOverview(t *testing.T, srv *httptest.Server) overviewBody {
	t.Helper()
	r, err := http.Get(srv.URL + "/api/overview")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		t.Fatalf("GET /api/overview: %d", r.StatusCode)
	}
	var o overviewBody
	if err := json.NewDecoder(r.Body).Decode(&o); err != nil {
		t.Fatal(err)
	}
	if len(o.Repos) != 1 {
		t.Fatalf("repos = %+v, want one", o.Repos)
	}
	return o
}

func noDatabaseFiles(t *testing.T, db string) {
	t.Helper()
	for _, suffix := range []string{"", "-wal", "-shm", ".flock"} {
		if _, err := os.Stat(db + suffix); !os.IsNotExist(err) {
			t.Errorf("%s: the dashboard must not create it (stat: %v)", db+suffix, err)
		}
	}
}

func commonDir(t *testing.T, r *testrepo.Repo) string {
	t.Helper()
	cd, err := worktree.ResolveCommonDir(r.Dir)
	if err != nil {
		t.Fatal(err)
	}
	return cd
}

func testSpec() api.Spec {
	return api.Spec{Title: "first", Intent: "x", BaseRef: "refs/heads/main"}
}

func TestDashboardOnFreshRepository(t *testing.T) {
	ctx := context.Background()
	repo := testrepo.New(t)
	db := worktree.DatabasePath(commonDir(t, repo))
	srv, src, closeSrc := dashboardOn(t, repo.Dir)

	o := getOverview(t, srv)
	name := o.Repos[0].Name
	if name != filepath.Base(repo.Dir) || o.Repos[0].State != "stopped" || o.Repos[0].Error != "" || len(o.Tasks) != 0 {
		t.Fatalf("overview on a fresh repository = %+v", o)
	}

	r, err := http.Get(srv.URL + "/api/repos/" + name + "/pi")
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != 200 {
		t.Fatalf("GET pi settings: %d, want 200", r.StatusCode)
	}

	noDatabaseFiles(t, db)

	// A controller creates the database: its tasks appear with no restart.
	w, err := storage.Open(ctx, db, storage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if _, err := w.CreateTask(ctx, "t1", testSpec()); err != nil {
		t.Fatal(err)
	}
	o = getOverview(t, srv)
	if len(o.Tasks) != 1 || o.Tasks[0].ID != "t1" || o.Repos[0].State != "running" || o.Repos[0].Error != "" {
		t.Fatalf("overview with a controller = %+v", o)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if o = getOverview(t, srv); len(o.Tasks) != 1 || o.Repos[0].State != "stopped" {
		t.Fatalf("overview after the controller stopped = %+v", o)
	}

	dir := src.placeholderDir
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("placeholder directory before close: %v", err)
	}
	if err := closeSrc(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("placeholder directory after close: %v", err)
	}
	if err := closeSrc(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

// An inbox command can make the spine directory before any controller runs;
// reporting the state must not then leave a lock file in it.
func TestDashboardCreatesNothingInExistingSpineDir(t *testing.T) {
	repo := testrepo.New(t)
	db := worktree.DatabasePath(commonDir(t, repo))
	if err := os.MkdirAll(filepath.Dir(db), 0o755); err != nil {
		t.Fatal(err)
	}
	srv, _, _ := dashboardOn(t, repo.Dir)
	if o := getOverview(t, srv); o.Repos[0].State != "stopped" {
		t.Fatalf("state = %q", o.Repos[0].State)
	}
	noDatabaseFiles(t, db)
}

// The database file exists before the controller has migrated it. The source
// may switch to it at that moment, and must then pick up the schema and tasks.
func TestDashboardSwitchesToDatabaseBeingCreated(t *testing.T) {
	ctx := context.Background()
	repo := testrepo.New(t)
	db := worktree.DatabasePath(commonDir(t, repo))
	srv, _, _ := dashboardOn(t, repo.Dir)
	getOverview(t, srv)

	if err := os.MkdirAll(filepath.Dir(db), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(db, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	getOverview(t, srv) // not yet migrated: only that it answers matters

	w, err := storage.Open(ctx, db, storage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if _, err := w.CreateTask(ctx, "t1", testSpec()); err != nil {
		t.Fatal(err)
	}
	if o := getOverview(t, srv); len(o.Tasks) != 1 || o.Repos[0].Error != "" {
		t.Fatalf("overview after migration = %+v", o)
	}
}

func TestRepoSourceWithExistingDatabase(t *testing.T) {
	ctx := context.Background()
	repo := testrepo.New(t)
	db := worktree.DatabasePath(commonDir(t, repo))
	w, err := storage.Open(ctx, db, storage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.CreateTask(ctx, "t1", testSpec()); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	srv, src, _ := dashboardOn(t, repo.Dir)
	if src.placeholder != nil {
		t.Fatal("a placeholder was created although the database exists")
	}
	if o := getOverview(t, srv); len(o.Tasks) != 1 || o.Repos[0].State != "stopped" {
		t.Fatalf("overview = %+v", o)
	}
}

// A database that exists but cannot be read fails at startup; if it appears
// later, the placeholder stays and the problem is reported on the repository.
func TestRepoSourceUnreadableDatabase(t *testing.T) {
	ctx := context.Background()
	repo := testrepo.New(t)
	db := worktree.DatabasePath(commonDir(t, repo))
	srv, _, _ := dashboardOn(t, repo.Dir)
	getOverview(t, srv)

	if err := os.MkdirAll(filepath.Dir(db), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(db, []byte("this is not a sqlite database, and it is long enough to show it"), 0o644); err != nil {
		t.Fatal(err)
	}
	if o := getOverview(t, srv); o.Repos[0].Error == "" || len(o.Tasks) != 0 {
		t.Fatalf("overview with a corrupt database = %+v", o)
	}
	if _, _, err := newRepoSource(ctx, repo.Dir); err == nil {
		t.Fatal("a corrupt database at startup must be an error")
	}
	if err := os.Remove(db); err != nil {
		t.Fatal(err)
	}
	if o := getOverview(t, srv); o.Repos[0].Error != "" {
		t.Fatalf("the error should clear once the problem is gone: %+v", o)
	}
}

func TestRepoSourceNotAGitRepository(t *testing.T) {
	if _, _, err := newRepoSource(context.Background(), t.TempDir()); err == nil {
		t.Fatal("want an error outside a git repository")
	}
}
