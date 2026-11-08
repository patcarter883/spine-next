package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/patcarter883/spine-next/internal/agent"
	"github.com/patcarter883/spine-next/internal/auth"
	"github.com/patcarter883/spine-next/internal/config"
)

// piEnv serves one repository with PATH holding only fake tools.
func piEnv(t *testing.T, tools map[string]string) (*httptest.Server, string, string) {
	t.Helper()
	bin, agentDir, path := t.TempDir(), t.TempDir(), t.TempDir()
	for name, body := range tools {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	t.Setenv(agent.PiAgentDirEnv, agentDir)
	a := newRepo(t)
	src := SourceFunc(func(context.Context) []Repo {
		return []Repo{{Name: "alpha", Path: path, State: "running", Store: a.ro, InboxDir: a.inbox}}
	})
	srv := httptest.NewServer((&Server{Source: src}).Handler())
	t.Cleanup(srv.Close)
	return srv, path, agentDir
}

func piCall(t *testing.T, srv *httptest.Server, method, url string, body any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, srv.URL+url, bytes.NewReader(b))
	req.Header.Set("X-Spine", "1")
	req.Header.Set("Content-Type", "application/json")
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	out := map[string]any{}
	json.NewDecoder(r.Body).Decode(&out)
	return r.StatusCode, out
}

func waitPiJob(t *testing.T, srv *httptest.Server) map[string]any {
	t.Helper()
	for end := time.Now().Add(20 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if _, j := piCall(t, srv, "GET", "/api/pi/install", nil); j["running"] == false {
			return j
		}
	}
	t.Fatal("the installation did not finish")
	return nil
}

func TestPiInstallNpm(t *testing.T) {
	// A fake npm that records its arguments and "installs" pi next to itself.
	srv, _, _ := piEnv(t, map[string]string{
		"node": `echo v24.1.0`,
		"npm": `echo "$@" > "$(dirname "$0")/npm.args"
echo added 1 package
printf '#!/bin/sh\necho ` + agent.PiVersion + `\n' > "$(dirname "$0")/pi"; chmod +x "$(dirname "$0")/pi"`,
	})
	if c, s := piCall(t, srv, "GET", "/api/repos/alpha/pi", nil); c != 200 || s["host"].(map[string]any)["version"] != nil {
		t.Fatalf("before: %d %v", c, s)
	}
	if c, j := piCall(t, srv, "POST", "/api/repos/alpha/pi/install", map[string]string{"method": "npm"}); c != 200 || j["target"] != agent.PiPackage+"@"+agent.PiVersion {
		t.Fatalf("install: %d %v", c, j)
	}
	j := waitPiJob(t, srv)
	if j["ok"] != true || !strings.Contains(j["log"].(string), "added 1 package") {
		t.Fatalf("job: %v", j)
	}
	_, s := piCall(t, srv, "GET", "/api/repos/alpha/pi", nil)
	if s["host"].(map[string]any)["version"] != agent.PiVersion {
		t.Fatalf("after: %v", s["host"])
	}
	args, _ := os.ReadFile(filepath.Join(filepath.Dir(s["host"].(map[string]any)["path"].(string)), "npm.args"))
	if want := "install -g --ignore-scripts " + agent.PiPackage + "@" + agent.PiVersion; strings.TrimSpace(string(args)) != want {
		t.Fatalf("npm args %q, want %q", args, want)
	}
	if c, _ := piCall(t, srv, "POST", "/api/repos/alpha/pi/install", map[string]string{"method": "curl|sh"}); c != 400 {
		t.Fatalf("unknown method: %d", c)
	}
}

