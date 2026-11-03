// Package agent runs coding agents as supervised process groups (RFC §3.4).
// The first adapter speaks ACP (Agent Client Protocol) over stdio, which is
// how Claude Code is driven through @agentclientprotocol/claude-agent-acp.
// Spine only sees the protocol stream; the agent's result is judged by the
// gate engine, never by what the agent says.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/patcarter883/spine-next/internal/acp"
	"github.com/patcarter883/spine-next/internal/api"
	"github.com/patcarter883/spine-next/internal/exec"
	"github.com/patcarter883/spine-next/internal/worktree"
)

// Adapter configures one agent command.
type Adapter struct {
	Name string   `yaml:"name"`
	Argv []string `yaml:"argv"`
	// Env adds variables; PassEnv lists parent variables passed through
	// (credentials, proxies). Nothing else is inherited.
	Env     map[string]string `yaml:"env"`
	PassEnv []string          `yaml:"pass_env"`
	// Mode is requested with session/set_mode when the agent offers it
	// (Claude: "acceptEdits"; empty = leave the agent's default).
	Mode string `yaml:"mode"`
	// Permissions: "worktree" (default) allows tool calls whose locations
	// stay inside the worktree and rejects the rest; "allow" allows all;
	// "reject" rejects all.
	Permissions string `yaml:"permissions"`
	// StallTimeout cancels a run with no protocol traffic for this long.
	StallTimeout time.Duration `yaml:"stall_timeout"`

	// Protocol is "acp" (default; Claude Code and other ACP agents) or
	// "pi-rpc" (the Pi coding agent's RPC mode).
	Protocol string `yaml:"protocol"`
	// Version pins the agent: Spine runs `<argv[0]> --version` before the
	// first run and refuses any other version (required for pi-rpc, whose
	// breaking changes land in minor releases).
	Version string `yaml:"version"`
	// Model and Provider select the model (pi-rpc: --model/--provider).
	Model    string `yaml:"model"`
	Provider string `yaml:"provider"`
	// Args are appended to the agent's command line.
	Args []string `yaml:"args"`
	// ToolTimeout aborts one tool call (shell command) that runs longer;
	// the session continues with a note (pi-rpc). Default 10m.
	ToolTimeout time.Duration `yaml:"tool_timeout"`
	// MaxTurns stops a run after this many assistant turns (pi-rpc).
	// Default 80.
	MaxTurns int `yaml:"max_turns"`
	// Container runs every agent process in a fresh container.
	Container *Container `yaml:"container"`
}

// DefaultPassEnv covers credentials and network settings agents need.
var DefaultPassEnv = []string{
	"PATH", "HOME", "USER", "LANG", "LC_ALL", "TZ", "TERM", "SHELL", "TMPDIR",
	"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "CLAUDE_CODE_OAUTH_TOKEN",
	"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_EXECUTABLE", "ANTHROPIC_MODEL",
	"AWS_REGION", "AWS_PROFILE", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN",
	"CLOUD_ML_REGION", "ANTHROPIC_VERTEX_PROJECT_ID", "GOOGLE_APPLICATION_CREDENTIALS",
	"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "https_proxy", "http_proxy", "no_proxy", "SSL_CERT_FILE", "NODE_EXTRA_CA_CERTS",
	"GOCACHE", "GOMODCACHE", "GOPATH", "GOTOOLCHAIN", "GOFLAGS", "GOPROXY",
}

// ClaudeAdapter is the default: Claude Code over ACP.
func ClaudeAdapter() Adapter {
	return Adapter{Name: "claude", Argv: []string{"claude-agent-acp"}, Mode: "acceptEdits", Permissions: "worktree",
		StallTimeout: 10 * time.Minute}
}

// PiVersion is the Pi release Spine is tested against; the built-in "pi"
// adapter pins it (Pi ships breaking changes in minor releases).
const PiVersion = "1.5.0"

// PiAdapter is the built-in Pi adapter (RPC mode, version pinned). It runs
// on the host unless the configuration gives it a container; `spine doctor`
// warns about that.
func PiAdapter() Adapter {
	return Adapter{Name: "pi", Argv: []string{"pi"}, Protocol: "pi-rpc", Version: PiVersion, Permissions: "worktree",
		StallTimeout: 15 * time.Minute}
}

// ProcRunner runs a command inside a lease, recording its process group
// (worktree.Manager.Run).
type ProcRunner interface {
	Run(ctx context.Context, l *worktree.Lease, c exec.Cmd) (exec.Result, error)
}

// Event is a progress record for the task log.
type Event struct {
	Kind   string `json:"kind"` // tool_call, tool_done, permission, usage, message, stderr
	Title  string `json:"title,omitempty"`
	Status string `json:"status,omitempty"`
	Detail string `json:"detail,omitempty"`
	Tokens int64  `json:"tokens,omitempty"`
}

