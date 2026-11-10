// Package ui serves the Spine dashboard: `spine ui` for one repository and
// `spine supervisor` for every registered repository. It reads each
// repository's database read-only (it never takes a writer lock) and sends
// task and repository commands through that repository's inbox, the same
// path as the CLI, so each repository's controller stays its database's only
// writer. Updates are pushed with SSE.
//
// Access: without users the dashboard is for its local user only (it
// refuses non-loopback addresses unless read-only). With users (spine
// users add), every request is authenticated and checked against the user's
// role, and every change is written to an audit log under the user's name.
package ui

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	osuser "os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/patcarter883/spine-next/internal/api"
	"github.com/patcarter883/spine-next/internal/auth"
	"github.com/patcarter883/spine-next/internal/config"
	"github.com/patcarter883/spine-next/internal/inbox"
	"github.com/patcarter883/spine-next/internal/notify"
	"github.com/patcarter883/spine-next/internal/storage"
)

//go:embed web
var webFS embed.FS

// Repo is one repository the dashboard shows.
type Repo struct {
	Name      string           `json:"name"`
	Path      string           `json:"path"`
	State     string           `json:"state"` // running, stopped, broken, starting
	Error     string           `json:"error,omitempty"`
	Restarts  int              `json:"restarts,omitempty"`
	Since     time.Time        `json:"since,omitempty"`
	Paused    *inbox.PauseInfo `json:"paused,omitempty"`
	WebURL    string           `json:"web_url,omitempty"` // commit links, when the remote is a known forge
	Store     *storage.Store   `json:"-"`                 // read-only; nil if the database cannot be opened
	InboxDir  string           `json:"-"`
	CommonDir string           `json:"-"`
}

// Source lists the repositories to show.
type Source interface {
	Repos(ctx context.Context) []Repo
}

// SourceFunc adapts a function.
type SourceFunc func(ctx context.Context) []Repo

func (f SourceFunc) Repos(ctx context.Context) []Repo { return f(ctx) }

// Admin is implemented by the supervisor: repository registration, global
// limits and controller restarts. Nil under `spine ui`.
type Admin interface {
	AddRepo(ctx context.Context, path, name string) error
	RemoveRepo(name string) error
	RestartRepo(name string) error
	SetParallel(name string, n int) error
	Slots() Slots
	SetLimits(maxAgents, maxGates int) error
	// RepoSlots reports a running controller's slot holders.
	RepoSlots(name string) (held []api.TaskID, parallel int, ok bool)
}

// Slots is the supervisor's global limiter state.
type Slots struct {
	Max     map[string]int `json:"max"`
	InUse   map[string]int `json:"in_use"`
	Peak    map[string]int `json:"peak"`
	Holders []SlotHolder   `json:"holders"`
	Waiting []SlotHolder   `json:"waiting"`
}

// SlotHolder holds or waits for a global slot.
type SlotHolder struct {
	Repo  string     `json:"repo"`
	Task  api.TaskID `json:"task"`
	Kind  string     `json:"kind"`
	Since time.Time  `json:"since"`
}

// Server serves the dashboard.
type Server struct {
	Source Source
	// Multi is true under the supervisor (shows the repository switcher).
	Multi bool
	// Admin enables supervisor operations (nil under `spine ui`).
	Admin Admin
	// Auth, when it has users, requires authentication and roles.
	Auth *auth.Authenticator
	// ReadOnly rejects every change, whoever asks.
	ReadOnly bool
	// Home holds the dashboard's own files: templates.yaml, notify.yaml,
	// audit.log. Empty = no user templates, notifications or audit log.
	Home string
	// Notifier delivers notifications (Start runs it).
	Notifier *notify.Notifier
	Logf     func(string, ...any)
	// Extra adds read-only routes ("GET /api/...") behind the same access
	// checks.
	Extra map[string]http.HandlerFunc

	once    sync.Once
	mux     *http.ServeMux
	webURLs sync.Map // repo path -> forge URL ("" = none)
	pi      piInstaller
}

// Handler returns the HTTP routes.
func (s *Server) Handler() http.Handler {
	s.once.Do(s.routes)
	return http.HandlerFunc(s.serve)
}