func TestPiInstallRefusesOldNodeAndReportsPath(t *testing.T) {
	srv, _, _ := piEnv(t, map[string]string{"node": `echo v20.11.0`, "npm": `echo ok`})
	c, j := piCall(t, srv, "POST", "/api/repos/alpha/pi/install", map[string]string{"method": "npm"})
	if c != 422 || !strings.Contains(j["error"].(string), agent.PiMinNode) {
		t.Fatalf("old node: %d %v", c, j)
	}
	// npm succeeds but puts pi somewhere the dashboard doesn't look.
	srv, _, _ = piEnv(t, map[string]string{"node": `echo v24.1.0`, "npm": `[ "$1" = prefix ] && echo /opt/global; exit 0`})
	if c, j := piCall(t, srv, "POST", "/api/repos/alpha/pi/install", map[string]string{"method": "npm"}); c != 200 {
		t.Fatalf("install: %d %v", c, j)
	}
	if j := waitPiJob(t, srv); j["ok"] != false || !strings.Contains(j["error"].(string), "/opt/global/bin is not on the dashboard's PATH") {
		t.Fatalf("job: %v", j)
	}
}

func TestPiBuildImage(t *testing.T) {
	// A fake docker: `build` checks it was given the Dockerfile; `run` is the
	// pinned-version check.
	srv, _, _ := piEnv(t, map[string]string{"docker": `case "$1" in
build) echo "$@" > "$(dirname "$0")/build.args"; for last; do :; done; grep -q 'PI_VERSION' "$last/Dockerfile" || exit 3;;
run) echo ` + agent.PiVersion + `;;
image) exit 1;;
esac`})
	if c, j := piCall(t, srv, "POST", "/api/repos/alpha/pi/install", map[string]string{"method": "image"}); c != 200 || j["target"] != "spine-pi:"+agent.PiVersion {
		t.Fatalf("install: %d %v", c, j)
	}
	if j := waitPiJob(t, srv); j["ok"] != true {
		t.Fatalf("job: %v", j)
	}
}

