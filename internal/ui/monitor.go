package ui

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/patcarter883/spine-next/internal/api"
	"github.com/patcarter883/spine-next/internal/doctor"
	"github.com/patcarter883/spine-next/internal/notify"
	"github.com/patcarter883/spine-next/internal/storage"
	"github.com/patcarter883/spine-next/internal/worktree"
)

// - Stats: project dashboard, cost, quality -

type dayStat struct {
	Day        string  `json:"day"`
	Integrated int     `json:"integrated"`
	Failed     int     `json:"failed"`
	Created    int     `json:"created"`
	Tokens     int64   `json:"tokens"`
	CostUSD    float64 `json:"cost_usd"`
	Runs       int     `json:"runs"`
}

type repoStat struct {
	Repo       string  `json:"repo"`
	InFlight   int     `json:"in_flight"`
	Pending    int     `json:"pending"`
	NeedsYou   int     `json:"needs_you"`
	Integrated int     `json:"integrated"`
	Failed     int     `json:"failed"`
	Tokens     int64   `json:"tokens"`
	CostUSD    float64 `json:"cost_usd"`
}

type count struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type supStat struct {
	Supervisor string         `json:"supervisor"`
	Decisions  int            `json:"decisions"`
	Actions    map[string]int `json:"actions"`
	Tasks      int            `json:"tasks"`
	Integrated int            `json:"integrated"`
	// Improved counts decisions whose next round failed fewer checks.
	Improved  int `json:"improved"`
	Followed  int `json:"followed"` // decisions with a later gated round
	Fallbacks int `json:"fallbacks"`
}

type stats struct {
	Days   int                `json:"days"`
	Since  time.Time          `json:"since"`
	Totals map[string]int     `json:"totals"`
	Repos  []repoStat         `json:"repos"`
	PerDay []dayStat          `json:"per_day"`
	Waits  map[string]float64 `json:"waits"` // minutes: queue_p50/p90, approval_p50/p90, escalated_age_max
	Cost   struct {
		Tokens   int64      `json:"tokens"`
		CostUSD  float64    `json:"cost_usd"`
		TopTasks []taskCost `json:"top_tasks"`
		Budget   []taskCost `json:"over_budget"`
		Today    struct {
			Tokens  int64   `json:"tokens"`
			CostUSD float64 `json:"cost_usd"`
		} `json:"today"`
		Limits notify.Budgets `json:"limits"`
	} `json:"cost"`
	Quality struct {
		Finished       int       `json:"finished"`
		FirstRoundPass int       `json:"first_round_pass"`
		FirstRoundRate float64   `json:"first_round_rate"`
		AvgRounds      float64   `json:"avg_rounds"`
		AvgAttempts    float64   `json:"avg_attempts"`
		Escalations    []count   `json:"escalations"`
		FailingGates   []count   `json:"failing_gates"`
		FailReasons    []count   `json:"fail_reasons"`
		Supervisors    []supStat `json:"supervisors"`
		OutcomeReasons []count   `json:"outcome_reasons"`
	} `json:"quality"`
}

type taskCost struct {
	Repo      string  `json:"repo"`
	ID        string  `json:"id"`
	Title     string  `json:"title"`
	Tokens    int64   `json:"tokens"`
	CostUSD   float64 `json:"cost_usd"`
	Rounds    int     `json:"rounds"`
	MaxTokens int64   `json:"max_tokens,omitempty"`
	Phase     string  `json:"phase"`
}

func pct(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	sort.Float64s(xs)
	i := int(p * float64(len(xs)-1))
	return xs[i]
}

