package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	osexec "os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	piimage "github.com/patcarter883/spine-next/contrib/pi"
	"github.com/patcarter883/spine-next/internal/agent"
	"github.com/patcarter883/spine-next/internal/config"
	"github.com/patcarter883/spine-next/internal/exec"
)

// Install and configure the Pi agent. The commands are fixed: a request only
// picks the method, and the version and image come from the adapter's
// configuration.

var (
	piVersionRe  = regexp.MustCompile(`^v?\d+\.\d+\.\d+([-+][0-9A-Za-z.-]+)?$`)
	piImageRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@-]*$`)
	piNetworkRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)
	piProviderRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	piEnvRefRe   = regexp.MustCompile(`^\$\{?([A-Za-z_][A-Za-z0-9_]*)\}?$`)
)

const (
	piModelsFile   = "models.json"
	piNoKey        = "unused" // Pi needs a key for a model to be available; local servers ignore it
	piLogCap       = 64 << 10
	piJobTimeout   = 30 * time.Minute
	piQuickTimeout = 10 * time.Second
)

var piAPIs = []string{"openai-completions", "openai-responses", "anthropic-messages", "google-generative-ai"}

func piAdapter(cfg config.Config) agent.Adapter { return cfg.PiAdapter() }

func piImageOf(a agent.Adapter) (image, runtime string) {
	if a.Container != nil {
		return a.Container.Image, a.Container.RuntimeName()
	}
	return agent.PiImage(a.Version), "docker"
}

// quick runs a short status command; stderr comes back in the error.
func quick(ctx context.Context, env []string, argv ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, piQuickTimeout)
	defer cancel()
	res, err := exec.Run(ctx, exec.Cmd{Argv: argv, Env: env})
	if err != nil {
		return "", err
	}
	if !res.OK() {
		return "", errors.New(strings.TrimSpace(string(res.Stderr)))
	}
	return strings.TrimSpace(string(res.Stdout)), nil
}

// versionAtLeast compares dotted versions numerically ("v22.19.0").
func versionAtLeast(have, want string) bool {
	h, w := strings.Split(strings.TrimPrefix(have, "v"), "."), strings.Split(want, ".")
	for i := range w {
		if i >= len(h) {
			return false
		}
		a, _ := strconv.Atoi(h[i])
		b, _ := strconv.Atoi(w[i])
		if a != b {
			return a > b
		}
	}
	return true
}

// - Status -

type piKey struct {
	Kind string `json:"kind"` // none, unused, env, literal, command
	Env  string `json:"env,omitempty"`
}

type piModel struct {
	ID            string `json:"id"`
	ContextWindow int64  `json:"context_window,omitempty"`
	MaxTokens     int64  `json:"max_tokens,omitempty"`
}

type piProvider struct {
	Name    string    `json:"name"`
	BaseURL string    `json:"base_url"`
	API     string    `json:"api"`
	Key     piKey     `json:"key"` // never the key itself
	Models  []piModel `json:"models"`
}

func classifyKey(v string) piKey {
	switch {
	case v == "":
		return piKey{Kind: "none"}
	case v == piNoKey:
		return piKey{Kind: "unused"}
	case strings.HasPrefix(v, "!"):
		return piKey{Kind: "command"}
	}
	if m := piEnvRefRe.FindStringSubmatch(v); m != nil {
		return piKey{Kind: "env", Env: m[1]}
	}
	return piKey{Kind: "literal"}
}

// loadPiModels reads models.json keeping every field; absent is empty.
func loadPiModels(path string) (map[string]any, bool, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]any{}, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	doc := map[string]any{}
	if err := dec.Decode(&doc); err != nil {
		return nil, true, fmt.Errorf("%s: %w", path, err)
	}
	return doc, true, nil
}

func piProviders(doc map[string]any) []piProvider {
	ps, _ := doc["providers"].(map[string]any)
	out := []piProvider{}
	for name, v := range ps {
		m, _ := v.(map[string]any)
		p := piProvider{Name: name, Models: []piModel{}}
		p.BaseURL, _ = m["baseUrl"].(string)
		p.API, _ = m["api"].(string)
		key, _ := m["apiKey"].(string)
		p.Key = classifyKey(key)
		ms, _ := m["models"].([]any)
		for _, x := range ms {
			e, _ := x.(map[string]any)
			id, _ := e["id"].(string)
			mo := piModel{ID: id}
			if n, ok := e["contextWindow"].(json.Number); ok {
				mo.ContextWindow, _ = n.Int64()
			}
			if n, ok := e["maxTokens"].(json.Number); ok {
				mo.MaxTokens, _ = n.Int64()
			}
			p.Models = append(p.Models, mo)
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// piWarnings explains configurations that would fail only when a task runs.
func piWarnings(a agent.Adapter, provs []piProvider) []string {
	var out []string
	for _, p := range provs {
		if p.Key.Kind != "env" || p.Name != agent.PiProviderName(a) {
			continue
		}
		if _, ok := a.Env[p.Key.Env]; ok {
			continue
		}
		if _, ok := os.LookupEnv(p.Key.Env); !ok {
			out = append(out, fmt.Sprintf("provider %s reads $%s, which is not set where Spine runs: set it in the supervisor's environment, or in the adapter's env in %s", p.Name, p.Key.Env, config.Path))
		}
	}
	if a.Model == "" {
		return append(out, "no model selected: set the adapter's model")
	}
	prov, id, scoped := strings.Cut(a.Model, "/")
	for _, p := range provs {
		if scoped && p.Name != prov {
			continue
		}
		if slices.ContainsFunc(p.Models, func(m piModel) bool { return m.ID == id || (!scoped && m.ID == a.Model) }) {
			return out
		}
	}
	if !scoped {
		return append(out, fmt.Sprintf("model %s is not in %s; use provider/id", a.Model, piModelsFile))
	}
	return append(out, fmt.Sprintf("model %s is not in %s (fine only for a provider built into Pi)", a.Model, piModelsFile))
}

func (s *Server) piStatus(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.repo(r)
	if !ok {
		httpError(w, http.StatusNotFound, "unknown repository")
		return
	}
	cfg, err := config.Load(repo.Path)
	if err != nil {
		httpError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	a := piAdapter(cfg)
	ctx := r.Context()
	adapter := map[string]any{"name": a.Name, "version": a.Version, "model": a.Model, "max_turns": a.MaxTurns, "default": cfg.Agent == a.Name, "default_agent": cfg.Agent}
	if a.ToolTimeout > 0 {
		adapter["tool_timeout"] = a.ToolTimeout.String()
	}
	if c := a.Container; c != nil {
		adapter["container"] = map[string]any{"image": c.Image, "runtime": c.Runtime, "network": c.Network, "args": c.Args}
	}

	host := map[string]any{"command": a.Argv[0]}
	if p, err := osexec.LookPath(a.Argv[0]); err == nil {
		host["path"] = p
		// With the agent dir set, so a status check doesn't create ~/.pi.
		env := append(exec.MinimalEnv(), agent.PiAgentDirEnv+"="+agent.PiAgentDir(a))
		if v, err := quick(ctx, env, a.Argv[0], "--version"); err != nil {
			host["error"] = err.Error()
		} else {
			host["version"] = strings.TrimPrefix(v, "v")
		}
	}
	host["npm"] = lookup("npm")
	if v, err := quick(ctx, nil, "node", "--version"); err == nil {
		host["node"] = v
		host["node_ok"] = versionAtLeast(v, agent.PiMinNode)
	}

	image, rt := piImageOf(a)
	cont := map[string]any{"runtime": rt, "image": image, "runtime_found": lookup(rt)}
	if lookup(rt) {
		_, err := quick(ctx, agent.CLIEnv(), rt, "image", "inspect", "--format", "{{.Id}}", image)
		cont["image_found"] = err == nil
		if err != nil && !imageMissing(err.Error()) {
			cont["error"] = err.Error()
		}
	}

	dir := agent.PiAgentDir(a)
	path := filepath.Join(dir, piModelsFile)
	out := map[string]any{"pinned": agent.PiVersion, "min_node": agent.PiMinNode, "package": agent.PiPackage, "apis": piAPIs,
		"adapter": adapter, "host": host, "container": cont, "restart": s.Admin != nil}
	models := map[string]any{"path": path}
	provs := []piProvider{}
	if doc, exists, err := loadPiModels(path); err != nil {
		models["error"] = err.Error()
	} else {
		models["exists"] = exists
		provs = piProviders(doc)
	}
	models["providers"] = provs
	out["models"] = models
	if w := piWarnings(a, provs); len(w) > 0 {
		out["warnings"] = w
	}
	writeJSON(w, out)
}

func lookup(cmd string) bool {
	_, err := osexec.LookPath(cmd)
	return err == nil
}

// imageMissing recognizes docker's and podman's "no such image".
func imageMissing(msg string) bool {
	msg = strings.ToLower(msg)
	return strings.Contains(msg, "no such") || strings.Contains(msg, "failed to find") || strings.Contains(msg, "image not known")
}

// - Adapter -

func (s *Server) putPiAdapter(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.repo(r)
	if !ok {
		httpError(w, http.StatusNotFound, "unknown repository")
		return
	}
	var req config.PiSettings
	if !decode(w, r, &req) {
		return
	}
	req.Version = strings.TrimSpace(req.Version)
	req.Model = strings.TrimSpace(req.Model)
	req.ToolTimeout = strings.TrimSpace(req.ToolTimeout)
	bad := func(msg string) { httpError(w, http.StatusUnprocessableEntity, msg) }
	if !piVersionRe.MatchString(req.Version) {
		bad("version must look like 1.5.0: Pi ships breaking changes in minor releases, so it is pinned exactly")
		return
	}
	if strings.HasPrefix(req.Model, "-") {
		bad("the model must be provider/id")
		return
	}
	if req.MaxTurns < 0 {
		bad("max turns must not be negative")
		return
	}
	if c := req.Container; c != nil {
		if c.Image = strings.TrimSpace(c.Image); c.Image == "" {
			c.Image = agent.PiImage(req.Version)
		}
		if !piImageRe.MatchString(c.Image) {
			bad("the image name is not valid")
			return
		}
		if c.Runtime != "" && c.Runtime != "docker" && c.Runtime != "podman" {
			bad("the container runtime must be docker or podman")
			return
		}
		if c.Network != "" && !piNetworkRe.MatchString(c.Network) {
			bad("the network name is not valid")
			return
		}
	}
	req.AgentDir = strings.TrimSpace(req.AgentDir)
	if req.AgentDir != "" && !filepath.IsAbs(req.AgentDir) {
		bad("the Pi config directory must be an absolute path")
		return
	}
	cur, _ := os.ReadFile(filepath.Join(repo.Path, config.Path))
	next, err := config.ApplyPiAdapter(cur, r.PathValue("name"), req)
	if err != nil {
		bad(err.Error())
		return
	}
	s.writeConfig(w, r, repo, cur, next)
}

// - Model server (models.json) -

func (s *Server) piModelsPath(w http.ResponseWriter, r *http.Request) (string, bool) {
	repo, ok := s.repo(r)
	if !ok {
		httpError(w, http.StatusNotFound, "unknown repository")
		return "", false
	}
	cfg, err := config.Load(repo.Path)
	if err != nil {
		httpError(w, http.StatusUnprocessableEntity, err.Error())
		return "", false
	}
	dir := agent.PiAgentDir(piAdapter(cfg))
	if dir == "" {
		httpError(w, 500, "no home directory: set "+agent.PiAgentDirEnv)
		return "", false
	}
	return filepath.Join(dir, piModelsFile), true
}

func validBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("the server URL must be http(s)://host…")
	}
	return nil
}

// putPiProvider adds or changes one provider, keeping every other provider
// and every field the form doesn't show (compat, headers, a model's cost).
func (s *Server) putPiProvider(w http.ResponseWriter, r *http.Request) {
	path, ok := s.piModelsPath(w, r)
	if !ok {
		return
	}
	var req struct {
		Name    string    `json:"name"`
		BaseURL string    `json:"base_url"`
		API     string    `json:"api"`
		APIKey  string    `json:"api_key"` // empty keeps the current key
		Models  []piModel `json:"models"`
	}
	if !decode(w, r, &req) {
		return
	}
	bad := func(msg string) { httpError(w, http.StatusUnprocessableEntity, msg) }
	req.Name, req.BaseURL = strings.TrimSpace(req.Name), strings.TrimRight(strings.TrimSpace(req.BaseURL), "/")
	req.APIKey = strings.TrimSpace(req.APIKey)
	if !piProviderRe.MatchString(req.Name) {
		bad("the provider name may use letters, digits, dot, dash and underscore")
		return
	}
	if err := validBaseURL(req.BaseURL); err != nil {
		bad(err.Error())
		return
	}
	if !slices.Contains(piAPIs, req.API) {
		bad("the API must be one of " + strings.Join(piAPIs, ", "))
		return
	}
	// Pi runs a leading ! as a shell command at request time.
	if strings.HasPrefix(req.APIKey, "!") {
		bad("a !command key can't be set here: it runs in a shell. Edit " + path + " by hand if you need one")
		return
	}
	if len(req.Models) == 0 {
		bad("add at least one model")
		return
	}
	doc, _, err := loadPiModels(path)
	if err != nil {
		bad(err.Error() + " (fix the file first; it was not changed)")
		return
	}
	ps, _ := doc["providers"].(map[string]any)
	if ps == nil {
		ps = map[string]any{}
		doc["providers"] = ps
	}
	prov, _ := ps[req.Name].(map[string]any)
	if prov == nil {
		prov = map[string]any{}
		ps[req.Name] = prov
	}
	prov["baseUrl"], prov["api"] = req.BaseURL, req.API
	if req.APIKey != "" {
		prov["apiKey"] = req.APIKey
	} else if k, _ := prov["apiKey"].(string); k == "" {
		prov["apiKey"] = piNoKey
	}
	old := map[string]map[string]any{}
	if ms, ok := prov["models"].([]any); ok {
		for _, x := range ms {
			if e, ok := x.(map[string]any); ok {
				id, _ := e["id"].(string)
				old[id] = e
			}
		}
	}
	models := []any{}
	seen := map[string]bool{}
	for _, m := range req.Models {
		m.ID = strings.TrimSpace(m.ID)
		if m.ID == "" || seen[m.ID] || m.ContextWindow < 0 || m.MaxTokens < 0 {
			bad(fmt.Sprintf("model %q: ids must be set and unique, and sizes must not be negative", m.ID))
			return
		}
		seen[m.ID] = true
		e := old[m.ID]
		if e == nil {
			e = map[string]any{"id": m.ID}
		}
		for k, v := range map[string]int64{"contextWindow": m.ContextWindow, "maxTokens": m.MaxTokens} {
			if v > 0 {
				e[k] = v
			} else {
				delete(e, k) // Pi's defaults apply
			}
		}
		models = append(models, e)
	}
	prov["models"] = models
	note(r, r.PathValue("repo"), "", "pi: save provider "+req.Name)
	if !writePiModels(w, path, doc) {
		return
	}
	writeJSON(w, map[string]any{"saved": true, "path": path})
}

func (s *Server) deletePiProvider(w http.ResponseWriter, r *http.Request) {
	path, ok := s.piModelsPath(w, r)
	if !ok {
		return
	}
	doc, _, err := loadPiModels(path)
	if err != nil {
		httpError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	ps, _ := doc["providers"].(map[string]any)
	name := r.PathValue("provider")
	if _, ok := ps[name]; !ok {
		httpError(w, http.StatusNotFound, "unknown provider")
		return
	}
	delete(ps, name)
	note(r, r.PathValue("repo"), "", "pi: remove provider "+name)
	if !writePiModels(w, path, doc) {
		return
	}
	writeJSON(w, map[string]any{"saved": true, "path": path})
}

// writePiModels saves models.json privately (it may hold keys), with a
// backup of the previous content.
func writePiModels(w http.ResponseWriter, path string, doc map[string]any) bool {
	b, err := json.MarshalIndent(doc, "", "  ")
	if err == nil {
		err = os.MkdirAll(filepath.Dir(path), 0o700)
	}
	if err == nil {
		if cur, rerr := os.ReadFile(path); rerr == nil {
			err = config.WriteFileAtomic(path+".bak", cur, 0o600)
		}
	}
	if err == nil {
		err = config.WriteFileAtomic(path, append(b, '\n'), 0o600)
	}
	if err != nil {
		httpError(w, 500, err.Error())
		return false
	}
	return true
}

// piDiscover asks a model server for its models: a reachability check from
// where the agent runs (the browser can't reach it under the page's CSP).
func (s *Server) piDiscover(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BaseURL string `json:"base_url"`
		APIKey  string `json:"api_key"`
	}
	if !decode(w, r, &req) {
		return
	}
	base := strings.TrimRight(strings.TrimSpace(req.BaseURL), "/")
	if err := validBaseURL(base); err != nil {
		httpError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	note(r, r.PathValue("repo"), "", "pi: query model server "+base)
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	hreq, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	if err != nil {
		httpError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if k := strings.TrimSpace(req.APIKey); k != "" && k[0] != '$' && k[0] != '!' {
		hreq.Header.Set("Authorization", "Bearer "+k)
	}
	cl := http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := cl.Do(hreq)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": "could not reach " + base + "/models: " + err.Error()})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		writeJSON(w, map[string]any{"ok": false, "error": fmt.Sprintf("%s/models answered HTTP %d", base, resp.StatusCode)})
		return
	}
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&list); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": base + "/models did not return an OpenAI-style model list"})
		return
	}
	ids := []string{}
	for _, m := range list.Data {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	writeJSON(w, map[string]any{"ok": true, "models": ids})
}

// piCheck runs spine doctor's Pi probe on the adapter, whether or not it is
// the default agent. No prompt is sent.
func (s *Server) piCheck(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.repo(r)
	if !ok {
		httpError(w, http.StatusNotFound, "unknown repository")
		return
	}
	cfg, err := config.Load(repo.Path)
	if err != nil {
		httpError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	dir, err := os.MkdirTemp("", "spine-probe-")
	if err != nil {
		httpError(w, 500, err.Error())
		return
	}
	defer os.RemoveAll(dir)
	a := piAdapter(cfg)
	note(r, repo.Name, "", "pi: check adapter "+a.Name)
	msg, err := (&agent.Runner{}).ProbePi(r.Context(), a, dir)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "message": msg, "contained": a.Container != nil})
}

// - Installation -

// piJob is one installation, kept until the next one starts.
type piJob struct {
	Method   string    `json:"method"` // npm or image
	Target   string    `json:"target"`
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished"`
	Running  bool      `json:"running"`
	OK       bool      `json:"ok"`
	Error    string    `json:"error,omitempty"`
	Log      string    `json:"log"`
}