func (s *Server) routes() {
	m := http.NewServeMux()
	static, _ := fs.Sub(webFS, "web")
	files := http.FileServer(http.FS(static))
	m.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		b, _ := webFS.ReadFile("web/index.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(b)
	})
	m.Handle("GET /static/", http.StripPrefix("/static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	})))

	// Session.
	m.HandleFunc("GET /api/me", s.me)
	m.HandleFunc("POST /api/login", s.login)
	m.HandleFunc("POST /api/logout", s.logout)

	// Tasks.
	m.HandleFunc("GET /api/overview", s.overviewHandler)
	m.HandleFunc("GET /api/stream", s.stream)
	m.HandleFunc("GET /api/search", s.search)
	m.HandleFunc("GET /api/repos/{repo}/tasks/{id}", s.task)
	m.HandleFunc("GET /api/repos/{repo}/tasks/{id}/events", s.events)
	m.HandleFunc("GET /api/repos/{repo}/tasks/{id}/live", s.live)
	m.HandleFunc("GET /api/repos/{repo}/tasks/{id}/diff", s.diff)
	m.HandleFunc("GET /api/repos/{repo}/tasks/{id}/rounds", s.rounds)
	m.HandleFunc("GET /api/repos/{repo}/artifacts/{digest}", s.artifact)
	m.HandleFunc("POST /api/repos/{repo}/tasks/{id}/{action}", s.need(auth.Operator, s.command))
	m.HandleFunc("POST /api/repos/{repo}/tasks", s.need(auth.Operator, s.create))
	m.HandleFunc("POST /api/bulk", s.need(auth.Operator, s.bulk))

	// Planning.
	m.HandleFunc("GET /api/repos/{repo}/defaults", s.defaults)
	m.HandleFunc("GET /api/repos/{repo}/gates", s.gates)
	m.HandleFunc("GET /api/repos/{repo}/templates", s.templates)
	m.HandleFunc("PUT /api/templates", s.need(auth.Operator, s.putTemplate))
	m.HandleFunc("DELETE /api/templates/{name}", s.need(auth.Operator, s.deleteTemplate))
	m.HandleFunc("POST /api/repos/{repo}/plans", s.need(auth.Operator, s.startPlan))
	m.HandleFunc("POST /api/repos/{repo}/plans/validate", s.validatePlan)
	m.HandleFunc("POST /api/repos/{repo}/plans/link", s.linkPlan)
	m.HandleFunc("POST /api/repos/{repo}/plans/save", s.need(auth.Operator, s.savePlan))
	m.HandleFunc("POST /api/repos/{repo}/plans/submit", s.need(auth.Operator, s.submitPlan))
	m.HandleFunc("GET /api/graph", s.graph)
	m.HandleFunc("POST /api/repos/{repo}/gates/check", s.need(auth.Operator, s.gatesCheck))
	m.HandleFunc("GET /api/repos/{repo}/context", s.contextInfo)
	m.HandleFunc("POST /api/repos/{repo}/survey", s.need(auth.Operator, s.startSurvey))
	m.HandleFunc("GET /api/repos/{repo}/suggest", s.suggestScope)

	// Monitoring.
	m.HandleFunc("GET /api/stats", s.stats)
	m.HandleFunc("GET /api/slots", s.slots)
	m.HandleFunc("GET /api/repos/{repo}/health", s.health)
	m.HandleFunc("GET /api/notifications", s.notifications)
	m.HandleFunc("GET /api/notify/config", s.need(auth.Admin, s.notifyConfig))
	m.HandleFunc("PUT /api/notify/config", s.need(auth.Admin, s.putNotifyConfig))
	m.HandleFunc("POST /api/notify/test", s.need(auth.Admin, s.testNotify))
	m.HandleFunc("GET /api/audit", s.auditLog)

	// Operations.
	m.HandleFunc("POST /api/repos/{repo}/pause", s.need(auth.Operator, s.pause))
	m.HandleFunc("POST /api/repos/{repo}/resume", s.need(auth.Operator, s.resume))
	m.HandleFunc("GET /api/repos/{repo}/gc", s.gcPlan)
	m.HandleFunc("POST /api/repos/{repo}/gc", s.need(auth.Admin, s.gcRun))
	m.HandleFunc("GET /api/repos/{repo}/config", s.getConfig)
	m.HandleFunc("PUT /api/repos/{repo}/config", s.need(auth.Admin, s.putConfig))
	m.HandleFunc("POST /api/admin/repos", s.need(auth.Admin, s.addRepo))
	m.HandleFunc("DELETE /api/admin/repos/{name}", s.need(auth.Admin, s.removeRepo))
	m.HandleFunc("GET /api/admin/supervisor", s.supervisorSettings)
	m.HandleFunc("PUT /api/admin/supervisor", s.need(auth.Admin, s.putSupervisorSettings))
	m.HandleFunc("GET /api/repos/{repo}/agents", s.agents)
	m.HandleFunc("PUT /api/repos/{repo}/agents/{name}", s.need(auth.Admin, s.putAgent))
	m.HandleFunc("PUT /api/repos/{repo}/agents/{name}/pi", s.need(auth.Admin, s.putPiAdapter))
	m.HandleFunc("PUT /api/repos/{repo}/agents/{name}/acp", s.need(auth.Admin, s.putACPAdapter))
	m.HandleFunc("DELETE /api/repos/{repo}/agents/{name}", s.need(auth.Admin, s.deleteAgent))
	m.HandleFunc("GET /api/repos/{repo}/pi", s.piStatus)
	m.HandleFunc("PUT /api/repos/{repo}/pi/models", s.need(auth.Admin, s.putPiProvider))
	m.HandleFunc("DELETE /api/repos/{repo}/pi/models/{provider}", s.need(auth.Admin, s.deletePiProvider))
	m.HandleFunc("POST /api/repos/{repo}/pi/discover", s.need(auth.Admin, s.piDiscover))
	m.HandleFunc("POST /api/repos/{repo}/pi/check", s.need(auth.Admin, s.piCheck))
	m.HandleFunc("POST /api/repos/{repo}/pi/install", s.need(auth.Admin, s.piInstall))
	m.HandleFunc("GET /api/pi/install", s.need(auth.Admin, s.piInstallJob))
	for pattern, h := range s.Extra {
		m.HandleFunc(pattern, h)
	}
	s.mux = m
}

// - Identity, roles, CSRF, audit -

type ctxKey struct{}

// Identity is who is making a request.
type Identity struct {
	Name string    `json:"name"`
	Role auth.Role `json:"role"`
	// Authenticated is false without users (local, single-user mode).
	Authenticated bool `json:"authenticated"`
}

var localName = sync.OnceValue(func() string {
	if u, err := osuser.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	if n := os.Getenv("USER"); n != "" {
		return n
	}
	return "local"
})

func (s *Server) authEnabled() bool { return s.Auth != nil && s.Auth.Enabled() }

// whoami resolves the request's identity (ok=false: authentication required).
func (s *Server) whoami(r *http.Request) (Identity, bool) {
	if !s.authEnabled() {
		return Identity{Name: localName(), Role: auth.Admin}, true
	}
	u, ok := s.Auth.Check(r)
	if !ok {
		return Identity{}, false
	}
	return Identity{Name: u.Name, Role: u.Role, Authenticated: true}, true
}

func identity(r *http.Request) Identity {
	id, _ := r.Context().Value(ctxKey{}).(Identity)
	return id
}

// by is the name recorded on commands: the user, or "<os user> (local)".
func by(r *http.Request) string {
	id := identity(r)
	if !id.Authenticated {
		return id.Name + " (local)"
	}
	return id.Name
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(c int) { w.code = c; w.ResponseWriter.WriteHeader(c) }
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

type auditNote struct{ repo, task, detail string }

type auditKey struct{}

// note adds detail to the request's audit record.
func note(r *http.Request, repo, task, detail string) {
	if n, ok := r.Context().Value(auditKey{}).(*auditNote); ok {
		n.repo, n.task, n.detail = repo, task, detail
	}
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	public := !strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/api/me" || r.URL.Path == "/api/login"
	id, ok := s.whoami(r)
	if !ok && !public {
		httpError(w, http.StatusUnauthorized, "sign in required")
		return
	}
	mutating := r.Method != http.MethodGet && r.Method != http.MethodHead
	if mutating {
		// Same-origin only: the page sends this header; cross-site forms cannot.
		if r.Header.Get("X-Spine") != "1" {
			httpError(w, http.StatusForbidden, "missing X-Spine header")
			return
		}
		if s.ReadOnly && r.URL.Path != "/api/login" && r.URL.Path != "/api/logout" && !strings.HasSuffix(r.URL.Path, "/plans/validate") {
			httpError(w, http.StatusForbidden, "this dashboard is read-only")
			return
		}
	}
	ctx := context.WithValue(r.Context(), ctxKey{}, id)
	n := &auditNote{}
	ctx = context.WithValue(ctx, auditKey{}, n)
	sw := &statusWriter{ResponseWriter: w, code: 200}
	s.mux.ServeHTTP(sw, r.WithContext(ctx))
	if mutating && r.URL.Path != "/api/login" && r.URL.Path != "/api/logout" && !strings.HasSuffix(r.URL.Path, "/plans/validate") {
		s.audit(AuditEntry{At: time.Now().UTC(), User: id.Name, Role: string(id.Role), Authenticated: id.Authenticated,
			Remote: r.RemoteAddr, Method: r.Method, Path: r.URL.Path, Status: sw.code, Repo: n.repo, Task: n.task, Detail: n.detail})
	}
}

// need wraps a handler with a minimum role.
func (s *Server) need(role auth.Role, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if id := identity(r); !id.Role.AtLeast(role) {
			httpError(w, http.StatusForbidden, fmt.Sprintf("%s role required (you are %s)", role, id.Role))
			return
		}
		h(w, r)
	}
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	id, ok := s.whoami(r)
	out := map[string]any{"auth": s.authEnabled(), "read_only": s.ReadOnly, "signed_in": ok, "multi": s.Multi, "admin": s.Admin != nil,
		"notifications": s.Notifier != nil, "home": s.Home != ""}
	if ok {
		out["user"] = id
	}
	writeJSON(w, out)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !s.authEnabled() {
		httpError(w, http.StatusBadRequest, "this dashboard has no users")
		return
	}
	var req struct {
		Token string `json:"token"`
	}
	if !decode(w, r, &req) {
		return
	}
	u, ok := s.Auth.Login(w, req.Token)
	s.audit(AuditEntry{At: time.Now().UTC(), User: u.Name, Role: string(u.Role), Authenticated: ok, Remote: r.RemoteAddr,
		Method: r.Method, Path: r.URL.Path, Status: map[bool]int{true: 200, false: 401}[ok], Detail: "login"})
	if !ok {
		time.Sleep(500 * time.Millisecond) // slow down guessing
		httpError(w, http.StatusUnauthorized, "unknown token")
		return
	}
	writeJSON(w, Identity{Name: u.Name, Role: u.Role, Authenticated: true})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if s.Auth != nil {
		s.Auth.Logout(w, r)
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// - Helpers -

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// decode reads a JSON body strictly; it writes the error response itself.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		httpError(w, http.StatusUnsupportedMediaType, "send JSON")
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		httpError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return false
	}
	return true
}

func (s *Server) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
	}
}