func TestPiModelsKeepOtherFieldsAndHideKeys(t *testing.T) {
	srv, _, dir := piEnv(t, nil)
	const secret = "sk-very-secret"
	path := filepath.Join(dir, "models.json")
	os.WriteFile(path, []byte(`{"$schema":"x","providers":{"cloud":{"baseUrl":"https://c/v1","api":"openai-responses","apiKey":"`+secret+`","models":[{"id":"big"}]},
	 "local":{"baseUrl":"http://old/v1","api":"openai-completions","apiKey":"`+secret+`","compat":{"supportsStore":false},
	  "models":[{"id":"keep","contextWindow":1000,"cost":{"input":1}},{"id":"drop"}]}}}`), 0o644)

	c, j := piCall(t, srv, "PUT", "/api/repos/alpha/pi/models", map[string]any{"name": "local", "base_url": "http://127.0.0.1:8000/v1/", "api": "openai-completions",
		"models": []map[string]any{{"id": "keep", "context_window": 2000}, {"id": "new", "max_tokens": 512}}})
	if c != 200 {
		t.Fatalf("save: %d %v", c, j)
	}
	b, _ := os.ReadFile(path)
	var doc struct {
		Schema    string `json:"$schema"`
		Providers map[string]struct {
			BaseURL string `json:"baseUrl"`
			APIKey  string `json:"apiKey"`
			Compat  map[string]any
			Models  []map[string]any
		}
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	l := doc.Providers["local"]
	if doc.Schema != "x" || doc.Providers["cloud"].APIKey != secret || l.APIKey != secret || l.BaseURL != "http://127.0.0.1:8000/v1" || l.Compat["supportsStore"] != false {
		t.Fatalf("fields lost: %s", b)
	}
	if len(l.Models) != 2 || l.Models[0]["contextWindow"] != float64(2000) || l.Models[0]["cost"] == nil || l.Models[1]["maxTokens"] != float64(512) {
		t.Fatalf("models: %s", b)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}

	r, _ := http.Get(srv.URL + "/api/repos/alpha/pi")
	var buf bytes.Buffer
	buf.ReadFrom(r.Body)
	if strings.Contains(buf.String(), secret) {
		t.Fatalf("status leaks the key: %s", buf.String())
	}

	// A web form never plants a shell command, and a broken file is left alone.
	if c, _ := piCall(t, srv, "PUT", "/api/repos/alpha/pi/models", map[string]any{"name": "x", "base_url": "http://h/v1", "api": "openai-completions", "api_key": "!curl evil", "models": []map[string]any{{"id": "m"}}}); c != 422 {
		t.Fatalf("command key: %d", c)
	}
	if c, _ := piCall(t, srv, "PUT", "/api/repos/alpha/pi/models", map[string]any{"name": "a/b", "base_url": "http://h/v1", "api": "openai-completions", "models": []map[string]any{{"id": "m"}}}); c != 422 {
		t.Fatalf("slash in provider: %d", c)
	}
	if c, _ := piCall(t, srv, "DELETE", "/api/repos/alpha/pi/models/cloud", nil); c != 200 {
		t.Fatalf("delete: %d", c)
	}
	os.WriteFile(path, []byte("{broken"), 0o600)
	if c, _ := piCall(t, srv, "PUT", "/api/repos/alpha/pi/models", map[string]any{"name": "x", "base_url": "http://h/v1", "api": "openai-completions", "models": []map[string]any{{"id": "m"}}}); c != 422 {
		t.Fatalf("broken file: %d", c)
	}
	if b, _ := os.ReadFile(path); string(b) != "{broken" {
		t.Fatalf("broken file overwritten: %s", b)
	}
}

func TestPiAdapterSaveAndWarnings(t *testing.T) {
	srv, path, dir := piEnv(t, nil)
	c, j := piCall(t, srv, "PUT", "/api/repos/alpha/agents/pi/pi", map[string]any{"version": "1.5.0", "model": "local/m", "make_default": true, "container": map[string]any{"image": "", "network": "host"}})
	if c != 200 || j["changed"] != true {
		t.Fatalf("save: %d %v", c, j)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if a := cfg.Adapters["pi"]; cfg.Agent != "pi" || a.Container == nil || a.Container.Image != agent.PiImage("1.5.0") {
		t.Fatalf("config: %+v", a)
	}
	_, s := piCall(t, srv, "GET", "/api/repos/alpha/pi", nil)
	if w, _ := s["warnings"].([]any); len(w) != 1 || !strings.Contains(w[0].(string), "not in models.json") {
		t.Fatalf("warnings: %v", s["warnings"])
	}
	os.WriteFile(filepath.Join(dir, "models.json"), []byte(`{"providers":{"local":{"apiKey":"$MY_KEY","models":[{"id":"m"}]}}}`), 0o600)
	_, s = piCall(t, srv, "GET", "/api/repos/alpha/pi", nil)
	if w, _ := s["warnings"].([]any); len(w) != 1 || !strings.Contains(w[0].(string), "$MY_KEY") {
		t.Fatalf("env warning: %v", s["warnings"])
	}
	for _, bad := range []map[string]any{
		{"version": "latest"},
		{"version": "1.5.0; rm -rf /"},
		{"version": "1.5.0", "container": map[string]any{"image": "--privileged"}},
		{"version": "1.5.0", "container": map[string]any{"image": "x", "runtime": "sh"}},
		{"version": "1.5.0", "tool_timeout": "soon"},
	} {
		if c, _ := piCall(t, srv, "PUT", "/api/repos/alpha/agents/pi/pi", bad); c != 422 {
			t.Errorf("%v: %d", bad, c)
		}
	}
}

func TestPiDiscover(t *testing.T) {
	srv, _, _ := piEnv(t, nil)
	var auth string
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		if r.URL.Path != "/v1/models" {
			w.WriteHeader(404)
			return
		}
		w.Write([]byte(`{"data":[{"id":"a"},{"id":"b"}]}`))
	}))
	defer model.Close()
	_, j := piCall(t, srv, "POST", "/api/repos/alpha/pi/discover", map[string]string{"base_url": model.URL + "/v1/", "api_key": "k"})
	if j["ok"] != true || len(j["models"].([]any)) != 2 || auth != "Bearer k" {
		t.Fatalf("%v (auth %q)", j, auth)
	}
	_, j = piCall(t, srv, "POST", "/api/repos/alpha/pi/discover", map[string]string{"base_url": model.URL + "/nope", "api_key": "$K"})
	if j["ok"] != false || auth != "" || !strings.Contains(j["error"].(string), "404") {
		t.Fatalf("%v (auth %q)", j, auth)
	}
	if c, _ := piCall(t, srv, "POST", "/api/repos/alpha/pi/discover", map[string]string{"base_url": "file:///etc/passwd"}); c != 422 {
		t.Fatalf("file url: %d", c)
	}
}

