package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/patcarter883/spine-next/internal/agent"
	"github.com/patcarter883/spine-next/internal/api"
	"github.com/patcarter883/spine-next/internal/artifacts"
	"github.com/patcarter883/spine-next/internal/config"
	"github.com/patcarter883/spine-next/internal/gate"
	"github.com/patcarter883/spine-next/internal/gitro"
	"github.com/patcarter883/spine-next/internal/storage"
)

var oidRe = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// MaxPatch bounds a diff response.
const MaxPatch = 4 << 20

type diffFile struct {
	Path    string `json:"path"`
	OldPath string `json:"old_path,omitempty"`
	Status  string `json:"status"` // A M D R
	Adds    int    `json:"adds"`
	Dels    int    `json:"dels"`
	Binary  bool   `json:"binary,omitempty"`
}

type diffResponse struct {
	Kind      string     `json:"kind"` // snapshot, compare, merge
	Base      string     `json:"base"`
	Head      string     `json:"head"`
	BaseLabel string     `json:"base_label"`
	HeadLabel string     `json:"head_label"`
	Files     []diffFile `json:"files"`
	Patch     string     `json:"patch"`
	Truncated bool       `json:"truncated"`
}

// diff serves one of:
//
//	?seq=N           snapshot N against the base its attempt started from
//	?seq=N&from=M    snapshot N against snapshot M (round comparison)
//	?merge=<commit>  a merge result (gated, or integrated) against its base
//
// Only commits recorded on the task can be named.
func (s *Server) diff(w http.ResponseWriter, r *http.Request) {
	repo, t, ok := s.getTask(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	ctx := r.Context()
	snap := func(v string) (*api.SnapshotRef, error) {
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("bad snapshot %q", v)
		}
		for i := range t.Status.Snapshots {
			if t.Status.Snapshots[i].Seq == n {
				return &t.Status.Snapshots[i], nil
			}
		}
		return nil, fmt.Errorf("%s has no snapshot #%d", t.ID, n)
	}
	git := gitro.Repo{Dir: repo.Path}
	var resp diffResponse
	switch {
	case q.Get("merge") != "":
		c := q.Get("merge")
		known := c == t.Status.IntegratedCommit || c == t.Status.ProposedCommit
		for _, g := range append(t.Status.GateHistory, derefGate(t.Status.LastGate)...) {
			known = known || (g.OnMerge && g.Commit == c)
		}
		if !oidRe.MatchString(c) || !known {
			httpError(w, http.StatusBadRequest, "not a merge commit of this task")
			return
		}
		parent, err := git.Line(ctx, "rev-parse", "--verify", "-q", c+"^1")
		if err != nil {
			httpError(w, http.StatusGone, "the merge commit is no longer in the repository (garbage-collected?)")
			return
		}
		resp = diffResponse{Kind: "merge", Base: parent, Head: c, BaseLabel: "base tip " + short(parent), HeadLabel: "merge result " + short(c)}
	case q.Get("seq") != "":
		head, err := snap(q.Get("seq"))
		if err != nil {
			httpError(w, http.StatusBadRequest, err.Error())
			return
		}
		resp = diffResponse{Kind: "snapshot", Head: head.Commit, HeadLabel: fmt.Sprintf("snapshot #%d (%s)", head.Seq, head.Label)}
		if f := q.Get("from"); f != "" {
			from, err := snap(f)
			if err != nil {
				httpError(w, http.StatusBadRequest, err.Error())
				return
			}
			resp.Kind, resp.Base, resp.BaseLabel = "compare", from.Commit, fmt.Sprintf("snapshot #%d (%s)", from.Seq, from.Label)
		} else {
			if l, err := repo.Store.GetLease(ctx, string(t.ID), head.Attempt); err == nil && oidRe.MatchString(l.BaseOID) {
				resp.Base = l.BaseOID
			} else if p, err := git.Line(ctx, "rev-parse", "--verify", "-q", head.Commit+"^1"); err == nil {
				resp.Base = p
			}
			resp.BaseLabel = "base " + short(resp.Base)
		}
	default:
		httpError(w, http.StatusBadRequest, "name a snapshot (?seq=) or a merge commit (?merge=)")
		return
	}
	if resp.Base == "" {
		httpError(w, http.StatusGone, "the base of this snapshot is unknown")
		return
	}
	if _, err := git.Line(ctx, "cat-file", "-e", resp.Head+"^{commit}"); err != nil {
		httpError(w, http.StatusGone, "the snapshot is no longer in the repository (garbage-collected?)")
		return
	}
	files, err := numstat(ctx, git, resp.Base, resp.Head)
	if err != nil {
		httpError(w, 500, err.Error())
		return
	}
	resp.Files = files
	patch, err := git.Run(ctx, "-c", "core.quotepath=off", "diff", "--no-color", "--no-ext-diff", "-M", "--src-prefix=a/", "--dst-prefix=b/", resp.Base, resp.Head)
	if err != nil {
		httpError(w, 500, err.Error())
		return
	}
	if len(patch) > MaxPatch {
		patch, resp.Truncated = patch[:MaxPatch], true
	}
	resp.Patch = string(patch)
	writeJSON(w, resp)
}

