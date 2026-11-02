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

func TestUI(t *testing.T) {
	ctx := context.Background()
	db := filepath.Join(t.TempDir(), "s.db")
	w, err := storage.Open(ctx, db, storage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	w.CreateTask(ctx, "t1", api.Spec{Intent: "do <b>x</b>", BaseRef: "refs/heads/main"})
	ro, err := storage.OpenReadOnly(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	in := t.TempDir()
	srv := httptest.NewServer((&Server{Store: ro, InboxDir: in, Running: func() bool { return true }}).Handler())
	defer srv.Close()

	r, _ := http.Get(srv.URL + "/")
	if r.StatusCode != 200 || !strings.Contains(r.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("index %d", r.StatusCode)
	}
	var list struct {
		Tasks      []taskRow `json:"tasks"`
		Controller bool      `json:"controller"`
	}
	r, _ = http.Get(srv.URL + "/api/tasks")
	json.NewDecoder(r.Body).Decode(&list)
	if len(list.Tasks) != 1 || list.Tasks[0].Phase != api.PhasePending || !list.Controller {
		t.Fatalf("%+v", list)
	}
	if r, _ := http.Get(srv.URL + "/api/tasks/nope"); r.StatusCode != 404 {
		t.Fatalf("unknown task: %d", r.StatusCode)
	}
	r, _ = http.Get(srv.URL + "/api/tasks/t1/events")
	var evs []storage.Event
	json.NewDecoder(r.Body).Decode(&evs)
	if len(evs) != 1 || evs[0].Kind != "submitted" {
		t.Fatalf("events %+v", evs)
	}

	// Commands need the same-origin header and go to the inbox.
	if r, _ := http.Post(srv.URL+"/api/tasks/t1/cancel", "", nil); r.StatusCode != 403 {
		t.Fatalf("cross-site POST accepted: %d", r.StatusCode)
	}
	req, _ := http.NewRequest("POST", srv.URL+"/api/tasks/t1/cancel", nil)
	req.Header.Set("X-Spine", "1")
	if r, _ := http.DefaultClient.Do(req); r.StatusCode != 200 {
		t.Fatalf("cancel %d", r.StatusCode)
	}
	es, _ := inbox.List(in)
	if len(es) != 1 || es[0].Kind != inbox.Cancel || es[0].Task != "t1" {
		t.Fatalf("inbox %+v", es)
	}

	// SSE pushes the list, and again after a change.
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
				w.CreateTask(ctx, "t2", api.Spec{Intent: "y", BaseRef: "refs/heads/main"})
			} else {
				if !strings.Contains(sc.Text(), `"t2"`) {
					t.Fatalf("second push lacks t2: %s", sc.Text())
				}
				break
			}
		}
	}
	if got != 2 {
		t.Fatalf("pushes %d", got)
	}
}
