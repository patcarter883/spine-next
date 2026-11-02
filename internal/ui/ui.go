// Package ui serves `spine ui`: a small embedded dashboard (RFC §10, M4).
// It reads the database read-only (it never takes the writer lock) and
// sends approve/retry/cancel through the same inbox as the CLI, so the
// controller remains the only writer. Updates are pushed with SSE.
package ui

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/patcarter883/spine-next/internal/api"
	"github.com/patcarter883/spine-next/internal/inbox"
	"github.com/patcarter883/spine-next/internal/storage"
)

//go:embed index.html
var indexHTML []byte

// Server serves the dashboard for one repository.
type Server struct {
	Store    *storage.Store // read-only
	InboxDir string
	// Running reports whether a controller holds the store.
	Running func() bool
}

// Handler returns the HTTP routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'")
		w.Write(indexHTML)
	})
	mux.HandleFunc("GET /api/tasks", s.tasks)
	mux.HandleFunc("GET /api/tasks/{id}", s.task)
	mux.HandleFunc("GET /api/tasks/{id}/events", s.events)
	mux.HandleFunc("GET /api/stream", s.stream)
	mux.HandleFunc("POST /api/tasks/{id}/{action}", s.command)
	return mux
}

type taskRow struct {
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

func row(t api.Task) taskRow {
	title := t.Spec.Title
	if title == "" {
		title = t.Spec.Intent
		if len(title) > 100 {
			title = title[:100] + "…"
		}
	}
	r := taskRow{ID: t.ID, Title: title, Phase: t.Status.Phase, Reason: t.Status.Reason, Message: t.Status.Message,
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

func (s *Server) list(ctx context.Context) ([]taskRow, error) {
	ts, err := s.Store.ListTasks(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]taskRow, 0, len(ts))
	for _, t := range ts {
		out = append(out, row(t))
	}
	return out, nil
}

func (s *Server) tasks(w http.ResponseWriter, r *http.Request) {
	rows, err := s.list(r.Context())
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]any{"tasks": rows, "controller": s.Running != nil && s.Running()})
}

func (s *Server) task(w http.ResponseWriter, r *http.Request) {
	t, err := s.Store.GetTask(r.Context(), api.TaskID(r.PathValue("id")))
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
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	evs, err := s.Store.ListEvents(r.Context(), api.TaskID(r.PathValue("id")), after, 1000)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if evs == nil {
		evs = []storage.Event{}
	}
	writeJSON(w, evs)
}

// stream pushes the task list whenever it changes (polled every second
// server-side; the store has no change feed across processes).
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
		rows, err := s.list(r.Context())
		if err == nil {
			b, _ := json.Marshal(map[string]any{"tasks": rows, "controller": s.Running != nil && s.Running()})
			if string(b) != last {
				last = string(b)
				fmt.Fprintf(w, "event: tasks\ndata: %s\n\n", b)
				fl.Flush()
			}
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
	id := api.TaskID(r.PathValue("id"))
	if _, err := s.Store.GetTask(r.Context(), id); err != nil {
		http.Error(w, "unknown task", http.StatusNotFound)
		return
	}
	cid, err := inbox.Write(s.InboxDir, inbox.Command{Kind: k, Task: id, By: "ui"})
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]any{"queued": cid, "controller": s.Running != nil && s.Running()})
}
