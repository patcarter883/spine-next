// Package notify tells people when something needs them: a task waiting
// for approval, an escalation, a failure, a broken repository, or a token
// or cost budget crossing its threshold. It watches successive dashboard
// states, so it works the same for `spine ui` and `spine supervisor`, and
// delivers to webhooks (Slack-compatible or generic JSON), email (SMTP) and
// the desktop (notify-send / osascript). The dashboard also shows the feed
// and raises browser notifications from it.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/smtp"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// Kinds of notification.
const (
	KindApproval   = "approval"   // a verified task waits for approval
	KindEscalated  = "escalated"  // a task needs a decision
	KindFailed     = "failed"     // a task failed
	KindIntegrated = "integrated" // a task landed (off by default)
	KindBroken     = "broken"     // a repository's controller is broken
	KindBudget     = "budget"     // a budget threshold was crossed
	KindPlan       = "plan"       // a plan is ready for review
)

// DefaultKinds are delivered when Config.Events is empty.
var DefaultKinds = []string{KindApproval, KindEscalated, KindFailed, KindBroken, KindBudget, KindPlan}

// Webhook is one HTTP endpoint.
type Webhook struct {
	URL    string `yaml:"url" json:"url"`
	Format string `yaml:"format,omitempty" json:"format,omitempty"` // slack (default) | generic
}

// Email is an SMTP destination.
type Email struct {
	Host        string   `yaml:"host" json:"host"`
	Port        int      `yaml:"port,omitempty" json:"port,omitempty"`
	Username    string   `yaml:"username,omitempty" json:"username,omitempty"`
	PasswordEnv string   `yaml:"password_env,omitempty" json:"password_env,omitempty"`
	From        string   `yaml:"from" json:"from"`
	To          []string `yaml:"to" json:"to"`
}

// Budgets are thresholds that raise KindBudget.
type Budgets struct {
	DailyTokens  int64   `yaml:"daily_tokens,omitempty" json:"daily_tokens,omitempty"`
	DailyCostUSD float64 `yaml:"daily_cost_usd,omitempty" json:"daily_cost_usd,omitempty"`
	// TaskPercent warns when a task has used this share of its token
	// budget (default 80; 0 keeps the default, negative disables).
	TaskPercent int `yaml:"task_percent,omitempty" json:"task_percent,omitempty"`
}

// Config is Home()/notify.yaml.
type Config struct {
	Desktop      bool      `yaml:"desktop,omitempty" json:"desktop"`
	Webhooks     []Webhook `yaml:"webhooks,omitempty" json:"webhooks"`
	Email        *Email    `yaml:"email,omitempty" json:"email,omitempty"`
	Events       []string  `yaml:"events,omitempty" json:"events"`
	Budgets      Budgets   `yaml:"budgets,omitempty" json:"budgets"`
	DashboardURL string    `yaml:"dashboard_url,omitempty" json:"dashboard_url,omitempty"`
}

// Validate checks a config.
func (c Config) Validate() error {
	for _, w := range c.Webhooks {
		if !strings.HasPrefix(w.URL, "https://") && !strings.HasPrefix(w.URL, "http://") {
			return fmt.Errorf("webhook url %q must be http(s)", w.URL)
		}
		if w.Format != "" && w.Format != "slack" && w.Format != "generic" {
			return fmt.Errorf("webhook format must be slack or generic")
		}
	}
	if e := c.Email; e != nil && (e.Host == "" || e.From == "" || len(e.To) == 0) {
		return fmt.Errorf("email needs host, from and to")
	}
	known := map[string]bool{KindApproval: true, KindEscalated: true, KindFailed: true, KindIntegrated: true, KindBroken: true, KindBudget: true, KindPlan: true}
	for _, k := range c.Events {
		if !known[k] {
			return fmt.Errorf("unknown event %q", k)
		}
	}
	return nil
}

func (c Config) wants(kind string) bool {
	ks := c.Events
	if len(ks) == 0 {
		ks = DefaultKinds
	}
	for _, k := range ks {
		if k == kind {
			return true
		}
	}
	return false
}

// File is the config file.
type File struct{ Path string }

// Load reads the config (absent = defaults).
func (f File) Load() (Config, error) {
	b, err := os.ReadFile(f.Path)
	if os.IsNotExist(err) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, err
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return Config{}, fmt.Errorf("%s: %w", f.Path, err)
	}
	return c, c.Validate()
}

// Save writes the config (0600: it may name credentials' variables).
func (f File) Save(c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	b, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	tmp := f.Path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, f.Path)
}

// TaskState is one task in a State.
type TaskState struct {
	Repo, ID, Title, Phase, Reason, Kind string
	Tokens, MaxTokens                    int64
}