// RunRequest is one agent run.
type RunRequest struct {
	Lease   *worktree.Lease
	Adapter Adapter
	Prompt  Prompt
	Timeout time.Duration
	// MaxTokens stops the run once reported usage exceeds it (0 = none).
	MaxTokens int64
	Events    func(Event)
}

// Runner runs agents.
type Runner struct {
	Procs ProcRunner
	// WorktreeRoot labels agent containers so a restarted controller can
	// remove the ones it left behind.
	WorktreeRoot string
	// Version is reported to the agent in clientInfo.
	Version string
}

// ErrProtocol is an agent that violates ACP (wrong version, bad handshake).
var ErrProtocol = errors.New("agent: protocol error")

// Run starts the agent, drives one ACP prompt turn and returns the outcome.
// It never returns while the agent's process group is alive. err is non-nil
// only for faults outside the agent (cannot spawn, cannot record the
// process); an agent that crashes or misbehaves is an Outcome.
func (r *Runner) Run(ctx context.Context, req RunRequest) (api.Outcome, error) {
	switch req.Adapter.Protocol {
	case "", "acp":
	case "pi-rpc":
		return r.runPi(ctx, req)
	default:
		return api.Outcome{Attempt: req.Prompt.Attempt, Round: req.Prompt.Round},
			fmt.Errorf("agent: adapter %q: unknown protocol %q", req.Adapter.Name, req.Adapter.Protocol)
	}
	start := time.Now()
	out := api.Outcome{Attempt: req.Prompt.Attempt, Round: req.Prompt.Round}
	a := req.Adapter
	if len(a.Argv) == 0 {
		return out, fmt.Errorf("agent: adapter %q has no argv", a.Name)
	}
	if a.StallTimeout <= 0 {
		a.StallTimeout = 10 * time.Minute
	}
	emit := req.Events
	if emit == nil {
		emit = func(Event) {}
	}
	cwd := req.Lease.Path

	inR, inW, err := os.Pipe()
	if err != nil {
		return out, err
	}
	outR, outW := io.Pipe()
	var stderr tail
	h := &handler{cwd: cwd, policy: a.Permissions, emit: emit}
	conn := acp.NewConn(inW, h)
	var lastActivity atomic.Int64
	lastActivity.Store(time.Now().UnixNano())
	conn.OnActivity = func() { lastActivity.Store(time.Now().UnixNano()) }

	runCtx, killRun := context.WithCancel(context.WithoutCancel(ctx))
	defer killRun()
	type procDone struct {
		res exec.Result
		err error
	}
	doneCh := make(chan procDone, 1)
	argv, env := append(append([]string{}, a.Argv...), a.Args...), buildEnv(a)
	if a.Container != nil {
		wr, err := a.Container.wrap(RootLabel(r.WorktreeRoot), cwd, req.Lease.TaskID, req.Prompt.Attempt, req.Prompt.Round, argv, env, nil, nil)
		if err != nil {
			inR.Close()
			inW.Close()
			return out, err
		}
		defer a.Container.cleanup(wr)
		argv, env = wr.argv, wr.env
	}
	go func() {
		res, err := r.Procs.Run(runCtx, req.Lease, exec.Cmd{
			Argv: argv, Dir: cwd, Env: env, StdinFile: inR, Stdout: outW, Stderr: &stderr,
			Grace: 5 * time.Second,
		})
		outW.Close()
		doneCh <- procDone{res, err}
	}()
	go conn.Serve(runCtx, outR)

	// The turn: handshake and one prompt, bounded by timeout and stalls.
	turnCtx, cancelTurn := context.WithCancel(ctx)
	defer cancelTurn()
	var why atomic.Value // reason the turn was cut short
	if req.Timeout > 0 {
		t := time.AfterFunc(req.Timeout, func() { why.Store(api.OutcomeTimeout); cancelTurn() })
		defer t.Stop()
	}
	stopWatch := make(chan struct{})
	defer close(stopWatch)
	go func() {
		tk := time.NewTicker(time.Second)
		defer tk.Stop()
		for {
			select {
			case <-stopWatch:
				return
			case <-tk.C:
				if time.Since(time.Unix(0, lastActivity.Load())) > a.StallTimeout {
					why.Store(api.OutcomeStalled)
					cancelTurn()
					return
				}
				if req.MaxTokens > 0 && h.tokens.Load() > req.MaxTokens {
					why.Store(api.OutcomeBudgetExceeded)
					cancelTurn()
					return
				}
			}
		}
	}()

	resp, turnErr := r.turn(turnCtx, conn, req, a)
	inW.Close() // EOF on stdin: well-behaved agents exit
	var pd procDone
	select {
	case pd = <-doneCh:
	case <-time.After(10 * time.Second):
		killRun() // SIGTERM → SIGKILL to the whole group
		pd = <-doneCh
	}
	if pd.err != nil {
		return out, pd.err
	}

	out.Duration = time.Since(start)
	out.ExitCode, out.Signal = pd.res.ExitCode, pd.res.Signal
	out.TokensUsed = h.tokens.Load()
	if resp != nil && resp.Usage != nil && resp.Usage.TotalTokens > out.TokensUsed {
		out.TokensUsed = resp.Usage.TotalTokens
	}
	out.Message = h.lastMessage()
	switch {
	case resp != nil:
		out.StopReason = resp.StopReason
		switch resp.StopReason {
		case acp.StopEndTurn:
			out.Reason = api.OutcomeCompleted
		case acp.StopMaxTokens, acp.StopMaxTurnRequests:
			out.Reason = api.OutcomeBudgetExceeded
		case acp.StopRefusal:
			out.Reason = api.OutcomeRefused
		default:
			out.Reason = api.OutcomeCancelled
		}
		if w, ok := why.Load().(api.OutcomeReason); ok {
			out.Reason = w
		}
	case why.Load() != nil:
		out.Reason = why.Load().(api.OutcomeReason)
	case ctx.Err() != nil:
		out.Reason = api.OutcomeCancelled
	default:
		out.Reason = api.OutcomeCrashed
		msg := ""
		if turnErr != nil {
			msg = turnErr.Error()
		}
		if s := strings.TrimSpace(stderr.String()); s != "" {
			msg = strings.TrimSpace(msg + "\nstderr: " + s)
		}
		out.Message = trunc(msg, 2000)
	}
	return out, nil
}