func sorted(m map[string]int) []count {
	out := []count{}
	for k, v := range m {
		out = append(out, count{k, v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// stats aggregates tasks and their events across repositories.
func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days <= 0 || days > 365 {
		days = 14
	}
	writeJSON(w, s.computeStats(r.Context(), days, r.URL.Query().Get("repo")))
}

func (s *Server) computeStats(ctx context.Context, days int, only string) stats {
	now := time.Now().UTC()
	today := now.Format("2006-01-02")
	since := now.AddDate(0, 0, -days+1).Truncate(24 * time.Hour)
	st := stats{Days: days, Since: since, Totals: map[string]int{}, Repos: []repoStat{}, Waits: map[string]float64{}}
	st.Cost.TopTasks, st.Cost.Budget = []taskCost{}, []taskCost{}
	st.Quality.Supervisors = []supStat{}
	if cfg, ok := s.notifyCfg(); ok {
		st.Cost.Limits = cfg.Budgets
	}
	per := map[string]*dayStat{}
	for d := since; !d.After(now); d = d.AddDate(0, 0, 1) {
		k := d.Format("2006-01-02")
		per[k] = &dayStat{Day: k}
	}
	dayOf := func(t time.Time) *dayStat { return per[t.UTC().Format("2006-01-02")] }
	var queue, approval []float64
	escal, failGates, failReasons, outcomes := map[string]int{}, map[string]int{}, map[string]int{}, map[string]int{}
	sups := map[string]*supStat{}
	var rounds, attempts []float64
	var costs []taskCost
	for _, repo := range s.repos(ctx) {
		if repo.Store == nil || (only != "" && repo.Name != only) {
			continue
		}
		rs := repoStat{Repo: repo.Name}
		tasks, err := repo.Store.ListTasks(ctx)
		if err != nil {
			continue
		}
		byID := map[api.TaskID]api.Task{}
		for _, t := range tasks {
			byID[t.ID] = t
			ph := t.Status.Phase
			switch {
			case ph == api.PhasePending:
				rs.Pending++
			case ph == api.PhaseEscalated:
				rs.NeedsYou++
				age := now.Sub(t.UpdatedAt).Minutes()
				if age > st.Waits["escalated_age_max"] {
					st.Waits["escalated_age_max"] = age
				}
			case !ph.Terminal():
				rs.InFlight++
			case ph == api.PhaseIntegrated:
				rs.Integrated++
			case ph == api.PhaseFailed:
				rs.Failed++
			}
			if t.CreatedAt.After(since) {
				if d := dayOf(t.CreatedAt); d != nil {
					d.Created++
				}
			}
			if !t.Status.StartedAt.IsZero() && t.Status.StartedAt.After(since) {
				queue = append(queue, t.Status.StartedAt.Sub(t.CreatedAt).Minutes())
			}
			for _, g := range t.Status.GateHistory {
				if !g.Passed && g.FailedGate != "" {
					name := g.FailedGate
					if g.OnMerge {
						name += " (merge result)"
					}
					failGates[name]++
				}
			}
			if t.Spec.Kind != api.KindPlan && (ph == api.PhaseIntegrated || ph == api.PhaseFailed) && t.UpdatedAt.After(since) {
				st.Quality.Finished++
				attempts = append(attempts, float64(t.Status.Attempt))
				if ph == api.PhaseIntegrated && t.Status.Attempt <= 1 && t.Status.PatchRound == 0 {
					st.Quality.FirstRoundPass++
				}
				if ph == api.PhaseFailed {
					failReasons[t.Status.Reason]++
				}
			}
			tc := taskCost{Repo: repo.Name, ID: string(t.ID), Title: row(repo.Name, t).Title, Tokens: t.Status.TokensUsed,
				MaxTokens: t.Spec.Budget.MaxTokens, Phase: string(ph)}
			costs = append(costs, tc)
			rs.Tokens += t.Status.TokensUsed
		}
		evs, _ := repo.Store.ListEventsByKind(ctx, since, 0, "outcome", "transition", "decision", "verdict", "command")
		roundsBy := map[api.TaskID]int{}
		costBy := map[api.TaskID]float64{}
		escAt := map[api.TaskID]time.Time{}
		type gated struct {
			at    time.Time
			count int
		}
		verdicts := map[api.TaskID][]gated{}
		type dec struct {
			sup string
			at  time.Time
			n   int
		}
		var decs []struct {
			task api.TaskID
			dec
		}
		for _, e := range evs {
			switch e.Kind {
			case "outcome":
				var o api.Outcome
				json.Unmarshal(e.Data, &o)
				if d := dayOf(e.At); d != nil {
					d.Tokens += o.TokensUsed
					d.CostUSD += o.CostUSD
					d.Runs++
				}
				st.Cost.Tokens += o.TokensUsed
				st.Cost.CostUSD += o.CostUSD
				rs.CostUSD += o.CostUSD
				costBy[e.TaskID] += o.CostUSD
				roundsBy[e.TaskID]++
				outcomes[string(o.Reason)]++
				if e.At.Format("2006-01-02") == today {
					st.Cost.Today.Tokens += o.TokensUsed
					st.Cost.Today.CostUSD += o.CostUSD
				}
			case "transition":
				var d struct {
					To     api.Phase `json:"to"`
					Reason string    `json:"reason"`
				}
				json.Unmarshal(e.Data, &d)
				switch d.To {
				case api.PhaseIntegrated:
					if x := dayOf(e.At); x != nil {
						x.Integrated++
					}
				case api.PhaseFailed:
					if x := dayOf(e.At); x != nil {
						x.Failed++
					}
				case api.PhaseEscalated:
					escal[d.Reason]++
					if d.Reason == "AwaitingApproval" {
						escAt[e.TaskID] = e.At
					}
				}
			case "command":
				var c struct {
					Kind string `json:"kind"`
				}
				json.Unmarshal(e.Data, &c)
				if c.Kind == "approve" {
					if at, ok := escAt[e.TaskID]; ok {
						approval = append(approval, e.At.Sub(at).Minutes())
						delete(escAt, e.TaskID)
					}
				}
			case "verdict":
				var d struct {
					OnMerge bool `json:"on_merge"`
					Verdict struct {
						Gates []api.GateVerdict `json:"gates"`
					} `json:"verdict"`
				}
				json.Unmarshal(e.Data, &d)
				if !d.OnMerge {
					n := 0
					for _, g := range d.Verdict.Gates {
						if !g.Passed() && !g.Skipped {
							n += 1 + len(g.New)
						}
					}
					verdicts[e.TaskID] = append(verdicts[e.TaskID], gated{e.At, n})
				}
			case "decision":
				var d struct {
					Supervisor string `json:"supervisor"`
					Fallback   string `json:"fallback_reason"`
					Decision   struct {
						Action string `json:"action"`
					} `json:"decision"`
				}
				json.Unmarshal(e.Data, &d)
				name := d.Supervisor
				if name == "" {
					name = "rules"
				}
				sp := sups[name]
				if sp == nil {
					sp = &supStat{Supervisor: name, Actions: map[string]int{}}
					sups[name] = sp
				}
				sp.Decisions++
				sp.Actions[d.Decision.Action]++
				if d.Fallback != "" {
					sp.Fallbacks++
				}
				// The failure count of the round being decided on.
				n := -1
				if vs := verdicts[e.TaskID]; len(vs) > 0 {
					n = vs[len(vs)-1].count
				}
				decs = append(decs, struct {
					task api.TaskID
					dec
				}{e.TaskID, dec{name, e.At, n}})
			}
		}
		tasksBy := map[string]map[api.TaskID]bool{}
		for _, d := range decs {
			sp := sups[d.sup]
			if tasksBy[d.sup] == nil {
				tasksBy[d.sup] = map[api.TaskID]bool{}
			}
			if !tasksBy[d.sup][d.task] {
				tasksBy[d.sup][d.task] = true
				sp.Tasks++
				if byID[d.task].Status.Phase == api.PhaseIntegrated {
					sp.Integrated++
				}
			}
			for _, v := range verdicts[d.task] {
				if v.at.After(d.at) {
					sp.Followed++
					if d.n >= 0 && v.count < d.n {
						sp.Improved++
					}
					break
				}
			}
		}
		for i := range costs {
			if costs[i].Repo == repo.Name {
				costs[i].CostUSD = costBy[api.TaskID(costs[i].ID)]
				costs[i].Rounds = roundsBy[api.TaskID(costs[i].ID)]
			}
		}
		for _, t := range tasks {
			if t.Spec.Kind != api.KindPlan && (t.Status.Phase == api.PhaseIntegrated || t.Status.Phase == api.PhaseFailed) && t.UpdatedAt.After(since) {
				rounds = append(rounds, float64(roundsBy[t.ID]))
			}
		}
		st.Repos = append(st.Repos, rs)
		st.Totals["in_flight"] += rs.InFlight
		st.Totals["pending"] += rs.Pending
		st.Totals["needs_you"] += rs.NeedsYou
		st.Totals["integrated"] += rs.Integrated
		st.Totals["failed"] += rs.Failed
	}
	if d := per[today]; d != nil {
		st.Totals["merged_today"] = d.Integrated
		st.Totals["failed_today"] = d.Failed
	}
	for d := since; !d.After(now); d = d.AddDate(0, 0, 1) {
		st.PerDay = append(st.PerDay, *per[d.Format("2006-01-02")])
	}
	st.Waits["queue_p50"], st.Waits["queue_p90"] = pct(queue, .5), pct(queue, .9)
	st.Waits["approval_p50"], st.Waits["approval_p90"] = pct(approval, .5), pct(approval, .9)
	sort.Slice(costs, func(i, j int) bool { return costs[i].Tokens > costs[j].Tokens })
	for i, c := range costs {
		if i < 10 && c.Tokens > 0 {
			st.Cost.TopTasks = append(st.Cost.TopTasks, c)
		}
		pctLimit := st.Cost.Limits.TaskPercent
		if pctLimit == 0 {
			pctLimit = 80
		}
		if c.MaxTokens > 0 && c.Tokens*100 >= c.MaxTokens*int64(pctLimit) {
			st.Cost.Budget = append(st.Cost.Budget, c)
		}
	}
	if st.Quality.Finished > 0 {
		st.Quality.FirstRoundRate = float64(st.Quality.FirstRoundPass) / float64(st.Quality.Finished)
	}
	st.Quality.AvgRounds, st.Quality.AvgAttempts = mean(rounds), mean(attempts)
	st.Quality.Escalations, st.Quality.FailingGates = sorted(escal), sorted(failGates)
	st.Quality.FailReasons, st.Quality.OutcomeReasons = sorted(failReasons), sorted(outcomes)
	for _, sp := range sups {
		st.Quality.Supervisors = append(st.Quality.Supervisors, *sp)
	}
	sort.Slice(st.Quality.Supervisors, func(i, j int) bool {
		return st.Quality.Supervisors[i].Supervisor < st.Quality.Supervisors[j].Supervisor
	})
	return st
}

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	t := 0.0
	for _, x := range xs {
		t += x
	}
	return t / float64(len(xs))
}

// - Slots -

type repoSlots struct {
	Repo     string       `json:"repo"`
	State    string       `json:"state"`
	Restarts int          `json:"restarts"`
	Since    time.Time    `json:"since,omitempty"`
	Error    string       `json:"error,omitempty"`
	Paused   bool         `json:"paused"`
	Parallel int          `json:"parallel"`
	Held     []api.TaskID `json:"held"`
	// Ready are Pending tasks whose dependencies have landed: they wait for
	// a slot (or for the repository to be resumed).
	Ready   []api.TaskID `json:"ready"`
	Blocked int          `json:"blocked"`
}

func (s *Server) slots(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	out := map[string]any{"supervisor": s.Admin != nil}
	if s.Admin != nil {
		out["global"] = s.Admin.Slots()
	}
	var rows []repoSlots
	for _, repo := range s.repos(ctx) {
		rs := repoSlots{Repo: repo.Name, State: repo.State, Restarts: repo.Restarts, Since: repo.Since, Error: firstLine(repo.Error, 300),
			Paused: repo.Paused != nil, Held: []api.TaskID{}, Ready: []api.TaskID{}}
		if repo.Store == nil {
			rows = append(rows, rs)
			continue
		}
		tasks, _ := repo.Store.ListTasks(ctx)
		phase := map[api.TaskID]api.Phase{}
		for _, t := range tasks {
			phase[t.ID] = t.Status.Phase
		}
		held, par, live := []api.TaskID(nil), 0, false
		if s.Admin != nil {
			held, par, live = s.Admin.RepoSlots(repo.Name)
		}
		if !live {
			if cfg, err := loadConfig(repo.Path); err == nil {
				par = cfg.Parallel
			}
			for _, t := range tasks {
				switch t.Status.Phase {
				case api.PhaseProvisioned, api.PhaseWorking, api.PhaseVerifying, api.PhaseVerified:
					held = append(held, t.ID)
				}
			}
		}
		rs.Parallel = par
		if held != nil {
			rs.Held = held
		}
		for _, t := range tasks {
			if t.Status.Phase != api.PhasePending {
				continue
			}
			ready := true
			for _, d := range t.Spec.DependsOn {
				if strings.Contains(string(d), ":") {
					continue // other repository: shown on the graph
				}
				if phase[d] != api.PhaseIntegrated {
					ready = false
				}
			}
			if ready {
				rs.Ready = append(rs.Ready, t.ID)
			} else {
				rs.Blocked++
			}
		}
		rows = append(rows, rs)
	}
	out["repos"] = rows
	writeJSON(w, out)
}

// - Health -

type healthReport struct {
	Repo       string             `json:"repo"`
	Findings   []doctor.Finding   `json:"findings"`
	DBBytes    int64              `json:"db_bytes"`
	WALBytes   int64              `json:"wal_bytes"`
	DiskFree   uint64             `json:"disk_free"`
	DiskTotal  uint64             `json:"disk_total"`
	Stuck      []string           `json:"stuck"`
	GC         *worktree.GCReport `json:"gc,omitempty"`
	InboxQueue int                `json:"inbox_queue"`
	Rejected   []string           `json:"rejected"`
}

// health runs `spine doctor` (and, with ?probe=1, the agent handshake)
// plus checks for disk, database size and stuck work.
func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.repo(r)
	if !ok {
		httpError(w, http.StatusNotFound, "unknown repository")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	h := healthReport{Repo: repo.Name, Stuck: []string{}, Rejected: []string{}}
	h.Findings = append(doctor.Run(ctx, repo.Path), doctor.Agent(ctx, repo.Path, r.URL.Query().Get("probe") == "1")...)
	h.DBBytes, h.WALBytes = repo.Store.Sizes()
	var fs syscall.Statfs_t
	if syscall.Statfs(repo.Path, &fs) == nil {
		h.DiskFree, h.DiskTotal = fs.Bavail*uint64(fs.Bsize), fs.Blocks*uint64(fs.Bsize)
		if h.DiskTotal > 0 && h.DiskFree*20 < h.DiskTotal {
			h.Findings = append(h.Findings, doctor.Finding{Level: doctor.Warn, Check: "disk", Message: "less than 5% free"})
		}
	}
	if h.WALBytes > 256<<20 {
		h.Findings = append(h.Findings, doctor.Finding{Level: doctor.Warn, Check: "database", Message: "WAL is over 256 MiB: the controller may be unable to checkpoint (a long-lived reader?)"})
	}
	tasks, _ := repo.Store.ListTasks(ctx)
	byID := map[string]api.Task{}
	for _, t := range tasks {
		byID[string(t.ID)] = t
		b := t.Spec.Budget.WithDefaults()
		if !t.Status.Phase.Terminal() && t.Status.Phase != api.PhasePending && time.Since(t.UpdatedAt) > b.PerRunTimeout+15*time.Minute {
			h.Stuck = append(h.Stuck, "task "+string(t.ID)+" has not changed for "+time.Since(t.UpdatedAt).Round(time.Minute).String()+" ("+string(t.Status.Phase)+")")
		}
	}
	if leases, err := repo.Store.ListLeases(ctx, storage.LeaseRequested, storage.LeaseReady, storage.LeaseDraining); err == nil {
		for _, l := range leases {
			t, ok := byID[l.TaskID]
			switch {
			case !ok:
				h.Stuck = append(h.Stuck, "lease "+l.Path+" belongs to an unknown task")
			case t.Status.Phase.Done():
				h.Stuck = append(h.Stuck, "lease "+l.Path+" is held by "+string(t.Status.Phase)+" task "+l.TaskID)
			case l.Attempt != t.Status.Attempt:
				h.Stuck = append(h.Stuck, "lease "+l.Path+" is from an earlier attempt of "+l.TaskID)
			}
		}
	}
	if cfg, err := loadConfig(repo.Path); err == nil && repo.CommonDir != "" {
		root := cfg.WorktreeRoot
		if root == "" {
			root = worktree.DefaultRoot(repo.CommonDir)
		}
		if rep, err := worktree.PlanGC(ctx, repo.Path, root, repo.Store, 14); err == nil {
			h.GC = &rep
			for _, p := range rep.StrayWorktrees {
				h.Stuck = append(h.Stuck, "stray worktree "+p)
			}
		}
	}
	if ents, err := os.ReadDir(repo.InboxDir); err == nil {
		for _, e := range ents {
			if strings.HasSuffix(e.Name(), ".json") {
				h.InboxQueue++
			}
		}
	}
	if ents, err := os.ReadDir(filepath.Join(repo.InboxDir, "rejected")); err == nil {
		for _, e := range ents {
			if len(h.Rejected) < 20 && strings.HasSuffix(e.Name(), ".json") {
				h.Rejected = append(h.Rejected, e.Name())
			}
		}
	}
	writeJSON(w, h)
}

// - Search / history -

// search filters tasks across repositories: q (id, title, intent, commit),
// repo, phase (comma-separated), by, kind, from/to (dates, YYYY-MM-DD).
func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	text := strings.ToLower(strings.TrimSpace(q.Get("q")))
	phases := map[string]bool{}
	for _, p := range strings.Split(q.Get("phase"), ",") {
		if p = strings.TrimSpace(p); p != "" {
			phases[p] = true
		}
	}
	from, _ := time.Parse("2006-01-02", q.Get("from"))
	to, _ := time.Parse("2006-01-02", q.Get("to"))
	who := strings.ToLower(q.Get("by"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 2000 {
		limit = 500
	}
	ctx := r.Context()
	out := []taskRow{}
	for _, repo := range s.repos(ctx) {
		if repo.Store == nil || (q.Get("repo") != "" && q.Get("repo") != repo.Name) {
			continue
		}
		tasks, err := repo.Store.ListTasks(ctx)
		if err != nil {
			continue
		}
		for _, t := range tasks {
			rw := row(repo.Name, t)
			if len(phases) > 0 && !phases[string(t.Status.Phase)] {
				continue
			}
			if !from.IsZero() && t.CreatedAt.Before(from) {
				continue
			}
			if !to.IsZero() && t.CreatedAt.After(to.Add(24*time.Hour)) {
				continue
			}
			if who != "" && !strings.Contains(strings.ToLower(t.Spec.CreatedBy), who) {
				continue
			}
			if k := q.Get("kind"); k != "" && (k == "plan") != (t.Spec.Kind == api.KindPlan) {
				continue
			}
			if text != "" {
				hay := strings.ToLower(string(t.ID) + "\n" + t.Spec.Title + "\n" + t.Spec.Intent + "\n" + t.Status.IntegratedCommit + "\n" + t.Status.Message + "\n" + strings.Join(t.Spec.Scope, " "))
				if !strings.Contains(hay, text) {
					continue
				}
			}
			out = append(out, rw)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	total := len(out)
	if len(out) > limit {
		out = out[:limit]
	}
	writeJSON(w, map[string]any{"tasks": out, "total": total})
}

// - Notifications -

func (s *Server) notifyFile() (notify.File, bool) {
	if s.Home == "" {
		return notify.File{}, false
	}
	return notify.File{Path: filepath.Join(s.Home, "notify.yaml")}, true
}

func (s *Server) notifyCfg() (notify.Config, bool) {
	f, ok := s.notifyFile()
	if !ok {
		return notify.Config{}, false
	}
	c, err := f.Load()
	return c, err == nil
}

func (s *Server) notifications(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	if s.Notifier == nil {
		writeJSON(w, []notify.Notification{})
		return
	}
	writeJSON(w, s.Notifier.Feed(after))
}

func (s *Server) notifyConfig(w http.ResponseWriter, r *http.Request) {
	f, ok := s.notifyFile()
	if !ok {
		httpError(w, http.StatusNotImplemented, "this dashboard has no home directory")
		return
	}
	c, err := f.Load()
	out := map[string]any{"config": c, "path": f.Path, "kinds": []string{notify.KindApproval, notify.KindEscalated, notify.KindFailed,
		notify.KindIntegrated, notify.KindBroken, notify.KindBudget, notify.KindPlan}, "default_kinds": notify.DefaultKinds}
	if err != nil {
		out["error"] = err.Error()
	}
	writeJSON(w, out)
}

func (s *Server) putNotifyConfig(w http.ResponseWriter, r *http.Request) {
	f, ok := s.notifyFile()
	if !ok {
		httpError(w, http.StatusNotImplemented, "this dashboard has no home directory")
		return
	}
	var c notify.Config
	if !decode(w, r, &c) {
		return
	}
	note(r, "", "", "notification settings")
	if err := f.Save(c); err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) testNotify(w http.ResponseWriter, r *http.Request) {
	c, ok := s.notifyCfg()
	if !ok || s.Notifier == nil {
		httpError(w, http.StatusNotImplemented, "notifications are not configured")
		return
	}
	errs := s.Notifier.Deliver(r.Context(), c, notify.Notification{Kind: "test", Title: "Test notification from Spine", Message: "sent by " + by(r), At: time.Now().UTC()})
	writeJSON(w, map[string]any{"errors": errs})
}

// Start runs background work: the notifier's watch loop. It returns when
// ctx ends.
func (s *Server) Start(ctx context.Context) {
	if s.Notifier == nil {
		return
	}
	if s.Notifier.Config == nil {
		s.Notifier.Config = func() notify.Config { c, _ := s.notifyCfg(); return c }
	}
	tk := time.NewTicker(3 * time.Second)
	defer tk.Stop()
	for {
		s.observe(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
		}
	}
}

func (s *Server) observe(ctx context.Context) {
	ov := s.overview(ctx)
	st := notify.State{}
	for _, t := range ov.Tasks {
		st.Tasks = append(st.Tasks, notify.TaskState{Repo: t.Repo, ID: string(t.ID), Title: t.Title, Phase: string(t.Phase),
			Reason: t.Reason, Kind: t.Kind, Tokens: t.Tokens, MaxTokens: t.MaxTokens})
	}
	for _, r := range ov.Repos {
		st.Repos = append(st.Repos, notify.RepoState{Name: r.Name, State: r.State, Error: r.Error})
	}
	if c, ok := s.notifyCfg(); ok && (c.Budgets.DailyTokens > 0 || c.Budgets.DailyCostUSD > 0) {
		day := time.Now().UTC().Truncate(24 * time.Hour)
		for _, r := range ov.Repos {
			if r.Store == nil {
				continue
			}
			evs, _ := r.Store.ListEventsByKind(ctx, day, 0, "outcome")
			for _, e := range evs {
				var o api.Outcome
				json.Unmarshal(e.Data, &o)
				st.DayTok += o.TokensUsed
				st.DayCost += o.CostUSD
			}
		}
	}
	s.Notifier.Observe(ctx, st)
}

// - Audit -

// AuditEntry records one change made through the dashboard.
type AuditEntry struct {
	At            time.Time `json:"at"`
	User          string    `json:"user"`
	Role          string    `json:"role"`
	Authenticated bool      `json:"authenticated"`
	Remote        string    `json:"remote"`
	Method        string    `json:"method"`
	Path          string    `json:"path"`
	Status        int       `json:"status"`
	Repo          string    `json:"repo,omitempty"`
	Task          string    `json:"task,omitempty"`
	Detail        string    `json:"detail,omitempty"`
}

func (s *Server) auditPath() string {
	if s.Home == "" {
		return ""
	}
	return filepath.Join(s.Home, "audit.log")
}

func (s *Server) audit(e AuditEntry) {
	p := s.auditPath()
	if p == "" {
		return
	}
	b, _ := json.Marshal(e)
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		s.logf("audit: %v", err)
		return
	}
	f.Write(append(b, '\n'))
	f.Close()
}

// auditLog returns the newest entries first (?limit=, ?user=, ?repo=).
func (s *Server) auditLog(w http.ResponseWriter, r *http.Request) {
	out := []AuditEntry{}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 5000 {
		limit = 500
	}
	p := s.auditPath()
	if p == "" {
		writeJSON(w, map[string]any{"entries": out, "path": ""})
		return
	}
	f, err := os.Open(p)
	if err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			var e AuditEntry
			if json.Unmarshal(sc.Bytes(), &e) != nil {
				continue
			}
			if u := r.URL.Query().Get("user"); u != "" && e.User != u {
				continue
			}
			if rp := r.URL.Query().Get("repo"); rp != "" && e.Repo != rp {
				continue
			}
			out = append(out, e)
		}
		f.Close()
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	if len(out) > limit {
		out = out[:limit]
	}
	writeJSON(w, map[string]any{"entries": out, "path": p})
}