// RepoState is one repository in a State.
type RepoState struct{ Name, State, Error string }

// State is what the notifier compares between observations.
type State struct {
	Tasks   []TaskState
	Repos   []RepoState
	DayTok  int64
	DayCost float64
}

// Notification is one entry of the feed.
type Notification struct {
	Seq     int64     `json:"seq"`
	At      time.Time `json:"at"`
	Kind    string    `json:"kind"`
	Repo    string    `json:"repo,omitempty"`
	Task    string    `json:"task,omitempty"`
	Title   string    `json:"title"`
	Message string    `json:"message,omitempty"`
	// Errors lists deliveries that failed.
	Errors []string `json:"errors,omitempty"`
}

// Notifier detects changes and delivers notifications.
type Notifier struct {
	Config func() Config
	Logf   func(string, ...any)
	// Client sends webhooks (default: 10s timeout).
	Client *http.Client

	mu     sync.Mutex
	seeded bool
	tasks  map[string]string // repo/id -> phase/reason
	repos  map[string]string
	budget map[string]bool // thresholds already raised (by key)
	feed   []Notification
	seq    int64
}

func (n *Notifier) cfg() Config {
	if n.Config == nil {
		return Config{}
	}
	return n.Config()
}

// Observe compares s with the previous state and notifies about changes.
// The first observation only records the state: nothing that was already
// waiting at startup is announced again.
func (n *Notifier) Observe(ctx context.Context, s State) {
	n.mu.Lock()
	first := !n.seeded
	if first {
		n.seeded, n.tasks, n.repos, n.budget = true, map[string]string{}, map[string]string{}, map[string]bool{}
	}
	var out []Notification
	add := func(kind, repo, task, title, msg string) {
		if !first {
			out = append(out, Notification{Kind: kind, Repo: repo, Task: task, Title: title, Message: msg})
		}
	}
	cfg := n.cfg()
	for _, t := range s.Tasks {
		key := t.Repo + "/" + t.ID
		cur := t.Phase + "/" + t.Reason
		prev, seen := n.tasks[key]
		n.tasks[key] = cur
		label := t.Title
		if label == "" {
			label = t.ID
		}
		if seen && prev != cur || !seen && !first {
			switch {
			case t.Phase == "Escalated" && t.Reason == "AwaitingApproval":
				add(KindApproval, t.Repo, t.ID, "Approval needed: "+label, "Verified; review the diff and approve to integrate.")
			case t.Phase == "Escalated" && (t.Reason == "PlanReady" || t.Reason == "PlanInvalid"):
				add(KindPlan, t.Repo, t.ID, "Plan ready: "+label, t.Reason)
			case t.Phase == "Escalated":
				add(KindEscalated, t.Repo, t.ID, "Needs you: "+label, t.Reason)
			case t.Phase == "Failed":
				add(KindFailed, t.Repo, t.ID, "Failed: "+label, t.Reason)
			case t.Phase == "Integrated" && seen:
				add(KindIntegrated, t.Repo, t.ID, "Integrated: "+label, "")
			}
		}
		pct := cfg.Budgets.TaskPercent
		if pct == 0 {
			pct = 80
		}
		if pct > 0 && t.MaxTokens > 0 && t.Tokens*100 >= t.MaxTokens*int64(pct) {
			bk := "task/" + key
			if !n.budget[bk] {
				n.budget[bk] = true
				add(KindBudget, t.Repo, t.ID, fmt.Sprintf("Token budget %d%% used: %s", t.Tokens*100/t.MaxTokens, label),
					fmt.Sprintf("%d of %d tokens", t.Tokens, t.MaxTokens))
			}
		}
	}
	for _, r := range s.Repos {
		prev := n.repos[r.Name]
		n.repos[r.Name] = r.State
		if r.State == "broken" && prev != "broken" {
			add(KindBroken, r.Name, "", "Repository broken: "+r.Name, firstLine(r.Error))
		}
	}
	day := time.Now().UTC().Format("2006-01-02")
	if b := cfg.Budgets.DailyTokens; b > 0 && s.DayTok >= b && !n.budget["tok/"+day] {
		n.budget["tok/"+day] = true
		add(KindBudget, "", "", "Daily token budget reached", fmt.Sprintf("%d tokens today (budget %d)", s.DayTok, b))
	}
	if b := cfg.Budgets.DailyCostUSD; b > 0 && s.DayCost >= b && !n.budget["cost/"+day] {
		n.budget["cost/"+day] = true
		add(KindBudget, "", "", "Daily cost budget reached", fmt.Sprintf("$%.2f today (budget $%.2f)", s.DayCost, b))
	}
	var deliver []int
	for _, x := range out {
		n.seq++
		x.Seq, x.At = n.seq, time.Now().UTC()
		n.feed = append(n.feed, x)
		if cfg.wants(x.Kind) {
			deliver = append(deliver, len(n.feed)-1)
		}
	}
	if len(n.feed) > 200 {
		drop := len(n.feed) - 200
		n.feed = n.feed[drop:]
		for i := range deliver {
			deliver[i] -= drop
		}
	}
	toSend := make([]Notification, 0, len(deliver))
	for _, i := range deliver {
		if i >= 0 {
			toSend = append(toSend, n.feed[i])
		}
	}
	n.mu.Unlock()
	for _, x := range toSend {
		if errs := n.Deliver(ctx, cfg, x); len(errs) > 0 {
			n.mu.Lock()
			for i := range n.feed {
				if n.feed[i].Seq == x.Seq {
					n.feed[i].Errors = errs
				}
			}
			n.mu.Unlock()
		}
	}
}