// turn performs the handshake and one prompt. If ctx ends while the prompt
// is running, it sends session/cancel and waits briefly for the agent's
// (cancelled) response, as ACP prescribes, before giving up.
func (r *Runner) turn(ctx context.Context, conn *acp.Conn, req RunRequest, a Adapter) (*acp.PromptResponse, error) {
	var ir acp.InitializeResponse
	ver := r.Version
	if ver == "" {
		ver = "dev"
	}
	if err := conn.Call(ctx, acp.MethodInitialize, acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersion,
		ClientInfo:      &acp.Implementation{Name: "spine", Version: ver},
	}, &ir); err != nil {
		return nil, fmt.Errorf("initialize: %w", err)
	}
	if ir.ProtocolVersion != acp.ProtocolVersion {
		return nil, fmt.Errorf("%w: agent speaks ACP v%d, Spine speaks v%d", ErrProtocol, ir.ProtocolVersion, acp.ProtocolVersion)
	}
	var ns acp.NewSessionResponse
	if err := conn.Call(ctx, acp.MethodSessionNew, acp.NewSessionRequest{Cwd: req.Lease.Path, McpServers: []any{}}, &ns); err != nil {
		return nil, fmt.Errorf("session/new: %w", err)
	}
	if ns.SessionID == "" {
		return nil, fmt.Errorf("%w: empty session id", ErrProtocol)
	}
	if a.Mode != "" && ns.Modes != nil {
		for _, m := range ns.Modes.AvailableModes {
			if m.ID == a.Mode && ns.Modes.CurrentModeID != a.Mode {
				_ = conn.Call(ctx, acp.MethodSessionSetMode, acp.SetModeRequest{SessionID: ns.SessionID, ModeID: a.Mode}, nil)
			}
		}
	}
	type result struct {
		pr  acp.PromptResponse
		err error
	}
	ch := make(chan result, 1)
	callCtx, stopCall := context.WithCancel(context.Background())
	defer stopCall()
	go func() {
		var pr acp.PromptResponse
		err := conn.Call(callCtx, acp.MethodSessionPrompt, acp.PromptRequest{
			SessionID: ns.SessionID,
			Prompt:    []acp.ContentBlock{{Type: "text", Text: req.Prompt.Render()}},
		}, &pr)
		ch <- result{pr, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			return nil, fmt.Errorf("session/prompt: %w", r.err)
		}
		return &r.pr, nil
	case <-ctx.Done():
	}
	conn.Notify(acp.MethodSessionCancel, acp.CancelNotification{SessionID: ns.SessionID})
	select {
	case r := <-ch:
		if r.err == nil {
			return &r.pr, nil // typically stopReason "cancelled"
		}
		return nil, fmt.Errorf("session/prompt: %w", r.err)
	case <-time.After(5 * time.Second):
		return nil, fmt.Errorf("session/prompt: agent did not acknowledge cancel")
	}
}

// handler answers the agent's requests and records its updates.
type handler struct {
	cwd    string
	policy string
	emit   func(Event)
	tokens atomic.Int64
	mu     sync.Mutex
	msg    strings.Builder
}