// - Overview -

type taskRow struct {
	Repo       string     `json:"repo"`
	ID         api.TaskID `json:"id"`
	Title      string     `json:"title"`
	Kind       string     `json:"kind,omitempty"`
	Phase      api.Phase  `json:"phase"`
	Reason     string     `json:"reason"`
	Message    string     `json:"message"`
	Attempt    int        `json:"attempt"`
	Round      int        `json:"round"`
	Tokens     int64      `json:"tokens"`
	MaxTokens  int64      `json:"max_tokens,omitempty"`
	Created    time.Time  `json:"created"`
	Updated    time.Time  `json:"updated"`
	Started    time.Time  `json:"started,omitempty"`
	Commit     string     `json:"commit,omitempty"`
	NeedsYou   bool       `json:"needs_you"`
	FailedGate string     `json:"failed_gate,omitempty"`
	After      []string   `json:"after,omitempty"`
	By         string     `json:"by,omitempty"`
	Template   string     `json:"template,omitempty"`
	Hints      int        `json:"hints,omitempty"`
}

func row(repo string, t api.Task) taskRow {
	title := t.Spec.Title
	if title == "" {
		title = strings.TrimSpace(strings.SplitN(t.Spec.Intent, "\n", 2)[0])
		if len(title) > 100 {
			title = title[:100] + "…"
		}
	}
	r := taskRow{Repo: repo, ID: t.ID, Title: title, Kind: t.Spec.Kind, Phase: t.Status.Phase, Reason: t.Status.Reason, Message: t.Status.Message,
		Attempt: t.Status.Attempt, Round: t.Status.PatchRound, Tokens: t.Status.TokensUsed, MaxTokens: t.Spec.Budget.MaxTokens,
		Created: t.CreatedAt, Updated: t.UpdatedAt, Started: t.Status.StartedAt, Commit: t.Status.IntegratedCommit,
		NeedsYou: t.Status.Phase == api.PhaseEscalated, By: t.Spec.CreatedBy, Template: t.Spec.Template, Hints: len(t.Status.Hints)}
	for _, d := range t.Spec.DependsOn {
		r.After = append(r.After, string(d))
	}
	if g := t.Status.LastGate; g != nil && !g.Passed {
		r.FailedGate = g.FailedGate
	}
	return r
}