type piInstaller struct {
	mu  sync.Mutex
	job *piJob
}

func (p *piInstaller) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.job.Log += string(b)
	if n := len(p.job.Log); n > piLogCap {
		p.job.Log = p.job.Log[n-piLogCap:]
	}
	return len(b), nil
}

func (p *piInstaller) snapshot() any {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.job == nil {
		return map[string]any{"none": true}
	}
	return *p.job
}

func (p *piInstaller) start(j *piJob) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.job != nil && p.job.Running {
		return false
	}
	p.job = j
	return true
}

func (p *piInstaller) finish(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.job.Running, p.job.Finished, p.job.OK = false, time.Now().UTC(), err == nil
	if err != nil {
		p.job.Error = err.Error()
	}
}

func (s *Server) piInstallJob(w http.ResponseWriter, r *http.Request) { writeJSON(w, s.pi.snapshot()) }

func (s *Server) piInstall(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.repo(r)
	if !ok {
		httpError(w, http.StatusNotFound, "unknown repository")
		return
	}
	var req struct {
		Method string `json:"method"`
	}
	if !decode(w, r, &req) {
		return
	}
	cfg, err := config.Load(repo.Path)
	if err != nil {
		httpError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	a := piAdapter(cfg)
	version := strings.TrimPrefix(a.Version, "v")
	if !piVersionRe.MatchString(version) {
		httpError(w, http.StatusUnprocessableEntity, fmt.Sprintf("the adapter pins %q, which is not a version", a.Version))
		return
	}
	var job *piJob
	var run func(context.Context) error
	switch req.Method {
	case "npm":
		if !lookup("npm") {
			httpError(w, http.StatusUnprocessableEntity, "npm is not on the dashboard's PATH")
			return
		}
		if v, err := quick(r.Context(), nil, "node", "--version"); err == nil && !versionAtLeast(v, agent.PiMinNode) {
			httpError(w, http.StatusUnprocessableEntity, fmt.Sprintf("Pi needs Node %s or newer; this machine has %s", agent.PiMinNode, v))
			return
		}
		spec := agent.PiPackage + "@" + version
		job = &piJob{Method: "npm", Target: spec}
		run = func(ctx context.Context) error { return s.installPiNpm(ctx, a, spec) }
	case "image":
		image, rt := piImageOf(a)
		if !lookup(rt) {
			httpError(w, http.StatusUnprocessableEntity, rt+" is not on the dashboard's PATH")
			return
		}
		if !piImageRe.MatchString(image) {
			httpError(w, http.StatusUnprocessableEntity, fmt.Sprintf("the image name %q is not valid", image))
			return
		}
		job = &piJob{Method: "image", Target: image}
		run = func(ctx context.Context) error { return s.buildPiImage(ctx, a, rt, image, version) }
	default:
		httpError(w, http.StatusBadRequest, "method must be npm or image")
		return
	}
	job.Running, job.Started = true, time.Now().UTC()
	if !s.pi.start(job) {
		httpError(w, http.StatusConflict, "an installation is already running")
		return
	}
	note(r, repo.Name, "", "pi: install "+job.Target)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), piJobTimeout)
		defer cancel()
		s.pi.finish(run(ctx))
	}()
	writeJSON(w, s.pi.snapshot())
}