func (h *handler) lastMessage() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return truncTail(strings.TrimSpace(h.msg.String()), MaxFinalMessage)
}

func (h *handler) Notify(ctx context.Context, method string, params json.RawMessage) {
	if method != acp.MethodSessionUpdate {
		return
	}
	var n acp.SessionNotification
	if json.Unmarshal(params, &n) != nil {
		return
	}
	var u acp.SessionUpdate
	if json.Unmarshal(n.Update, &u) != nil {
		return
	}
	switch u.SessionUpdate {
	case "agent_message_chunk":
		h.mu.Lock()
		if h.msg.Len() > 64<<10 { // keep the tail: the final summary matters
			s := h.msg.String()
			h.msg.Reset()
			h.msg.WriteString(s[len(s)-8<<10:])
		}
		txt := acp.TextOf(u.Content)
		h.msg.WriteString(txt)
		h.mu.Unlock()
		if txt != "" {
			h.emit(Event{Kind: "message", Detail: txt})
		}
	case "tool_call":
		h.emit(Event{Kind: "tool_call", Title: trunc(u.Title, 200), Status: u.Status, Detail: u.Kind})
	case "tool_call_update":
		if u.Status == "completed" || u.Status == "failed" {
			h.emit(Event{Kind: "tool_done", Title: trunc(u.Title, 200), Status: u.Status})
		}
	case "usage_update":
		// "used" is context tokens in use: a lower bound on spend, good
		// enough to stop a runaway turn before PromptResponse.usage arrives.
		for {
			cur := h.tokens.Load()
			if u.Used <= cur || h.tokens.CompareAndSwap(cur, u.Used) {
				break
			}
		}
		h.emit(Event{Kind: "usage", Tokens: u.Used})
	}
}

func (h *handler) Request(ctx context.Context, method string, params json.RawMessage) (any, *acp.RPCError) {
	switch method {
	case acp.MethodRequestPermission:
		var req acp.RequestPermissionRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, &acp.RPCError{Code: acp.CodeInvalidParams, Message: err.Error()}
		}
		allow, why := h.permit(req.ToolCall)
		want := []string{acp.AllowOnce, acp.AllowAlways}
		if !allow {
			want = []string{acp.RejectOnce, acp.RejectAlways}
		}
		h.emit(Event{Kind: "permission", Title: trunc(req.ToolCall.Title, 200), Status: map[bool]string{true: "allowed", false: "rejected"}[allow], Detail: why})
		for _, k := range want {
			for _, o := range req.Options {
				if o.Kind == k {
					return acp.RequestPermissionResponse{Outcome: acp.PermissionOutcome{Outcome: "selected", OptionID: o.OptionID}}, nil
				}
			}
		}
		return acp.RequestPermissionResponse{Outcome: acp.PermissionOutcome{Outcome: "cancelled"}}, nil
	}
	// fs/* and terminal/* are not advertised; anything else is unknown.
	return nil, &acp.RPCError{Code: acp.CodeMethodNotFound, Message: "method not supported by spine: " + method}
}

// permit applies the adapter's permission policy to a tool call.
func (h *handler) permit(tc acp.ToolCallUpdate) (bool, string) {
	switch h.policy {
	case "allow":
		return true, "policy allow"
	case "reject":
		return false, "policy reject"
	}
	for _, l := range tc.Locations {
		p := l.Path
		if !filepath.IsAbs(p) {
			p = filepath.Join(h.cwd, p)
		}
		rel, err := filepath.Rel(h.cwd, filepath.Clean(p))
		if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
			return false, "location outside the worktree: " + l.Path
		}
	}
	return true, "inside worktree"
}

func buildEnv(a Adapter) []string {
	pass := a.PassEnv
	if pass == nil {
		pass = DefaultPassEnv
	}
	var env []string
	for _, k := range pass {
		if strings.HasPrefix(k, "GIT_") {
			continue
		}
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	for k, v := range a.Env {
		env = append(env, k+"="+v)
	}
	return append(env, "SPINE_AGENT=1")
}

// tail keeps the last 16 KiB written.
type tail struct {
	mu  sync.Mutex
	buf []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > 16<<10 {
		t.buf = t.buf[len(t.buf)-16<<10:]
	}
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// MaxFinalMessage bounds the agent's final message kept in an outcome.
const MaxFinalMessage = 8000

// truncTail keeps the last n bytes: an agent's summary comes at the end.
func truncTail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[len(s)-n:]
	for i := 0; i < 4 && i < len(s); i++ { // do not start mid-rune
		if s[i]&0xC0 != 0x80 {
			return "…" + s[i:]
		}
	}
	return "…" + s
}
