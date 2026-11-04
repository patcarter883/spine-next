package ui

import (
	"context"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/patcarter883/spine-next/internal/api"
	"github.com/patcarter883/spine-next/internal/app"
	"github.com/patcarter883/spine-next/internal/config"
	"github.com/patcarter883/spine-next/internal/gate"
	"github.com/patcarter883/spine-next/internal/inbox"
	"github.com/patcarter883/spine-next/internal/modmap"
	"github.com/patcarter883/spine-next/internal/repoctx"
	"github.com/patcarter883/spine-next/internal/tools/ast"
)

// gatesCheck dry-runs the base's gates (or a draft) on a revision with no
// agent: onboarding stage 1.
func (s *Server) gatesCheck(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.repo(r)
	if !ok {
		httpError(w, http.StatusNotFound, "unknown repository")
		return
	}
	var req struct {
		Rev string `json:"rev"`
	}
	if r.ContentLength > 0 && !decode(w, r, &req) {
		return
	}
	cfg, err := config.Load(repo.Path)
	if err != nil {
		httpError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	rev := strings.TrimSpace(req.Rev)
	if rev == "" {
		rev = strings.TrimPrefix(cfg.Base, "refs/heads/")
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Minute)
	defer cancel()
	gc, ok, err := gate.LoadConfig(ctx, repo.Path, cfg.Base)
	if err != nil {
		httpError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	src := gate.ConfigPath + " at " + strings.TrimPrefix(cfg.Base, "refs/heads/")
	if !ok {
		gc, src = gate.DefaultConfig(ast.Available()), "built-in defaults"
	}
	note(r, repo.Name, "", "gates check "+rev)
	rep, err := app.GateEngine(repo.CommonDir).Check(ctx, repo.Path, rev, gc, src)
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, rep)
}

type contextStatus struct {
	Base        string      `json:"base"`
	GatesFile   bool        `json:"gates_file"`
	Notes       string      `json:"notes"`
	NotesTokens int         `json:"notes_tokens"`
	NotesCap    int         `json:"notes_cap"`
	Truncated   bool        `json:"truncated"`
	Modules     *modmap.Map `json:"modules,omitempty"`
	ModulesErr  string      `json:"modules_error,omitempty"`
	Stale       int         `json:"stale_commits"` // -1 unknown
	StaleAfter  int         `json:"stale_after"`
	Experiment  float64     `json:"experiment"`
	Adapters    []struct {
		Name    string `json:"name"`
		Context bool   `json:"context"`
	} `json:"adapters"`
	Surveys []taskRow `json:"surveys"`
}

// contextInfo reports the repository context at the base: onboarding
// state, the summary, the module map and how stale it is.
func (s *Server) contextInfo(w http.ResponseWriter, r *http.Request) {
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
	ctx := r.Context()
	st := contextStatus{Base: strings.TrimPrefix(cfg.Base, "refs/heads/"), NotesCap: cfg.Context.NotesTokens, StaleAfter: cfg.Context.StaleAfter,
		Experiment: cfg.Context.Experiment, Stale: -1, Surveys: []taskRow{}}
	_, st.GatesFile, _ = gate.LoadConfig(ctx, repo.Path, cfg.Base)
	rc, err := repoctx.Load(ctx, repo.Path, cfg.Base, cfg.Context.NotesTokens)
	if err != nil {
		httpError(w, 500, err.Error())
		return
	}
	st.Notes, st.NotesTokens, st.Truncated, st.Modules = rc.Notes, rc.NotesSize, rc.Truncated, rc.Modules
	if rc.ModErr != nil {
		st.ModulesErr = rc.ModErr.Error()
	}
	st.Stale = repoctx.Staleness(ctx, repo.Path, cfg.Base, rc.Modules)
	for _, n := range cfg.AdapterNames() {
		st.Adapters = append(st.Adapters, struct {
			Name    string `json:"name"`
			Context bool   `json:"context"`
		}{n, cfg.Adapters[n].Context})
	}
	if ts, err := repo.Store.ListTasks(ctx); err == nil {
		for _, t := range ts {
			if t.Spec.Kind == "survey" {
				st.Surveys = append(st.Surveys, row(repo.Name, t))
			}
		}
	}
	writeJSON(w, st)
}

// startSurvey submits a survey task (onboarding stage 2, or a refresh).
func (s *Server) startSurvey(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.repo(r)
	if !ok {
		httpError(w, http.StatusNotFound, "unknown repository")
		return
	}
	var req struct {
		Focus string `json:"focus"`
		Agent string `json:"agent"`
	}
	if r.ContentLength > 0 && !decode(w, r, &req) {
		return
	}
	cfg, err := config.Load(repo.Path)
	if err != nil {
		httpError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	spec, err := cfg.SurveySpec(req.Focus, req.Agent, by(r))
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	id := "survey-" + time.Now().UTC().Format("20060102-150405")
	note(r, repo.Name, id, "survey")
	if _, err := inbox.Write(repo.InboxDir, inbox.Command{Kind: inbox.Submit, Task: api.TaskID(id), Spec: &spec, By: by(r)}); err != nil {
		httpError(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]any{"id": id, "repo": repo.Name, "controller": repo.State == "running"})
}

type suggestion struct {
	Module string   `json:"module"`
	Label  string   `json:"label,omitempty"`
	Scope  []string `json:"scope"`
	Reason string   `json:"reason"`
	Score  float64  `json:"score"`
}

var wordRe = regexp.MustCompile(`[A-Za-z][A-Za-z0-9_]{2,}`)
var pathRe = regexp.MustCompile(`[A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.*-]+)+`)

var stop = map[string]bool{"the": true, "and": true, "add": true, "for": true, "with": true, "that": true, "this": true, "from": true,
	"into": true, "test": true, "tests": true, "fix": true, "make": true, "use": true, "new": true, "should": true, "when": true, "all": true}

// suggestScope proposes scopes for an intent from the module map: modules
// whose path, package or label share words with it, or that it names, each
// with its own tests.
func (s *Server) suggestScope(w http.ResponseWriter, r *http.Request) {
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
	m, ok, err := modmap.Load(r.Context(), repo.Path, cfg.Base)
	if err != nil || !ok {
		writeJSON(w, map[string]any{"suggestions": []suggestion{}, "map": false})
		return
	}
	writeJSON(w, map[string]any{"suggestions": Suggest(m, r.URL.Query().Get("q")), "map": true})
}

// Suggest ranks modules for an intent.
func Suggest(m *modmap.Map, intent string) []suggestion {
	words := map[string]bool{}
	for _, w := range wordRe.FindAllString(strings.ToLower(intent), -1) {
		if !stop[w] {
			words[strings.TrimSuffix(w, "s")] = true
		}
	}
	named := pathRe.FindAllString(intent, -1)
	var out []suggestion
	for _, mod := range m.Modules {
		if mod.Name == "." {
			continue
		}
		var score float64
		var why []string
		for _, p := range named {
			if strings.HasPrefix(p, mod.Name+"/") || p == mod.Name || strings.HasPrefix(mod.Name, strings.TrimSuffix(p, "/")) {
				score += 5
				why = append(why, "named "+p)
			}
		}
		hay := strings.ToLower(mod.Name + " " + mod.Package + " " + mod.Label)
		for _, seg := range wordRe.FindAllString(hay, -1) {
			if words[strings.TrimSuffix(seg, "s")] {
				w := 1.0
				if strings.Contains(strings.ToLower(mod.Name), seg) {
					w = 2
				}
				score += w
				why = append(why, seg)
			}
		}
		if score == 0 {
			continue
		}
		scope := []string{mod.Name + "/**"}
		if mod.Language == "go" {
			scope = []string{mod.Name + "/*"} // a Go package is one directory; subdirectories are other packages
		}
		for _, t := range mod.Tests {
			covered := strings.HasPrefix(t, mod.Name+"/") && (mod.Language != "go" || !strings.Contains(strings.TrimPrefix(t, mod.Name+"/"), "/"))
			if !covered {
				scope = append(scope, t)
			}
		}
		out = append(out, suggestion{Module: mod.Name, Label: mod.Label, Scope: scope, Reason: "matches " + strings.Join(uniq(why), ", "), Score: score})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > 6 {
		out = out[:6]
	}
	if out == nil {
		out = []suggestion{}
	}
	return out
}

func uniq(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}
