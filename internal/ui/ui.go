// Package ui serves the Spine dashboard: `spine ui` for one repository and
// `spine supervisor` for every registered repository. It reads each
// repository's database read-only (it never takes a writer lock) and sends
// approve/retry/cancel through that repository's inbox, the same path as the
// CLI, so each repository's controller stays its database's only writer.
// Updates are pushed with SSE.
package ui

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/patcarter883/spine-next/internal/api"
	"github.com/patcarter883/spine-next/internal/config"
	"github.com/patcarter883/spine-next/internal/inbox"
	"github.com/patcarter883/spine-next/internal/storage"
)

//go:embed index.html
var indexHTML []byte

// Repo is one repository the dashboard shows.
type Repo struct {
	Name     string         `json:"name"`
	Path     string         `json:"path"`
	State    string         `json:"state"` // running, stopped, broken, starting
	Error    string         `json:"error,omitempty"`
	Restarts int            `json:"restarts,omitempty"`
	Store    *storage.Store `json:"-"` // read-only; nil if the database cannot be opened
	InboxDir string         `json:"-"`
}

// Source lists the repositories to show.
type Source interface {
	Repos(ctx context.Context) []Repo
}

// SourceFunc adapts a function.
type SourceFunc func(ctx context.Context) []Repo

func (f SourceFunc) Repos(ctx context.Context) []Repo { return f(ctx) }

// Server serves the dashboard.
type Server struct {
	Source Source
	// Multi is true under the supervisor (shows the repository switcher).
	Multi bool
}

// Handler returns the HTTP routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'")
		w.Write(indexHTML)
	})
	mux.HandleFunc("GET /api/overview", s.overviewHandler)
	mux.HandleFunc("GET /api/repos/{repo}/tasks/{id}", s.task)
	mux.HandleFunc("GET /api/repos/{repo}/tasks/{id}/events", s.events)
	mux.HandleFunc("GET /api/stream", s.stream)
	mux.HandleFunc("POST /api/repos/{repo}/tasks/{id}/{action}", s.command)
	mux.HandleFunc("POST /api/repos/{repo}/tasks", s.create)
	mux.HandleFunc("GET /api/repos/{repo}/defaults", s.defaults)
	return mux
}

type taskRow struct {
	Repo       string     `json:"repo"`
	ID         api.TaskID `json:"id"`
	Title      string     `json:"title"`
	Phase      api.Phase  `json:"phase"`
	Reason     string     `json:"reason"`
	Message    string     `json:"message"`
	Attempt    int        `json:"attempt"`
	Round      int        `json:"round"`
	Tokens     int64      `json:"tokens"`
	Created    time.Time  `json:"created"`
	Updated    time.Time  `json:"updated"`
	Commit     string     `json:"commit,omitempty"`
	NeedsYou   bool       `json:"needs_you"`
	FailedGate string     `json:"failed_gate,omitempty"`
}

func row(repo string, t api.Task) taskRow {
	title := t.Spec.Title
	if title == "" {
		title = t.Spec.Intent
		if len(title) > 100 {
			title = title[:100] + "…"
		}
	}
	r := taskRow{Repo: repo, ID: t.ID, Title: title, Phase: t.Status.Phase, Reason: t.Status.Reason, Message: t.Status.Message,
		Attempt: t.Status.Attempt, Round: t.Status.PatchRound, Tokens: t.Status.TokensUsed, Created: t.CreatedAt,
		Updated: t.UpdatedAt, Commit: t.Status.IntegratedCommit, NeedsYou: t.Status.Phase == api.PhaseEscalated}
	if g := t.Status.LastGate; g != nil && !g.Passed {
		r.FailedGate = g.FailedGate
	}
	return r
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

type overview struct {
	Multi bool      `json:"multi"`
	Repos []Repo    `json:"repos"`
	Tasks []taskRow `json:"tasks"`
}

// overview lists every repository and all their tasks, newest first.
func (s *Server) overview(ctx context.Context) overview {
	repos := s.Source.Repos(ctx)
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
	return o
}

func (s *Server) overviewHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.overview(r.Context()))
}

func (s *Server) repo(r *http.Request) (Repo, bool) {
	name := r.PathValue("repo")
	for _, x := range s.Source.Repos(r.Context()) {
		if x.Name == name && x.Store != nil {
			return x, true
		}
	}
	return Repo{}, false
}

func (s *Server) task(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.repo(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	t, err := repo.Store.GetTask(r.Context(), api.TaskID(r.PathValue("id")))
	if err == storage.ErrNotFound {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, t)
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.repo(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	evs, err := repo.Store.ListEvents(r.Context(), api.TaskID(r.PathValue("id")), after, 1000)
	if err != nil {
		http.Error(w, err.Error(), 500)
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
		http.Error(w, "streaming unsupported", 500)
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

func (s *Server) command(w http.ResponseWriter, r *http.Request) {
	// Same-origin only: the page sends this header; cross-site forms cannot.
	if r.Header.Get("X-Spine") != "1" {
		http.Error(w, "missing X-Spine header", http.StatusForbidden)
		return
	}
	kinds := map[string]inbox.Kind{"approve": inbox.Approve, "retry": inbox.Retry, "cancel": inbox.Cancel}
	k, ok := kinds[r.PathValue("action")]
	if !ok {
		http.NotFound(w, r)
		return
	}
	repo, ok := s.repo(r)
	if !ok {
		http.Error(w, "unknown repository", http.StatusNotFound)
		return
	}
	id := api.TaskID(r.PathValue("id"))
	if _, err := repo.Store.GetTask(r.Context(), id); err != nil {
		http.Error(w, "unknown task", http.StatusNotFound)
		return
	}
	cid, err := inbox.Write(repo.InboxDir, inbox.Command{Kind: k, Task: id, By: "ui"})
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]any{"queued": cid, "controller": repo.State == "running"})
}

// - Creating tasks -

type createRequest struct {
	ID string `json:"id"`
	config.TaskInput
}

func httpError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// defaults returns what the new-task form pre-fills for a repository.
func (s *Server) defaults(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.repo(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	cfg, err := config.Load(repo.Path)
	if err != nil {
		httpError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, map[string]any{
		"base": strings.TrimPrefix(cfg.Base, "refs/heads/"), "agent": cfg.Agent, "agents": cfg.AdapterNames(),
		"max_attempts": cfg.Budget.MaxAttempts, "max_rounds": cfg.Budget.MaxPatchRounds,
	})
}

// create builds a spec from the form with the repository's own defaults
// (the same code as `spine run`) and submits it through the repository's
// inbox, so the controller stays the database's only writer.
func (s *Server) create(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Spine") != "1" {
		httpError(w, http.StatusForbidden, "missing X-Spine header")
		return
	}
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		httpError(w, http.StatusUnsupportedMediaType, "send JSON")
		return
	}
	repo, ok := s.repo(r)
	if !ok {
		httpError(w, http.StatusNotFound, "unknown repository")
		return
	}
	var req createRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		httpError(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	cfg, err := config.Load(repo.Path)
	if err != nil {
		httpError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	spec, err := cfg.NewSpec(req.TaskInput)
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
	if _, err := inbox.Write(repo.InboxDir, inbox.Command{Kind: inbox.Submit, Task: id, Spec: &spec, By: "ui"}); err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"id": id, "repo": repo.Name, "controller": repo.State == "running"})
}