type overview struct {
	Multi bool      `json:"multi"`
	Repos []Repo    `json:"repos"`
	Tasks []taskRow `json:"tasks"`
	// Notified is the latest notification sequence (the page fetches the
	// feed when it moves).
	Notified int64 `json:"notified"`
}

// repos lists repositories with their pause state and forge link.
func (s *Server) repos(ctx context.Context) []Repo {
	rs := s.Source.Repos(ctx)
	for i := range rs {
		if rs[i].InboxDir != "" {
			if p, ok := inbox.Paused(rs[i].InboxDir); ok {
				p := p
				rs[i].Paused = &p
			}
			if rs[i].CommonDir == "" {
				rs[i].CommonDir = filepath.Dir(filepath.Dir(rs[i].InboxDir))
			}
		}
		if rs[i].WebURL == "" && rs[i].Path != "" {
			rs[i].WebURL = s.webURL(ctx, rs[i].Path)
		}
	}
	return rs
}

// overview lists every repository and all their tasks, newest first.
func (s *Server) overview(ctx context.Context) overview {
	repos := s.repos(ctx)
	o := overview{Multi: s.Multi, Repos: repos, Tasks: []taskRow{}}
	for i, r := range repos {
		if r.Store == nil {
			continue
		}
		ts, err := r.Store.ListTasks(ctx)
		if err != nil {
			if o.Repos[i].Error == "" {
				o.Repos[i].Error = "reading tasks: " + err.Error()
			}
			continue
		}
		for _, t := range ts {
			o.Tasks = append(o.Tasks, row(r.Name, t))
		}
	}
	sort.SliceStable(o.Tasks, func(i, j int) bool { return o.Tasks[i].Created.After(o.Tasks[j].Created) })
	if s.Notifier != nil {
		if f := s.Notifier.Feed(0); len(f) > 0 {
			o.Notified = f[len(f)-1].Seq
		}
	}
	return o
}

