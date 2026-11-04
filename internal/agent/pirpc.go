package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/patcarter883/spine-next/internal/api"
	"github.com/patcarter883/spine-next/internal/exec"
)

// Pi (the earendil-works pi coding agent) driven through its RPC mode:
// JSONL commands on stdin, responses and session events on stdout
// (docs/rpc.md, rpc-commands.md, json.md in @earendil-works/pi-coding-agent
// 1.5.0). Pi has no sandbox, no approval prompts, no default timeout for
// shell commands and no turn limit, and a failed run can still exit 0, so
// every guardrail is enforced here, from outside the process:
//
//   - the version is pinned (breaking changes land in minor releases);
//   - project-local Pi resources are not trusted (--no-approve, no
//     extensions, skills, prompt templates or MCP), so a worker cannot plant
//     code that a later round loads;
//   - a tool call running past ToolTimeout is aborted and the session is
//     told to continue without it (twice; the third ends the run);
//   - assistant turns, tokens, stalls and wall clock are bounded; near the
//     time limit Pi is steered to wrap up;
//   - success is read from the final assistant message's stopReason and
//     agent_settled, never from the exit code.

// DefaultPiFlags are always passed in front of the adapter's Args.
var DefaultPiFlags = []string{"--mode", "rpc", "--no-session", "--no-approve",
	"--no-extensions", "--no-skills", "--no-prompt-templates", "--no-themes", "--no-mcp"}

// PiAgentDirEnv is Pi's config-directory variable (models.json, settings).
const PiAgentDirEnv = "PI_CODING_AGENT_DIR"