// piStep runs one installation command, streaming into the job's log.
func (s *Server) piStep(ctx context.Context, env []string, dir string, argv ...string) error {
	fmt.Fprintf(&s.pi, "$ %s\n", strings.Join(argv, " "))
	res, err := exec.Run(ctx, exec.Cmd{Argv: argv, Env: env, Dir: dir, Stdout: &s.pi, Stderr: &s.pi})
	if err != nil {
		return err
	}
	if !res.OK() {
		return fmt.Errorf("%s failed (exit %d)", argv[0], res.ExitCode)
	}
	return nil
}

// piInstallEnv adds what package managers and builds need to the minimal
// environment.
func piInstallEnv(base []string) []string {
	for _, k := range []string{"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "https_proxy", "http_proxy", "no_proxy", "SSL_CERT_FILE", "NODE_EXTRA_CA_CERTS", "NPM_CONFIG_PREFIX", "npm_config_prefix"} {
		if v, ok := os.LookupEnv(k); ok {
			base = append(base, k+"="+v)
		}
	}
	return base
}

func (s *Server) installPiNpm(ctx context.Context, a agent.Adapter, spec string) error {
	env := piInstallEnv(exec.MinimalEnv())
	// --ignore-scripts as in contrib/pi/Dockerfile: Pi needs no install scripts.
	if err := s.piStep(ctx, env, "", "npm", "install", "-g", "--ignore-scripts", spec); err != nil {
		return err
	}
	a.Container = nil
	if !lookup(a.Argv[0]) {
		bin, err := quick(ctx, env, "npm", "prefix", "-g")
		if err != nil {
			return fmt.Errorf("installed, but %s is not on the dashboard's PATH", a.Argv[0])
		}
		return fmt.Errorf("installed, but %s/bin is not on the dashboard's PATH: add it where the dashboard or supervisor starts, then restart it", bin)
	}
	if err := (&agent.Runner{}).CheckPiVersion(ctx, a); err != nil {
		return err
	}
	fmt.Fprintf(&s.pi, "pi %s is installed\n", a.Version)
	return nil
}

func (s *Server) buildPiImage(ctx context.Context, a agent.Adapter, rt, image, version string) error {
	dir, err := os.MkdirTemp("", "spine-pi-image-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), piimage.Dockerfile, 0o600); err != nil {
		return err
	}
	if err := s.piStep(ctx, piInstallEnv(agent.CLIEnv()), "", rt, "build", "-t", image, "--build-arg", "PI_VERSION="+version, dir); err != nil {
		return err
	}
	a.Container = &agent.Container{Runtime: rt, Image: image}
	if err := (&agent.Runner{}).CheckPiVersion(ctx, a); err != nil {
		return err
	}
	fmt.Fprintf(&s.pi, "%s runs pi %s\n", image, a.Version)
	return nil
}