func (s *Server) overviewHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.overview(r.Context()))
}

func (s *Server) repo(r *http.Request) (Repo, bool) {
	name := r.PathValue("repo")
	for _, x := range s.repos(r.Context()) {
		if x.Name == name && x.Store != nil {
			return x, true
		}
	}
	return Repo{}, false
}

func (s *Server) getTask(w http.ResponseWriter, r *http.Request) (Repo, api.Task, bool) {
	repo, ok := s.repo(r)
	if !ok {
		httpError(w, http.StatusNotFound, "unknown repository")
		return Repo{}, api.Task{}, false
	}
	t, err := repo.Store.GetTask(r.Context(), api.TaskID(r.PathValue("id")))
	if err == storage.ErrNotFound {
		httpError(w, http.StatusNotFound, "unknown task")
		return Repo{}, api.Task{}, false
	}
	if err != nil {
		httpError(w, 500, err.Error())
		return Repo{}, api.Task{}, false
	}
	return repo, t, true
}

func (s *Server) task(w http.ResponseWriter, r *http.Request) {
	if _, t, ok := s.getTask(w, r); ok {
		writeJSON(w, t)
	}
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.repo(r)
	if !ok {
		httpError(w, http.StatusNotFound, "unknown repository")
		return
	}
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	evs, err := repo.Store.ListEvents(r.Context(), api.TaskID(r.PathValue("id")), after, 2000)
	if err != nil {
		httpError(w, 500, err.Error())
		return
	}
	if evs == nil {
		evs = []storage.Event{}
	}
	writeJSON(w, evs)
}