func TestPiRoles(t *testing.T) {
	a := newRepo(t)
	path := t.TempDir()
	users := auth.Store{Path: filepath.Join(t.TempDir(), "users.yaml")}
	viewer, _ := users.Add("vera", auth.Viewer)
	op, _ := users.Add("otto", auth.Operator)
	src := SourceFunc(func(context.Context) []Repo {
		return []Repo{{Name: "alpha", Path: path, State: "running", Store: a.ro, InboxDir: a.inbox}}
	})
	srv := httptest.NewServer((&Server{Source: src, Auth: &auth.Authenticator{Store: users}}).Handler())
	defer srv.Close()
	call := func(method, url, tok string) int {
		req, _ := http.NewRequest(method, srv.URL+url, strings.NewReader(`{"method":"npm"}`))
		req.Header.Set("X-Spine", "1")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+tok)
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		return r.StatusCode
	}
	if c := call("GET", "/api/repos/alpha/pi", viewer); c != 200 {
		t.Fatalf("viewer status: %d", c)
	}
	for _, x := range [][2]string{{"POST", "/api/repos/alpha/pi/install"}, {"PUT", "/api/repos/alpha/agents/pi/pi"}, {"PUT", "/api/repos/alpha/pi/models"},
		{"DELETE", "/api/repos/alpha/pi/models/x"}, {"POST", "/api/repos/alpha/pi/discover"}, {"POST", "/api/repos/alpha/pi/check"}, {"GET", "/api/pi/install"}} {
		if c := call(x[0], x[1], op); c != 403 {
			t.Errorf("operator %s %s: %d", x[0], x[1], c)
		}
	}
}

func TestConfigListsSupervisorProviders(t *testing.T) {
	srv, _, dir := piEnv(t, nil)
	const secret = "sk-very-secret"
	os.WriteFile(filepath.Join(dir, "models.json"), []byte(`{"providers":{"local":{"baseUrl":"http://h/v1","api":"openai-completions","apiKey":"`+secret+`","models":[{"id":"m"}]},
	 "gw":{"baseUrl":"http://g","api":"anthropic-messages","models":[{"id":"x"}]}}}`), 0o600)
	r, _ := http.Get(srv.URL + "/api/repos/alpha/config")
	var buf bytes.Buffer
	buf.ReadFrom(r.Body)
	if strings.Contains(buf.String(), secret) {
		t.Fatalf("config leaks the key: %s", buf.String())
	}
	var c struct {
		Providers []piProvider `json:"supervisor_providers"`
	}
	json.Unmarshal(buf.Bytes(), &c)
	if len(c.Providers) != 1 || c.Providers[0].Name != "local" || c.Providers[0].Models[0].ID != "m" {
		t.Fatalf("%+v", c.Providers)
	}
	local, model := "local", "m"
	if code, j := piCall(t, srv, "PUT", "/api/repos/alpha/config", map[string]any{"settings": config.Settings{SupervisorProvider: &local, SupervisorModel: &model}}); code != 200 || j["changed"] != true {
		t.Fatalf("%d %v", code, j)
	}
}
