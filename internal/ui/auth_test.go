package ui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/patcarter883/spine-next/internal/auth"
	"github.com/patcarter883/spine-next/internal/inbox"
)

func TestAuthRolesReadOnlyAudit(t *testing.T) {
	a := newRepo(t, "t1", "t2")
	home := t.TempDir()
	users := auth.Store{Path: filepath.Join(home, "users.yaml")}
	viewer, _ := users.Add("vera", auth.Viewer)
	op, _ := users.Add("otto", auth.Operator)
	admin, _ := users.Add("ada", auth.Admin)
	src := SourceFunc(func(context.Context) []Repo {
		return []Repo{{Name: "alpha", Path: t.TempDir(), State: "running", Store: a.ro, InboxDir: a.inbox}}
	})
	s := &Server{Source: src, Auth: &auth.Authenticator{Store: users}, Home: home}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	call := func(method, path, tok, body string) int {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.Header.Set("X-Spine", "1")
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		return r.StatusCode
	}
	// The page and /api/me are public; data is not.
	for path, want := range map[string]int{"/": 200, "/static/app.js": 200, "/api/me": 200, "/api/overview": 401, "/api/repos/alpha/tasks/t1": 401} {
		if c := call("GET", path, "", ""); c != want {
			t.Errorf("anonymous %s: %d, want %d", path, c, want)
		}
	}
	if c := call("GET", "/api/overview", "spn_wrong", ""); c != 401 {
		t.Fatalf("bad token: %d", c)
	}
	// Roles.
	if c := call("GET", "/api/overview", viewer, ""); c != 200 {
		t.Fatalf("viewer read %d", c)
	}
	if c := call("POST", "/api/repos/alpha/tasks/t1/cancel", viewer, ""); c != 403 {
		t.Fatalf("viewer cancel %d", c)
	}
	if c := call("POST", "/api/repos/alpha/tasks/t1/cancel", op, ""); c != 200 {
		t.Fatalf("operator cancel %d", c)
	}
	if c := call("PUT", "/api/repos/alpha/config", op, `{"raw":"parallel: 3\n"}`); c != 403 {
		t.Fatalf("operator config %d", c)
	}
	if c := call("PUT", "/api/repos/alpha/config", admin, `{"raw":"parallel: 3\n"}`); c != 200 {
		t.Fatalf("admin config %d", c)
	}
	es, _ := inbox.List(a.inbox)
	if len(es) != 1 || es[0].By != "otto" {
		t.Fatalf("command not recorded as the user: %+v", es)
	}
	// Browser sign-in: cookie session.
	jar, _ := cookiejar.New(nil)
	cl := &http.Client{Jar: jar}
	req, _ := http.NewRequest("POST", srv.URL+"/api/login", strings.NewReader(`{"token":"`+op+`"}`))
	req.Header.Set("X-Spine", "1")
	req.Header.Set("Content-Type", "application/json")
	r, err := cl.Do(req)
	if err != nil || r.StatusCode != 200 {
		t.Fatalf("login %v %d", err, r.StatusCode)
	}
	r, _ = cl.Get(srv.URL + "/api/me")
	var me struct {
		User Identity
	}
	json.NewDecoder(r.Body).Decode(&me)
	if me.User.Name != "otto" || me.User.Role != auth.Operator {
		t.Fatalf("me %+v", me)
	}
	// Removing a user ends their sessions.
	users.Remove("otto")
	if r, _ := cl.Get(srv.URL + "/api/overview"); r.StatusCode != 401 {
		t.Fatalf("removed user still signed in: %d", r.StatusCode)
	}
	// Audit: who did what.
	b, _ := os.ReadFile(filepath.Join(home, "audit.log"))
	log := string(b)
	for _, want := range []string{`"user":"otto"`, `"task":"t1"`, `"detail":"cancel `, `"user":"vera"`, `"status":403`, `"user":"ada"`, `"detail":"login"`} {
		if !strings.Contains(log, want) {
			t.Errorf("audit lacks %s:\n%s", want, log)
		}
	}

	// Read-only: even an admin cannot change anything.
	s2 := &Server{Source: src, Auth: &auth.Authenticator{Store: users}, ReadOnly: true}
	srv2 := httptest.NewServer(s2.Handler())
	defer srv2.Close()
	req, _ = http.NewRequest("POST", srv2.URL+"/api/repos/alpha/tasks/t2/cancel", nil)
	req.Header.Set("X-Spine", "1")
	req.Header.Set("Authorization", "Bearer "+admin)
	if r, _ := http.DefaultClient.Do(req); r.StatusCode != 403 {
		t.Fatalf("read-only accepted a change: %d", r.StatusCode)
	}
	req, _ = http.NewRequest("GET", srv2.URL+"/api/overview", nil)
	req.Header.Set("Authorization", "Bearer "+viewer)
	if r, _ := http.DefaultClient.Do(req); r.StatusCode != 200 {
		t.Fatalf("read-only read %d", r.StatusCode)
	}
}

func TestDefaultsListPiAdapter(t *testing.T) {
	a := newRepo(t)
	src := SourceFunc(func(context.Context) []Repo {
		return []Repo{{Name: "alpha", Path: t.TempDir(), State: "running", Store: a.ro, InboxDir: a.inbox}}
	})
	srv := httptest.NewServer((&Server{Source: src}).Handler())
	defer srv.Close()
	r, _ := http.Get(srv.URL + "/api/repos/alpha/defaults")
	var d struct {
		Agents   []string
		Adapters []adapterInfo
	}
	json.NewDecoder(r.Body).Decode(&d)
	var pi *adapterInfo
	for i := range d.Adapters {
		if d.Adapters[i].Name == "pi" {
			pi = &d.Adapters[i]
		}
	}
	if pi == nil || pi.Protocol != "pi-rpc" || !strings.Contains(pi.Label, "Pi 1.5.0") || pi.Warning == "" {
		t.Fatalf("pi adapter: %+v", d)
	}
}