// stream pushes the overview whenever it changes (polled every second
// server-side; databases have no change feed across processes).
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		httpError(w, 500, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	var last string
	tk := time.NewTicker(time.Second)
	defer tk.Stop()
	for {
		b, _ := json.Marshal(s.overview(r.Context()))
		if string(b) != last {
			last = string(b)
			fmt.Fprintf(w, "event: overview\ndata: %s\n\n", b)
			fl.Flush()
		}
		select {
		case <-r.Context().Done():
			return
		case <-tk.C:
		}
	}
}

// - Task commands -

type commandRequest struct {
	Text string            `json:"text,omitempty"` // hint, retry
	Seq  int               `json:"seq,omitempty"`  // rollback
	Task *config.TaskInput `json:"task,omitempty"` // edit
}

var commandKinds = map[string]inbox.Kind{"approve": inbox.Approve, "retry": inbox.Retry, "cancel": inbox.Cancel,
	"hint": inbox.Hint, "edit": inbox.Edit, "rollback": inbox.Rollback}

func (s *Server) command(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("action") == "accept" {
		s.accept(w, r)
		return
	}
	k, ok := commandKinds[r.PathValue("action")]
	if !ok {
		httpError(w, http.StatusNotFound, "unknown action")
		return
	}
	var req commandRequest
	if r.ContentLength > 0 || strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		if !decode(w, r, &req) {
			return
		}
	}
	repo, t, ok := s.getTask(w, r)
	if !ok {
		return
	}
	cmd, err := s.buildCommand(r, repo, t, k, req)
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	note(r, repo.Name, string(t.ID), string(k)+" "+trunc(req.Text, 200))
	cid, err := inbox.Write(repo.InboxDir, cmd)
	if err != nil {
		httpError(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]any{"queued": cid, "controller": repo.State == "running"})
}

// accept sends the command an escalated task's proposal names.
func (s *Server) accept(w http.ResponseWriter, r *http.Request) {
	repo, t, ok := s.getTask(w, r)
	if !ok {
		return
	}
	cmd, err := inbox.Accept(t, by(r))
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	note(r, repo.Name, string(t.ID), "accept "+string(t.Status.Proposal.Kind)+" "+trunc(cmd.Text, 200))
	cid, err := inbox.Write(repo.InboxDir, cmd)
	if err != nil {
		httpError(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]any{"queued": cid, "controller": repo.State == "running"})
}