func derefGate(g *api.GateSummary) []api.GateSummary {
	if g == nil {
		return nil
	}
	return []api.GateSummary{*g}
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func numstat(ctx context.Context, git gitro.Repo, a, b string) ([]diffFile, error) {
	out, err := git.Run(ctx, "diff", "--no-color", "--no-ext-diff", "-M", "-z", "--numstat", a, b)
	if err != nil {
		return nil, err
	}
	st, err := git.Run(ctx, "diff", "--no-color", "--no-ext-diff", "-M", "-z", "--name-status", a, b)
	if err != nil {
		return nil, err
	}
	status := map[string]string{}
	parts := strings.Split(string(st), "\x00")
	for i := 0; i < len(parts)-1; {
		code := parts[i]
		if code == "" {
			break
		}
		if code[0] == 'R' || code[0] == 'C' {
			if i+2 < len(parts) {
				status[parts[i+2]] = code[:1]
			}
			i += 3
			continue
		}
		status[parts[i+1]] = code[:1]
		i += 2
	}
	files := []diffFile{}
	fs := strings.Split(string(out), "\x00")
	for i := 0; i < len(fs); i++ {
		rec := fs[i]
		if rec == "" {
			continue
		}
		cols := strings.SplitN(rec, "\t", 3)
		if len(cols) < 3 {
			continue
		}
		f := diffFile{}
		if cols[0] == "-" {
			f.Binary = true
		} else {
			f.Adds, _ = strconv.Atoi(cols[0])
			f.Dels, _ = strconv.Atoi(cols[1])
		}
		if cols[2] == "" && i+2 < len(fs) { // rename: old\0new
			f.OldPath, f.Path = fs[i+1], fs[i+2]
			i += 2
		} else {
			f.Path = cols[2]
		}
		f.Status = status[f.Path]
		if f.Status == "" {
			f.Status = "M"
		}
		files = append(files, f)
	}
	return files, nil
}

// - Rounds -

type roundView struct {
	Attempt   int                       `json:"attempt"`
	Round     int                       `json:"round"`
	Started   time.Time                 `json:"started,omitempty"`
	Adapter   string                    `json:"adapter,omitempty"`
	Feedback  []api.Feedback            `json:"feedback"`
	Outcome   *api.Outcome              `json:"outcome,omitempty"`
	Snapshots []api.SnapshotRef         `json:"snapshots"`
	Gate      *api.GateSummary          `json:"gate,omitempty"`
	MergeGate *api.GateSummary          `json:"merge_gate,omitempty"`
	Decision  map[string]any            `json:"decision,omitempty"`
	Fixed     []api.Diagnostic          `json:"fixed"`
	New       []api.Diagnostic          `json:"introduced"`
	Persist   []api.Diagnostic          `json:"persisting"`
	Tools     []map[string]any          `json:"tools"`
	Guards    []agent.Event             `json:"guards,omitempty"`
	Messages  int                       `json:"messages"`
	Diags     map[string]api.Diagnostic `json:"-"`
}

// rounds reconstructs every agent round from the task's events: the
// feedback it received, how the run ended (with the agent's final message),
// its snapshots, the gate verdicts (with links to full tool logs), the
// supervisor's decision, and which failures it fixed or introduced compared
// with the previous round of the same attempt.
func (s *Server) rounds(w http.ResponseWriter, r *http.Request) {
	repo, t, ok := s.getTask(w, r)
	if !ok {
		return
	}
	evs, err := repo.Store.ListEvents(r.Context(), t.ID, 0, 100000)
	if err != nil {
		httpError(w, 500, err.Error())
		return
	}
	type key struct{ a, r int }
	byKey := map[key]*roundView{}
	var order []key
	get := func(a, rd int) *roundView {
		k := key{a, rd}
		if v := byKey[k]; v != nil {
			return v
		}
		v := &roundView{Attempt: a, Round: rd, Feedback: []api.Feedback{}, Snapshots: []api.SnapshotRef{}, Fixed: []api.Diagnostic{},
			New: []api.Diagnostic{}, Persist: []api.Diagnostic{}, Tools: []map[string]any{}, Diags: map[string]api.Diagnostic{}}
		byKey[k] = v
		order = append(order, k)
		return v
	}
	var cur *roundView
	for _, e := range evs {
		switch e.Kind {
		case "agent_start":
			var d struct {
				Attempt, Round int
				Adapter        string
				Feedback       []api.Feedback
			}
			json.Unmarshal(e.Data, &d)
			cur = get(d.Attempt, d.Round)
			cur.Started, cur.Adapter = e.At, d.Adapter
			if d.Feedback != nil {
				cur.Feedback = d.Feedback
			}
		case "agent":
			if cur == nil {
				continue
			}
			var ev agent.Event
			json.Unmarshal(e.Data, &ev)
			switch ev.Kind {
			case "tool_call":
				if len(cur.Tools) < 500 {
					cur.Tools = append(cur.Tools, map[string]any{"title": ev.Title, "at": e.At})
				}
			case "tool_done":
				for i := len(cur.Tools) - 1; i >= 0; i-- {
					if cur.Tools[i]["title"] == ev.Title && cur.Tools[i]["status"] == nil {
						cur.Tools[i]["status"] = ev.Status
						break
					}
				}
			case "guard", "permission", "compaction":
				cur.Guards = append(cur.Guards, ev)
			case "message":
				cur.Messages++
			}
		case "outcome":
			var o api.Outcome
			json.Unmarshal(e.Data, &o)
			get(o.Attempt, o.Round).Outcome = &o
		case "verdict":
			var d struct {
				OnMerge bool         `json:"on_merge"`
				Attempt int          `json:"attempt"`
				Round   int          `json:"round"`
				Verdict gate.Verdict `json:"verdict"`
			}
			json.Unmarshal(e.Data, &d)
			v := get(d.Attempt, d.Round)
			sum := api.GateSummary{Attempt: d.Attempt, Round: d.Round, Tree: d.Verdict.Tree, OnMerge: d.OnMerge,
				Passed: d.Verdict.Passed, FailedGate: d.Verdict.FailedGate, Gates: d.Verdict.Gates}
			for _, g := range d.Verdict.Gates {
				for _, x := range g.New {
					sum.Fingerprints = append(sum.Fingerprints, x.Fingerprint)
					sum.NewCount++
					v.Diags[x.Fingerprint] = x
				}
			}
			for _, h := range t.Status.GateHistory {
				if h.Attempt == d.Attempt && h.Round == d.Round && h.OnMerge == d.OnMerge && h.Tree == sum.Tree {
					sum.Commit = h.Commit
				}
			}
			if d.OnMerge {
				v.MergeGate = &sum
			} else {
				v.Gate = &sum
			}
		case "decision":
			var d map[string]any
			json.Unmarshal(e.Data, &d)
			a, _ := d["attempt"].(float64)
			rd, _ := d["round"].(float64)
			get(int(a), int(rd)).Decision = d
		}
	}
	for _, sn := range t.Status.Snapshots {
		var a, rd int
		if _, err := fmt.Sscanf(sn.Label, "a%d-r%d", &a, &rd); err == nil {
			v := get(a, rd)
			v.Snapshots = append(v.Snapshots, sn)
		}
	}
	sort.SliceStable(order, func(i, j int) bool {
		if order[i].a != order[j].a {
			return order[i].a < order[j].a
		}
		return order[i].r < order[j].r
	})
	out := make([]*roundView, 0, len(order))
	var prev *roundView
	for _, k := range order {
		v := byKey[k]
		if prev != nil && prev.Attempt == v.Attempt && prev.Gate != nil && v.Gate != nil {
			was := map[string]bool{}
			for _, fp := range prev.Gate.Fingerprints {
				was[fp] = true
			}
			now := map[string]bool{}
			for _, fp := range v.Gate.Fingerprints {
				now[fp] = true
				if was[fp] {
					v.Persist = append(v.Persist, v.Diags[fp])
				} else {
					v.New = append(v.New, v.Diags[fp])
				}
			}
			for _, fp := range prev.Gate.Fingerprints {
				if !now[fp] {
					v.Fixed = append(v.Fixed, prev.Diags[fp])
				}
			}
		} else if v.Gate != nil {
			for _, fp := range v.Gate.Fingerprints {
				v.New = append(v.New, v.Diags[fp])
			}
		}
		if v.Gate != nil {
			prev = v
		}
		out = append(out, v)
	}
	writeJSON(w, map[string]any{"rounds": out, "best": t.Status.BestSnapshot, "integrated": t.Status.IntegratedCommit})
}

// - Artifacts -

var digestRe = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// artifact serves a stored tool log as plain text (?tail=N for the last N
// bytes; ?download=1 as an attachment).
func (s *Server) artifact(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.repo(r)
	if !ok {
		httpError(w, http.StatusNotFound, "unknown repository")
		return
	}
	d := r.PathValue("digest")
	if !digestRe.MatchString(d) || repo.CommonDir == "" {
		httpError(w, http.StatusBadRequest, "bad digest")
		return
	}
	st := artifacts.Store{Dir: artifactsDir(repo.CommonDir)}
	f, err := st.Open(d)
	if err != nil {
		httpError(w, http.StatusNotFound, "no such artifact (garbage-collected?)")
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	if r.URL.Query().Get("download") != "" {
		w.Header().Set("Content-Disposition", `attachment; filename="`+strings.TrimPrefix(d, "sha256:")[:16]+`.log"`)
	}
	if n, _ := strconv.ParseInt(r.URL.Query().Get("tail"), 10, 64); n > 0 {
		if sk, ok := f.(io.Seeker); ok {
			if size, err := sk.Seek(0, io.SeekEnd); err == nil && size > n {
				sk.Seek(size-n, io.SeekStart)
			} else {
				sk.Seek(0, io.SeekStart)
			}
		}
	}
	io.Copy(w, f)
}

func artifactsDir(commonDir string) string { return commonDir + "/spine/artifacts" }

// - Live view -

// live streams a task's new events (tool calls, agent messages, verdicts)
// and its status as they are written, for watching a run in progress.
func (s *Server) live(w http.ResponseWriter, r *http.Request) {
	repo, t, ok := s.getTask(w, r)
	if !ok {
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		httpError(w, 500, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	var rv int64
	ctx := r.Context()
	deadline := time.After(time.Hour)
	tk := time.NewTicker(400 * time.Millisecond)
	defer tk.Stop()
	for {
		evs, err := repo.Store.ListEvents(ctx, t.ID, after, 500)
		if err == nil {
			for _, e := range evs {
				b, _ := json.Marshal(e)
				fmt.Fprintf(w, "id: %d\nevent: ev\ndata: %s\n\n", e.ID, b)
				after = e.ID
			}
		}
		if cur, err := repo.Store.GetTask(ctx, t.ID); err == nil && cur.ResourceVersion != rv {
			rv = cur.ResourceVersion
			b, _ := json.Marshal(cur)
			fmt.Fprintf(w, "event: task\ndata: %s\n\n", b)
		} else if err == storage.ErrNotFound {
			return
		}
		fl.Flush()
		select {
		case <-ctx.Done():
			return
		case <-deadline:
			return
		case <-tk.C:
		}
	}
}

// - Shared helpers -

func gatesAt(ctx context.Context, repoPath, base string) (gate.Config, error) {
	gc, ok, err := gate.LoadConfig(ctx, repoPath, base)
	if err != nil {
		return gate.Config{}, err
	}
	if !ok {
		return gate.DefaultConfig(true), nil
	}
	return gc, nil
}

type adapterInfo struct {
	Name      string `json:"name"`
	Protocol  string `json:"protocol"`
	Version   string `json:"version,omitempty"`
	Model     string `json:"model,omitempty"`
	Container string `json:"container,omitempty"`
	Command   string `json:"command"`
	Label     string `json:"label"`
	Warning   string `json:"warning,omitempty"`
}

// describeAdapters labels each configured agent for the picker.
func describeAdapters(cfg config.Config) []adapterInfo {
	var out []adapterInfo
	for _, n := range cfg.AdapterNames() {
		a := cfg.Adapters[n]
		p := a.Protocol
		if p == "" {
			p = "acp"
		}
		i := adapterInfo{Name: n, Protocol: p, Version: a.Version, Model: a.Model, Command: strings.Join(a.Argv, " ")}
		if a.Container != nil {
			i.Container = a.Container.Image
		}
		switch {
		case p == "pi-rpc":
			i.Label = "Pi " + a.Version + " (local models)"
			if a.Model != "" {
				i.Label = "Pi " + a.Version + " · " + a.Model
			}
			if a.Container == nil {
				i.Warning = "runs on this machine without a sandbox; give it a container in .spine/config.yaml"
			}
		case n == "claude":
			i.Label = "Claude Code (ACP)"
		default:
			i.Label = n + " (" + p + ")"
		}
		if i.Container != "" {
			i.Label += " in " + i.Container
		}
		out = append(out, i)
	}
	return out
}

var forgeRe = regexp.MustCompile(`^(?:https://|git@|ssh://git@)(github\.com|gitlab\.com|bitbucket\.org|codeberg\.org)[:/]([^/]+/[^/]+?)(?:\.git)?/?$`)

// webURL returns the repository's forge URL for commit links, or "".
func (s *Server) webURL(ctx context.Context, path string) string {
	if v, ok := s.webURLs.Load(path); ok {
		return v.(string)
	}
	url := ""
	if out, err := (gitro.Repo{Dir: path}).Line(ctx, "remote", "get-url", "origin"); err == nil {
		if m := forgeRe.FindStringSubmatch(strings.TrimSpace(out)); m != nil {
			url = "https://" + m[1] + "/" + m[2]
		}
	}
	s.webURLs.Store(path, url)
	return url
}
