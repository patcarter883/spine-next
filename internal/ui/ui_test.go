package ui

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/patcarter883/spine-next/internal/api"
	"github.com/patcarter883/spine-next/internal/inbox"
	"github.com/patcarter883/spine-next/internal/storage"
)

type repoFixture struct {
	w, ro *storage.Store
	inbox string
}

func newRepo(t *testing.T, tasks ...api.TaskID) repoFixture {
	ctx := context.Background()
	db := filepath.Join(t.TempDir(), "s.db")
	w, err := storage.Open(ctx, db, storage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	for _, id := range tasks {
		w.CreateTask(ctx, id, api.Spec{Intent: "do <b>" + string(id) + "</b>", BaseRef: "refs/heads/main"})
	}
	ro, err := storage.OpenReadOnly(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ro.Close() })
	return repoFixture{w: w, ro: ro, inbox: t.TempDir()}
}

func TestMultiRepoUI(t *testing.T) {
	ctx := context.Background()
	a := newRepo(t, "t1")
	b := newRepo(t, "t1", "t2") // same task id in two repositories
	src := SourceFunc(func(context.Context) []Repo {
		return []Repo{
			{Name: "alpha", State: "running", Store: a.ro, InboxDir: a.inbox},
			{Name: "beta", State: "broken", Error: "boom", Restarts: 2, Store: b.ro, InboxDir: b.inbox},
		}
	})
	srv := httptest.NewServer((&Server{Source: src, Multi: true}).Handler())
	defer srv.Close()

	if r, _ := http.Get(srv.URL + "/"); r.StatusCode != 200 || !strings.Contains(r.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("index %d", r.StatusCode)
	}
	var ov overview
	r, _ := http.Get(srv.URL + "/api/overview")
	json.NewDecoder(r.Body).Decode(&ov)
	if !ov.Multi || len(ov.Repos) != 2 || len(ov.Tasks) != 3 || ov.Repos[1].Error != "boom" {
		t.Fatalf("%+v", ov)
	}
	// Task routes are per repository: beta/t2 exists, alpha/t2 does not.
	if r, _ := http.Get(srv.URL + "/api/repos/beta/tasks/t2"); r.StatusCode != 200 {
		t.Fatalf("beta/t2 %d", r.StatusCode)
	}
	if r, _ := http.Get(srv.URL + "/api/repos/alpha/tasks/t2"); r.StatusCode != 404 {
		t.Fatalf("alpha/t2 %d", r.StatusCode)
	}
	if r, _ := http.Get(srv.URL + "/api/repos/nope/tasks/t1"); r.StatusCode != 404 {
		t.Fatalf("unknown repo %d", r.StatusCode)
	}
	r, _ = http.Get(srv.URL + "/api/repos/alpha/tasks/t1/events")
	var evs []storage.Event
	json.NewDecoder(r.Body).Decode(&evs)
	if len(evs) != 1 || evs[0].Kind != "submitted" {
		t.Fatalf("events %+v", evs)
	}

	// Commands need the same-origin header and land in that repository's inbox.
	if r, _ := http.Post(srv.URL+"/api/repos/beta/tasks/t1/cancel", "", nil); r.StatusCode != 403 {
		t.Fatalf("cross-site POST accepted: %d", r.StatusCode)
	}
	req, _ := http.NewRequest("POST", srv.URL+"/api/repos/beta/tasks/t1/cancel", nil)
	req.Header.Set("X-Spine", "1")
	if r, _ := http.DefaultClient.Do(req); r.StatusCode != 200 {
		t.Fatalf("cancel %d", r.StatusCode)
	}
	if es, _ := inbox.List(b.inbox); len(es) != 1 || es[0].Kind != inbox.Cancel || es[0].Task != "t1" {
		t.Fatalf("beta inbox %+v", es)
	}
	if es, _ := inbox.List(a.inbox); len(es) != 0 {
		t.Fatalf("command leaked into alpha's inbox: %+v", es)
	}

	// SSE pushes the overview, and again after a change in either repository.
	sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, _ = http.NewRequestWithContext(sctx, "GET", srv.URL+"/api/stream", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	got := 0
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), "data: ") {
			got++
			if got == 1 {
				a.w.CreateTask(ctx, "t9", api.Spec{Intent: "y", BaseRef: "refs/heads/main"})
			} else {
				if !strings.Contains(sc.Text(), `"t9"`) {
					t.Fatalf("second push lacks t9: %s", sc.Text())
				}
				break
			}
		}
	}
	if got != 2 {
		t.Fatalf("pushes %d", got)
	}
}