// buildCommand validates a task command against the task's current state,
// so the person gets an immediate error instead of a rejected inbox file.
func (s *Server) buildCommand(r *http.Request, repo Repo, t api.Task, k inbox.Kind, req commandRequest) (inbox.Command, error) {
	cmd := inbox.Command{Kind: k, Task: t.ID, By: by(r), Text: strings.TrimSpace(req.Text), Seq: req.Seq}
	ph := t.Status.Phase
	stopped := ph == api.PhaseEscalated || ph == api.PhaseFailed || ph == api.PhaseCancelled
	switch k {
	case inbox.Approve:
		if !(ph == api.PhaseEscalated && t.Status.Reason == "AwaitingApproval") && ph.Done() {
			return cmd, fmt.Errorf("%s is %s", t.ID, ph)
		}
	case inbox.Retry:
		if !stopped {
			return cmd, fmt.Errorf("only an escalated, failed or cancelled task can be retried (%s is %s)", t.ID, ph)
		}
	case inbox.Cancel:
		if ph.Done() {
			return cmd, fmt.Errorf("%s is already %s", t.ID, ph)
		}
	case inbox.Hint:
		if cmd.Text == "" {
			return cmd, fmt.Errorf("the hint is empty")
		}
		if len(cmd.Text) > 2000 {
			return cmd, fmt.Errorf("hints are limited to 2000 characters")
		}
		if ph == api.PhaseIntegrated || ph == api.PhasePlanned {
			return cmd, fmt.Errorf("%s is %s", t.ID, ph)
		}
	case inbox.Rollback:
		if !stopped {
			return cmd, fmt.Errorf("stop the task first: only an escalated, failed or cancelled task can be rolled back")
		}
		found := false
		for _, sn := range t.Status.Snapshots {
			found = found || sn.Seq == req.Seq
		}
		if !found {
			return cmd, fmt.Errorf("%s has no snapshot #%d", t.ID, req.Seq)
		}
	case inbox.Edit:
		if ph != api.PhasePending && !stopped {
			return cmd, fmt.Errorf("%s is %s: only a pending or stopped task can be edited", t.ID, ph)
		}
		if req.Task == nil {
			return cmd, fmt.Errorf("edit needs the task fields")
		}
		spec, err := s.buildSpec(r.Context(), repo, *req.Task, by(r))
		if err != nil {
			return cmd, err
		}
		spec.CreatedBy, spec.Kind = t.Spec.CreatedBy, t.Spec.Kind
		cmd.Spec = &spec
	}
	return cmd, nil
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// - Creating tasks -

type createRequest struct {
	ID string `json:"id"`
	config.TaskInput
}

// defaults returns what the new-task form pre-fills for a repository.
func (s *Server) defaults(w http.ResponseWriter, r *http.Request) {
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
	writeJSON(w, map[string]any{
		"base": strings.TrimPrefix(cfg.Base, "refs/heads/"), "agent": cfg.Agent, "agents": cfg.AdapterNames(),
		"adapters": describeAdapters(cfg), "max_attempts": cfg.Budget.MaxAttempts, "max_rounds": cfg.Budget.MaxPatchRounds,
		"max_tokens": cfg.Budget.MaxTokens, "parallel": cfg.Parallel,
	})
}

// buildSpec turns form input into a spec with the repository's defaults,
// resolving gate names against the base's gates file.
func (s *Server) buildSpec(ctx context.Context, repo Repo, in config.TaskInput, who string) (api.Spec, error) {
	cfg, err := config.Load(repo.Path)
	if err != nil {
		return api.Spec{}, err
	}
	if len(in.Gates) > 0 {
		base := cfg.Base
		if b := strings.TrimSpace(in.Base); b != "" {
			base = config.FullRef(b)
		}
		gc, err := gatesAt(ctx, repo.Path, base)
		if err != nil {
			return api.Spec{}, err
		}
		for _, n := range in.Gates {
			g, ok := gc.Gates[n]
			if !ok {
				return api.Spec{}, fmt.Errorf("unknown gate %q at %s (have: %s)", n, strings.TrimPrefix(base, "refs/heads/"), strings.Join(gc.Order, ", "))
			}
			g.Name = n
			in.Acceptance = append(in.Acceptance, g)
		}
	}
	in.CreatedBy = who
	return cfg.NewSpec(in)
}

// create builds a spec from the form with the repository's own defaults
// (the same code as `spine run`) and submits it through the repository's
// inbox, so the controller stays the database's only writer.
func (s *Server) create(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.repo(r)
	if !ok {
		httpError(w, http.StatusNotFound, "unknown repository")
		return
	}
	var req createRequest
	if !decode(w, r, &req) {
		return
	}
	spec, err := s.buildSpec(r.Context(), repo, req.TaskInput, by(r))
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	id := api.TaskID(strings.TrimSpace(req.ID))
	if id == "" {
		id = api.NewTaskID(spec.Intent)
	}
	if !api.ValidTaskID(id) {
		httpError(w, http.StatusBadRequest, "task id must match [A-Za-z0-9][A-Za-z0-9._-]{0,63}")
		return
	}
	if _, err := repo.Store.GetTask(r.Context(), id); err == nil {
		httpError(w, http.StatusConflict, "a task with this id already exists")
		return
	}
	note(r, repo.Name, string(id), "create")
	if _, err := inbox.Write(repo.InboxDir, inbox.Command{Kind: inbox.Submit, Task: id, Spec: &spec, By: by(r)}); err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"id": id, "repo": repo.Name, "controller": repo.State == "running"})
}