type piRecord struct {
	Type    string          `json:"type"`
	ID      string          `json:"id,omitempty"`
	Command string          `json:"command,omitempty"`
	Success *bool           `json:"success,omitempty"`
	Error   string          `json:"error,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
	raw     []byte
}

type piConn struct {
	w       io.Writer
	wmu     sync.Mutex
	mu      sync.Mutex
	next    int
	pending map[string]chan piRecord
	events  chan piRecord
	done    chan struct{}
}

func newPiConn(w io.Writer) *piConn {
	return &piConn{w: w, pending: map[string]chan piRecord{}, events: make(chan piRecord, 4096), done: make(chan struct{})}
}

// serve reads strict JSONL (split on LF only: U+2028/2029 are valid inside
// JSON strings) and routes responses to callers and the rest to events.
func (c *piConn) serve(r io.Reader) {
	defer close(c.done)
	br := bufio.NewReaderSize(r, 1<<20)
	for {
		line, err := br.ReadBytes('\n')
		line = bytes.TrimSuffix(bytes.TrimSuffix(line, []byte("\n")), []byte("\r"))
		if len(line) > 0 {
			var rec piRecord
			if json.Unmarshal(line, &rec) == nil && rec.Type != "" {
				rec.raw = line
				if rec.Type == "response" && rec.ID != "" {
					c.mu.Lock()
					ch := c.pending[rec.ID]
					delete(c.pending, rec.ID)
					c.mu.Unlock()
					if ch != nil {
						ch <- rec
						continue
					}
				}
				select {
				case c.events <- rec:
				case <-time.After(30 * time.Second): // consumer gone; keep draining stdout
				}
			}
		}
		if err != nil {
			return
		}
	}
}

func (c *piConn) send(v map[string]any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err = c.w.Write(append(b, '\n'))
	return err
}

// call sends a command and waits for its response.
func (c *piConn) call(ctx context.Context, typ string, fields map[string]any) (piRecord, error) {
	c.mu.Lock()
	c.next++
	id := fmt.Sprintf("spine-%d", c.next)
	ch := make(chan piRecord, 1)
	c.pending[id] = ch
	c.mu.Unlock()
	msg := map[string]any{"id": id, "type": typ}
	for k, v := range fields {
		msg[k] = v
	}
	if err := c.send(msg); err != nil {
		return piRecord{}, err
	}
	select {
	case r := <-ch:
		if r.Success != nil && !*r.Success {
			return r, fmt.Errorf("pi %s: %s", typ, r.Error)
		}
		return r, nil
	case <-c.done:
		return piRecord{}, errors.New("pi exited")
	case <-ctx.Done():
		return piRecord{}, ctx.Err()
	}
}

type piUsage struct {
	TotalTokens int64 `json:"totalTokens"`
	Cost        struct {
		Total float64 `json:"total"`
	} `json:"cost"`
}

type piMessage struct {
	Role         string          `json:"role"`
	StopReason   string          `json:"stopReason"`
	ErrorMessage string          `json:"errorMessage"`
	Usage        *piUsage        `json:"usage"`
	Content      json.RawMessage `json:"content"`
}

func (m piMessage) text() string {
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(m.Content, &blocks) != nil {
		var s string
		json.Unmarshal(m.Content, &s)
		return s
	}
	var b strings.Builder
	for _, x := range blocks {
		if x.Type == "text" {
			b.WriteString(x.Text)
		}
	}
	return b.String()
}

type piEvent struct {
	Type         string          `json:"type"`
	Message      *piMessage      `json:"message"`
	ToolCallID   string          `json:"toolCallId"`
	ToolName     string          `json:"toolName"`
	Args         json.RawMessage `json:"args"`
	IsError      bool            `json:"isError"`
	Aborted      bool            `json:"aborted"`
	Success      *bool           `json:"success"`
	FinalError   string          `json:"finalError"`
	Reason       string          `json:"reason"`
	ErrorMessage string          `json:"errorMessage"`
}

// CheckPiVersion verifies the adapter's pinned Pi version.
func (r *Runner) CheckPiVersion(ctx context.Context, a Adapter) error {
	return r.checkPiVersion(ctx, a)
}

// versionCache remembers verified versions per command.
var versionCache sync.Map

// checkPiVersion runs `<agent> --version` (inside the container when
// configured) and compares it with the pinned version.
func (r *Runner) checkPiVersion(ctx context.Context, a Adapter) error {
	if a.Version == "" {
		return fmt.Errorf("agent %q: pi-rpc adapters must pin `version` (Pi ships breaking changes in minor releases)", a.Name)
	}
	argv := []string{a.Argv[0], "--version"}
	key := strings.Join(a.Argv, "\x00")
	if p, err := osexec.LookPath(a.Argv[0]); err == nil {
		if fi, err := os.Stat(p); err == nil { // an upgraded binary is re-checked
			key += fmt.Sprintf("\x00%d\x00%d", fi.Size(), fi.ModTime().UnixNano())
		}
	}
	if a.Container != nil {
		key += "\x00" + a.Container.Image
		argv = append([]string{a.Container.runtime(), "run", "--rm", "--network", "none", a.Container.Image}, argv...)
	}
	if v, ok := versionCache.Load(key); ok && v == a.Version {
		return nil
	}
	vctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	env := buildEnv(a)
	if a.Container != nil {
		env = cliEnv()
	}
	res, err := exec.Run(vctx, exec.Cmd{Argv: argv, Env: env})
	if err != nil {
		return fmt.Errorf("agent %q: %w", a.Name, err)
	}
	got := strings.TrimPrefix(strings.TrimSpace(string(res.Stdout)), "v")
	if !res.OK() || got == "" {
		return fmt.Errorf("agent %q: `%s` failed: %s", a.Name, strings.Join(argv, " "), trunc(strings.TrimSpace(string(res.Stderr)), 300))
	}
	if got != strings.TrimPrefix(a.Version, "v") {
		return fmt.Errorf("agent %q: found pi %s, but the adapter pins %s", a.Name, got, a.Version)
	}
	versionCache.Store(key, a.Version)
	return nil
}

func piArgs(a Adapter) []string {
	args := append([]string{}, DefaultPiFlags...)
	if a.Provider != "" {
		args = append(args, "--provider", a.Provider)
	}
	if a.Model != "" {
		args = append(args, "--model", a.Model)
	}
	return append(args, a.Args...)
}

// runPi drives one Pi RPC session for one Spine round.
func (r *Runner) runPi(ctx context.Context, req RunRequest) (api.Outcome, error) {
	start := time.Now()
	a := req.Adapter
	out := api.Outcome{Attempt: req.Prompt.Attempt, Round: req.Prompt.Round}
	if len(a.Argv) == 0 {
		return out, fmt.Errorf("agent: adapter %q has no argv", a.Name)
	}
	if a.StallTimeout <= 0 {
		a.StallTimeout = 15 * time.Minute
	}
	if a.ToolTimeout <= 0 {
		a.ToolTimeout = 10 * time.Minute
	}
	if a.MaxTurns <= 0 {
		a.MaxTurns = 80
	}
	emit := req.Events
	if emit == nil {
		emit = func(Event) {}
	}
	if err := r.checkPiVersion(ctx, a); err != nil {
		return out, err
	}

	argv := append(append([]string{}, a.Argv...), piArgs(a)...)
	env := buildEnv(a)
	var wr *wrapped
	if a.Container != nil {
		var mounts, extra []string
		if dir := piAgentDir(a); dir != "" {
			// A private, writable copy of the model configuration only: Pi
			// writes to its agent dir, and the host's auth.json, sessions
			// and extensions must not enter the container.
			priv, err := privatePiAgentDir(dir)
			if err != nil {
				return out, err
			}
			defer os.RemoveAll(priv)
			mounts = append(mounts, priv+":/spine/pi-agent")
			extra = append(extra, PiAgentDirEnv+"=/spine/pi-agent")
		}
		var err error
		wr, err = a.Container.wrap(RootLabel(r.WorktreeRoot), req.Lease.Path, req.Lease.TaskID, req.Prompt.Attempt, req.Prompt.Round, argv, env, mounts, extra)
		if err != nil {
			return out, err
		}
		defer a.Container.cleanup(wr)
		argv, env = wr.argv, wr.env
	}

	inR, inW, err := os.Pipe()
	if err != nil {
		return out, err
	}
	outR, outW := io.Pipe()
	var stderr tail
	conn := newPiConn(inW)
	runCtx, killRun := context.WithCancel(context.WithoutCancel(ctx))
	defer killRun()
	type procDone struct {
		res exec.Result
		err error
	}
	doneCh := make(chan procDone, 1)
	go func() {
		res, err := r.Procs.Run(runCtx, req.Lease, exec.Cmd{Argv: argv, Dir: req.Lease.Path, Env: env,
			StdinFile: inR, Stdout: outW, Stderr: &stderr, Grace: 5 * time.Second})
		outW.Close()
		doneCh <- procDone{res, err}
	}()
	go conn.serve(outR)

	g := &piGuard{a: a, req: req, conn: conn, emit: emit, start: start, tools: map[string]piTool{}}
	reason, turnErr := g.loop(ctx, killRun)

	// Final accounting from Pi itself (covers compaction and retries).
	if turnErr == nil {
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if resp, err := conn.call(sctx, "get_session_stats", nil); err == nil {
			var st struct {
				Tokens struct {
					Total int64 `json:"total"`
				} `json:"tokens"`
				Cost float64 `json:"cost"`
			}
			if json.Unmarshal(resp.Data, &st) == nil && st.Tokens.Total > 0 {
				g.tokens.Store(st.Tokens.Total)
				g.cost = st.Cost
			}
		}
		cancel()
	}
	inW.Close() // orderly shutdown
	var pd procDone
	select {
	case pd = <-doneCh:
	case <-time.After(10 * time.Second):
		killRun()
		pd = <-doneCh
	}
	if pd.err != nil {
		return out, pd.err
	}

	out.Duration = time.Since(start)
	out.ExitCode, out.Signal = pd.res.ExitCode, pd.res.Signal
	out.TokensUsed, out.CostUSD, out.Turns = g.tokens.Load(), g.cost, g.turns
	last := g.last
	if last != nil {
		out.StopReason = last.StopReason
		out.Message = truncTail(strings.TrimSpace(last.text()), MaxFinalMessage)
	}
	switch {
	case reason != "":
		out.Reason = reason
		if g.why != "" {
			out.Message = strings.TrimSpace(g.why + "\n" + out.Message)
		}
	case turnErr != nil:
		out.Reason = api.OutcomeCrashed
		out.Message = trunc(strings.TrimSpace(turnErr.Error()+"\nstderr: "+stderr.String()), 2000)
	case last == nil:
		out.Reason = api.OutcomeCrashed
		out.Message = trunc("pi produced no assistant response\nstderr: "+stderr.String(), 2000)
	default:
		switch last.StopReason {
		case "stop":
			out.Reason = api.OutcomeCompleted
		case "length":
			out.Reason = api.OutcomeBudgetExceeded
		case "aborted":
			out.Reason = api.OutcomeCancelled
		default: // "error" and anything unexpected: never treated as success
			out.Reason = api.OutcomeCrashed
			msg := last.ErrorMessage
			if g.retryErr != "" {
				msg = strings.TrimSpace(msg + "\n" + g.retryErr)
			}
			out.Message = trunc(strings.TrimSpace(fmt.Sprintf("pi stopped with %q: %s", last.StopReason, msg)), 2000)
		}
	}
	return out, nil
}

func piAgentDir(a Adapter) string {
	if d, ok := a.Env[PiAgentDirEnv]; ok {
		return d
	}
	if d := os.Getenv(PiAgentDirEnv); d != "" {
		return d
	}
	d := PiAgentDir(a)
	if _, err := os.Stat(d); err != nil {
		return ""
	}
	return d
}

// PiAgentDir is where Pi reads models.json for this adapter, whether or not
// the directory exists yet ("" when the home directory is unknown).
func PiAgentDir(a Adapter) string {
	if d, ok := a.Env[PiAgentDirEnv]; ok {
		return d
	}
	if d := os.Getenv(PiAgentDirEnv); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home + "/.pi/agent"
}

// PiAgentFiles are copied from the host's Pi agent dir into a container
// run's private one.
var PiAgentFiles = []string{"models.json", "settings.json"}

func privatePiAgentDir(src string) (string, error) {
	dst, err := os.MkdirTemp("", "spine-pi-agent-")
	if err != nil {
		return "", err
	}
	for _, f := range PiAgentFiles {
		b, err := os.ReadFile(src + "/" + f)
		if os.IsNotExist(err) {
			continue
		}
		if err == nil {
			err = os.WriteFile(dst+"/"+f, b, 0o600)
		}
		if err != nil {
			os.RemoveAll(dst)
			return "", err
		}
	}
	return dst, nil
}

type piTool struct {
	name  string
	title string
	start time.Time
}

// piGuard consumes events and enforces the guardrails.
type piGuard struct {
	a      Adapter
	req    RunRequest
	conn   *piConn
	emit   func(Event)
	start  time.Time
	tools  map[string]piTool
	turns  int
	tokens atomic.Int64
	cost   float64
	last   *piMessage
	// retryErr is the final auto-retry failure, if any.
	retryErr string
	why      string // guard explanation
	// continuation is sent as a new prompt after an abort that the run
	// should recover from (a timed-out tool call).
	continuation string
	toolTimeouts int
	wrappedUp    bool
	aborting     bool
	abortedAt    time.Time
}

// MaxToolTimeouts is how many timed-out tool calls a run recovers from.
const MaxToolTimeouts = 2

func toolTitle(name string, args json.RawMessage) string {
	var m map[string]any
	json.Unmarshal(args, &m)
	for _, k := range []string{"command", "path", "file_path", "pattern"} {
		if v, ok := m[k].(string); ok && v != "" {
			return trunc(name+": "+v, 200)
		}
	}
	return name
}

func (g *piGuard) abort(reason api.OutcomeReason, why string) (api.OutcomeReason, error) {
	g.why = why
	g.emit(Event{Kind: "guard", Title: "abort", Detail: why})
	g.aborting = true
	g.abortedAt = time.Now()
	go g.conn.call(context.Background(), "abort", nil)
	return reason, nil
}

// loop runs the prompt until Pi settles, returning a guard reason when a
// guardrail ended the run.
func (g *piGuard) loop(ctx context.Context, kill context.CancelFunc) (api.OutcomeReason, error) {
	pctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	resp, err := g.conn.call(pctx, "prompt", map[string]any{"message": g.req.Prompt.Render()})
	cancel()
	if err != nil {
		return "", fmt.Errorf("prompt rejected: %w", err)
	}
	var disp struct {
		Disposition string `json:"disposition"`
	}
	json.Unmarshal(resp.Data, &disp)
	if disp.Disposition == "handled" {
		return "", errors.New("pi handled the prompt without starting a run")
	}
	var reason api.OutcomeReason
	lastRecord := time.Now()
	tk := time.NewTicker(250 * time.Millisecond) // fine enough for the wrap-up window on short timeouts
	defer tk.Stop()
	for {
		select {
		case rec := <-g.conn.events:
			lastRecord = time.Now()
			var ev piEvent
			json.Unmarshal(rec.raw, &ev)
			switch ev.Type {
			case "turn_start":
				g.turns++
				if g.turns > g.a.MaxTurns && reason == "" && !g.aborting {
					reason, _ = g.abort(api.OutcomeBudgetExceeded, fmt.Sprintf("turn limit: more than %d assistant turns", g.a.MaxTurns))
				}
			case "tool_execution_start":
				t := piTool{name: ev.ToolName, title: toolTitle(ev.ToolName, ev.Args), start: time.Now()}
				g.tools[ev.ToolCallID] = t
				g.emit(Event{Kind: "tool_call", Title: t.title})
			case "tool_execution_end":
				t := g.tools[ev.ToolCallID]
				delete(g.tools, ev.ToolCallID)
				st := "completed"
				if ev.IsError {
					st = "failed"
				}
				g.emit(Event{Kind: "tool_done", Title: t.title, Status: st})
			case "message_end":
				if m := ev.Message; m != nil && m.Role == "assistant" {
					g.last = m
					if txt := strings.TrimSpace(m.text()); txt != "" {
						g.emit(Event{Kind: "message", Detail: trunc(txt, 8000)})
					}
					if m.Usage != nil {
						n := g.tokens.Add(m.Usage.TotalTokens)
						g.cost += m.Usage.Cost.Total
						g.emit(Event{Kind: "usage", Tokens: n})
						if g.req.MaxTokens > 0 && n > g.req.MaxTokens && reason == "" && !g.aborting {
							reason, _ = g.abort(api.OutcomeBudgetExceeded, fmt.Sprintf("token budget: %d tokens used, limit %d", n, g.req.MaxTokens))
						}
					}
				}
			case "auto_retry_end":
				if ev.Success != nil && !*ev.Success {
					g.retryErr = "provider retries exhausted: " + ev.FinalError
				}
			case "compaction_start":
				g.emit(Event{Kind: "compaction", Title: "compaction started", Detail: ev.Reason})
			case "agent_settled":
				g.tools = map[string]piTool{}
				if g.continuation != "" && reason == "" {
					// Recover from a timed-out tool call in the same session.
					msg := g.continuation
					g.continuation, g.aborting = "", false
					g.emit(Event{Kind: "guard", Title: "continue", Detail: msg})
					cctx, cancel := context.WithTimeout(ctx, time.Minute)
					_, err := g.conn.call(cctx, "prompt", map[string]any{"message": msg})
					cancel()
					if err != nil {
						return "", fmt.Errorf("continuation rejected: %w", err)
					}
					continue
				}
				return reason, nil
			}
		case <-g.conn.done:
			// Pi exited before settling.
			if reason != "" {
				return reason, nil
			}
			return "", errors.New("pi exited before the run settled")
		case <-tk.C:
			now := time.Now()
			if g.aborting && now.Sub(g.abortedAt) > 20*time.Second {
				kill() // abort was not honoured
				if reason == "" {
					reason = api.OutcomeCrashed
				}
				return reason, nil
			}
			if reason != "" || g.aborting {
				continue
			}
			switch {
			case ctx.Err() != nil:
				reason, _ = g.abort(api.OutcomeCancelled, "cancelled by the controller")
			case g.req.Timeout > 0 && now.Sub(g.start) > g.req.Timeout:
				reason, _ = g.abort(api.OutcomeTimeout, fmt.Sprintf("run timeout: %s", g.req.Timeout))
			case now.Sub(lastRecord) > g.a.StallTimeout:
				reason, _ = g.abort(api.OutcomeStalled, fmt.Sprintf("no activity for %s", g.a.StallTimeout))
			default:
				for _, t := range g.tools {
					if now.Sub(t.start) <= g.a.ToolTimeout {
						continue
					}
					g.toolTimeouts++
					if g.toolTimeouts > MaxToolTimeouts {
						reason, _ = g.abort(api.OutcomeBudgetExceeded, fmt.Sprintf("tool calls timed out %d times (limit %s each)", g.toolTimeouts, g.a.ToolTimeout))
					} else {
						g.abort("", fmt.Sprintf("tool call %q ran longer than %s", t.title, g.a.ToolTimeout))
						g.continuation = fmt.Sprintf("Your last tool call (%s) ran longer than %s and was stopped. Do not run long-running, interactive or watch-mode commands; pass a timeout to shell commands. Continue the task.", t.title, g.a.ToolTimeout)
					}
					break
				}
				if g.req.Timeout > 0 && !g.wrappedUp && now.Sub(g.start) > g.req.Timeout*8/10 {
					g.wrappedUp = true
					g.emit(Event{Kind: "guard", Title: "steer", Detail: "wrap up"})
					go g.conn.call(context.Background(), "steer", map[string]any{"message": "Time is nearly up. Finish the change you are making, make sure the code compiles, and stop."})
				}
			}
		}
	}
}

// ProbePi checks a pi-rpc adapter without spending tokens: the pinned
// version (inside the container when configured) and, for an uncontained
// adapter, that Pi starts in RPC mode in dir and resolves the configured
// model (get_state).
func (r *Runner) ProbePi(ctx context.Context, a Adapter, dir string) (string, error) {
	if err := r.checkPiVersion(ctx, a); err != nil {
		return "", err
	}
	if a.Container != nil {
		return "pi " + a.Version + " in " + a.Container.Image, nil
	}
	inR, inW, err := os.Pipe()
	if err != nil {
		return "", err
	}
	outR, outW := io.Pipe()
	var stderr tail
	pctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	done := make(chan struct{})
	go func() {
		exec.Run(pctx, exec.Cmd{Argv: append(append([]string{}, a.Argv...), piArgs(a)...), Dir: dir, Env: buildEnv(a),
			StdinFile: inR, Stdout: outW, Stderr: &stderr, Grace: 2 * time.Second})
		outW.Close()
		close(done)
	}()
	conn := newPiConn(inW)
	go conn.serve(outR)
	defer func() { inW.Close(); <-done }()
	resp, err := conn.call(pctx, "get_state", nil)
	if err != nil {
		return "", fmt.Errorf("pi rpc: %w (stderr: %s)", err, trunc(strings.TrimSpace(stderr.String()), 400))
	}
	var st struct {
		Model *struct {
			ID       string `json:"id"`
			Provider string `json:"provider"`
		} `json:"model"`
	}
	json.Unmarshal(resp.Data, &st)
	if st.Model == nil {
		return "", fmt.Errorf("pi started but has no model selected (set model: in the adapter, and models.json for a local server)")
	}
	return fmt.Sprintf("pi %s, model %s/%s", a.Version, st.Model.Provider, st.Model.ID), nil
}
