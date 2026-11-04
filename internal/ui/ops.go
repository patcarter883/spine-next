package ui

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/patcarter883/spine-next/internal/api"
	"github.com/patcarter883/spine-next/internal/config"
	"github.com/patcarter883/spine-next/internal/gitro"
	"github.com/patcarter883/spine-next/internal/inbox"
	"github.com/patcarter883/spine-next/internal/worktree"
)

func loadConfig(path string) (config.Config, error) { return config.Load(path) }

// - Pause / resume -

func (s *Server) pause(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.repo(r)
	if !ok {
		httpError(w, http.StatusNotFound, "unknown repository")
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	if r.ContentLength > 0 && !decode(w, r, &req) {
		return
	}
	note(r, repo.Name, "", "pause "+trunc(req.Reason, 200))
	if err := inbox.Pause(repo.InboxDir, inbox.PauseInfo{By: by(r), Reason: trunc(req.Reason, 500)}); err != nil {
		httpError(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"paused": true})
}

func (s *Server) resume(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.repo(r)
	if !ok {
		httpError(w, http.StatusNotFound, "unknown repository")
		return
	}
	note(r, repo.Name, "", "resume")
	if err := inbox.Resume(repo.InboxDir); err != nil {
		httpError(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"paused": false})
}

// - Garbage collection -

func (s *Server) worktreeRoot(repo Repo) string {
	if cfg, err := loadConfig(repo.Path); err == nil && cfg.WorktreeRoot != "" {
		return cfg.WorktreeRoot
	}
	return worktree.DefaultRoot(repo.CommonDir)
}

func (s *Server) gcPlan(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.repo(r)
	if !ok {
		httpError(w, http.StatusNotFound, "unknown repository")
		return
	}
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	rep, err := worktree.PlanGC(r.Context(), repo.Path, s.worktreeRoot(repo), repo.Store, days)
	if err != nil {
		httpError(w, 500, err.Error())
		return
	}
	writeJSON(w, rep)
}

// gcRun asks the repository's controller to collect garbage: it holds the
// locks and the writer database the cleanup needs.
func (s *Server) gcRun(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.repo(r)
	if !ok {
		httpError(w, http.StatusNotFound, "unknown repository")
		return
	}
	var req struct {
		Days int `json:"days"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Days < 1 {
		httpError(w, http.StatusBadRequest, "days must be at least 1")
		return
	}
	note(r, repo.Name, "", fmt.Sprintf("gc older than %d days", req.Days))
	id, err := inbox.Write(repo.InboxDir, inbox.Command{Kind: inbox.GC, Days: req.Days, By: by(r)})
	if err != nil {
		httpError(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]any{"queued": id, "controller": repo.State == "running"})
}

// - Repository configuration -

func (s *Server) getConfig(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.repo(r)
	if !ok {
		httpError(w, http.StatusNotFound, "unknown repository")
		return
	}
	path := filepath.Join(repo.Path, config.Path)
	raw, err := os.ReadFile(path)
	exists := err == nil
	out := map[string]any{"path": path, "exists": exists, "raw": string(raw), "restart": s.Admin != nil}
	cfg, err := config.Load(repo.Path)
	if err != nil {
		out["error"] = err.Error()
	} else {
		out["settings"] = map[string]any{"agent": cfg.Agent, "parallel": cfg.Parallel, "base": strings.TrimPrefix(cfg.Base, "refs/heads/"),
			"max_attempts": cfg.Budget.MaxAttempts, "max_patch_rounds": cfg.Budget.MaxPatchRounds, "max_tokens": cfg.Budget.MaxTokens,
			"max_wall_clock": cfg.Budget.MaxWallClock.String(), "per_run_timeout": cfg.Budget.PerRunTimeout.String(),
			"supervisor": cfg.Supervisor.Type}
		out["adapters"] = describeAdapters(cfg)
	}
	writeJSON(w, out)
}

// putConfig validates and writes .spine/config.yaml: either the whole file
// (raw) or form fields merged into it (settings), keeping comments. Under
// the supervisor the repository's controller restarts to pick it up.
func (s *Server) putConfig(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.repo(r)
	if !ok {
		httpError(w, http.StatusNotFound, "unknown repository")
		return
	}
	var req struct {
		Raw      *string          `json:"raw,omitempty"`
		Settings *config.Settings `json:"settings,omitempty"`
	}
	if !decode(w, r, &req) {
		return
	}
	cur, _ := os.ReadFile(filepath.Join(repo.Path, config.Path))
	var next []byte
	switch {
	case req.Raw != nil:
		next = []byte(*req.Raw)
		if _, err := config.Parse(next); err != nil {
			httpError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
	case req.Settings != nil:
		b, err := config.ApplySettings(cur, *req.Settings)
		if err != nil {
			httpError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		next = b
	default:
		httpError(w, http.StatusBadRequest, "send raw or settings")
		return
	}
	s.writeConfig(w, r, repo, cur, next)
}

// writeConfig replaces .spine/config.yaml with validated content and, under
// the supervisor, restarts the repository's controller to pick it up.
func (s *Server) writeConfig(w http.ResponseWriter, r *http.Request, repo Repo, cur, next []byte) {
	path := filepath.Join(repo.Path, config.Path)
	if bytes.Equal(bytes.TrimSpace(cur), bytes.TrimSpace(next)) {
		writeJSON(w, map[string]any{"changed": false})
		return
	}
	note(r, repo.Name, "", "edit "+config.Path)
	if len(cur) > 0 {
		config.WriteFileAtomic(path+".bak", cur, 0o644)
	}
	if err := config.WriteFileAtomic(path, next, 0o644); err != nil {
		httpError(w, 500, err.Error())
		return
	}
	out := map[string]any{"changed": true, "restarted": false}
	// A modified tracked file holds up integration into a checked-out base.
	if st, err := (gitro.Repo{Dir: repo.Path}).Run(r.Context(), "status", "--porcelain", "--untracked-files=no", "--", config.Path); err == nil && len(bytes.TrimSpace(st)) > 0 {
		out["uncommitted"] = true
	}
	if s.Admin != nil {
		if err := s.Admin.RestartRepo(repo.Name); err != nil {
			out["restart_error"] = err.Error()
		} else {
			out["restarted"] = true
		}
	}
	writeJSON(w, out)
}

// - Repositories (supervisor) -

func (s *Server) addRepo(w http.ResponseWriter, r *http.Request) {
	if s.Admin == nil {
		httpError(w, http.StatusNotImplemented, "repositories are managed by `spine supervisor`")
		return
	}
	var req struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if !decode(w, r, &req) {
		return
	}
	note(r, req.Name, "", "add repository "+req.Path)
	if err := s.Admin.AddRepo(r.Context(), strings.TrimSpace(req.Path), strings.TrimSpace(req.Name)); err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) removeRepo(w http.ResponseWriter, r *http.Request) {
	if s.Admin == nil {
		httpError(w, http.StatusNotImplemented, "repositories are managed by `spine supervisor`")
		return
	}
	note(r, r.PathValue("name"), "", "remove repository")
	if err := s.Admin.RemoveRepo(r.PathValue("name")); err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// SupervisorFile is the supervisor's settings file name under Home.
const SupervisorFile = "supervisor.yaml"

func (s *Server) supervisorSettings(w http.ResponseWriter, r *http.Request) {
	if s.Admin == nil {
		writeJSON(w, map[string]any{"supervisor": false})
		return
	}
	sl := s.Admin.Slots()
	out := map[string]any{"supervisor": true, "max_agents": sl.Max["agent"], "max_gates": sl.Max["gate"], "in_use": sl.InUse}
	if s.Home != "" {
		raw, _ := os.ReadFile(filepath.Join(s.Home, SupervisorFile))
		out["raw"], out["path"] = string(raw), filepath.Join(s.Home, SupervisorFile)
	}
	writeJSON(w, out)
}

// putSupervisorSettings changes the global limits now and in supervisor.yaml.
func (s *Server) putSupervisorSettings(w http.ResponseWriter, r *http.Request) {
	if s.Admin == nil {
		httpError(w, http.StatusNotImplemented, "not running under `spine supervisor`")
		return
	}
	var req struct {
		MaxAgents int `json:"max_agents"`
		MaxGates  int `json:"max_gates"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.MaxAgents < 1 || req.MaxGates < 1 || req.MaxAgents > 256 || req.MaxGates > 256 {
		httpError(w, http.StatusBadRequest, "limits must be between 1 and 256")
		return
	}
	note(r, "", "", fmt.Sprintf("limits: %d agents, %d gates", req.MaxAgents, req.MaxGates))
	if s.Home != "" {
		if err := setYAMLInts(filepath.Join(s.Home, SupervisorFile), map[string]int{"max_agents": req.MaxAgents, "max_gates": req.MaxGates}); err != nil {
			httpError(w, 500, err.Error())
			return
		}
	}
	if err := s.Admin.SetLimits(req.MaxAgents, req.MaxGates); err != nil {
		httpError(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// setYAMLInts sets top-level integer keys in a YAML file, keeping the rest.
func setYAMLInts(path string, kv map[string]int) error {
	src, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var doc yaml.Node
	if len(bytes.TrimSpace(src)) > 0 {
		if err := yaml.Unmarshal(src, &doc); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	if doc.Kind == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	}
	m := doc.Content[0]
	for k, v := range kv {
		set := false
		for i := 0; i+1 < len(m.Content); i += 2 {
			if m.Content[i].Value == k {
				m.Content[i+1].Kind, m.Content[i+1].Tag, m.Content[i+1].Value = yaml.ScalarNode, "!!int", strconv.Itoa(v)
				set = true
			}
		}
		if !set {
			m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: k},
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(v)})
		}
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return err
	}
	return config.WriteFileAtomic(path, buf.Bytes(), 0o644)
}

// - Bulk actions -

type bulkTarget struct {
	Repo string `json:"repo"`
	ID   string `json:"id"`
}

// bulk sends one command to many tasks; each result says whether it was
// queued or why not.
func (s *Server) bulk(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action string       `json:"action"`
		Text   string       `json:"text,omitempty"`
		Tasks  []bulkTarget `json:"tasks"`
	}
	if !decode(w, r, &req) {
		return
	}
	k, ok := commandKinds[req.Action]
	if !ok || k == inbox.Edit || k == inbox.Rollback {
		httpError(w, http.StatusBadRequest, "bulk actions: approve, retry, cancel, hint")
		return
	}
	if len(req.Tasks) == 0 || len(req.Tasks) > 500 {
		httpError(w, http.StatusBadRequest, "send 1 to 500 tasks")
		return
	}
	repos := map[string]Repo{}
	for _, x := range s.repos(r.Context()) {
		if x.Store != nil {
			repos[x.Name] = x
		}
	}
	type result struct {
		Repo   string `json:"repo"`
		ID     string `json:"id"`
		Queued bool   `json:"queued"`
		Error  string `json:"error,omitempty"`
	}
	var out []result
	n := 0
	for _, tg := range req.Tasks {
		res := result{Repo: tg.Repo, ID: tg.ID}
		repo, ok := repos[tg.Repo]
		if !ok {
			res.Error = "unknown repository"
			out = append(out, res)
			continue
		}
		t, err := repo.Store.GetTask(r.Context(), api.TaskID(tg.ID))
		if err != nil {
			res.Error = "unknown task"
			out = append(out, res)
			continue
		}
		cmd, err := s.buildCommand(r, repo, t, k, commandRequest{Text: req.Text})
		if err == nil {
			_, err = inbox.Write(repo.InboxDir, cmd)
		}
		if err != nil {
			res.Error = err.Error()
		} else {
			res.Queued = true
			n++
		}
		out = append(out, res)
	}
	note(r, "", "", fmt.Sprintf("bulk %s: %d of %d queued", req.Action, n, len(req.Tasks)))
	writeJSON(w, map[string]any{"results": out, "queued": n})
}