// Feed returns notifications after seq, oldest first.
func (n *Notifier) Feed(after int64) []Notification {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := []Notification{}
	for _, x := range n.feed {
		if x.Seq > after {
			out = append(out, x)
		}
	}
	return out
}

// Deliver sends x to every configured sink and returns failures.
func (n *Notifier) Deliver(ctx context.Context, c Config, x Notification) []string {
	var errs []string
	text := x.Title
	if x.Message != "" {
		text += "\n" + x.Message
	}
	if x.Repo != "" {
		text = "[" + x.Repo + "] " + text
	}
	link := ""
	if c.DashboardURL != "" {
		link = strings.TrimRight(c.DashboardURL, "/") + "/"
		if x.Task != "" {
			link += "#task=" + x.Repo + "/" + x.Task
		}
		text += "\n" + link
	}
	cl := n.Client
	if cl == nil {
		cl = &http.Client{Timeout: 10 * time.Second}
	}
	for _, w := range c.Webhooks {
		var body any = map[string]string{"text": text}
		if w.Format == "generic" {
			body = map[string]any{"kind": x.Kind, "repo": x.Repo, "task": x.Task, "title": x.Title, "message": x.Message, "at": x.At, "url": link}
		}
		b, _ := json.Marshal(body)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(b))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
			var resp *http.Response
			if resp, err = cl.Do(req); err == nil {
				resp.Body.Close()
				if resp.StatusCode >= 300 {
					err = fmt.Errorf("HTTP %d", resp.StatusCode)
				}
			}
		}
		if err != nil {
			errs = append(errs, "webhook "+redact(w.URL)+": "+err.Error())
		}
	}
	if e := c.Email; e != nil {
		if err := sendMail(*e, x.Title, text); err != nil {
			errs = append(errs, "email: "+err.Error())
		}
	}
	if c.Desktop {
		if err := desktop(ctx, x.Title, x.Message); err != nil {
			errs = append(errs, "desktop: "+err.Error())
		}
	}
	if len(errs) > 0 && n.Logf != nil {
		n.Logf("notify: %s", strings.Join(errs, "; "))
	}
	return errs
}

func redact(u string) string {
	if i := strings.Index(u, "://"); i >= 0 {
		rest := u[i+3:]
		if j := strings.Index(rest, "/"); j >= 0 {
			return u[:i+3] + rest[:j] + "/…"
		}
	}
	return u
}

func sendMail(e Email, subject, body string) error {
	port := e.Port
	if port == 0 {
		port = 587
	}
	var auth smtp.Auth
	if e.Username != "" {
		auth = smtp.PlainAuth("", e.Username, os.Getenv(e.PasswordEnv), e.Host)
	}
	msg := "From: " + e.From + "\r\nTo: " + strings.Join(e.To, ", ") + "\r\nSubject: " + strings.ReplaceAll(subject, "\n", " ") +
		"\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" + strings.ReplaceAll(body, "\n", "\r\n") + "\r\n"
	return smtp.SendMail(e.Host+":"+strconv.Itoa(port), auth, e.From, e.To, []byte(msg))
}

func desktop(ctx context.Context, title, msg string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	switch runtime.GOOS {
	case "darwin":
		script := fmt.Sprintf("display notification %q with title %q", msg, "Spine: "+title)
		return exec.CommandContext(ctx, "osascript", "-e", script).Run()
	default:
		if _, err := exec.LookPath("notify-send"); err != nil {
			return fmt.Errorf("notify-send not installed")
		}
		return exec.CommandContext(ctx, "notify-send", "--app-name=Spine", title, msg).Run()
	}
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	if len(s) > 240 {
		s = s[:240] + "…"
	}
	return s
}
