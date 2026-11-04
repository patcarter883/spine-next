// Command fakepi is a scripted stand-in for the Pi coding agent's RPC mode
// (`pi --mode rpc`), for tests. It answers --version, records its argv, and
// plays a plan: FAKE_PI_PLAN is {"<task or *>": Step}. A continuation
// prompt (a second prompt in one process) plays Step.Then. Every command type
// received is logged to `commands`. get_state reports the model from --provider
// and --model (a provider/id --model is split on the first slash), or null
// without one. When FAKE_PI_CHECK_MODELS is set, a model not listed in
// $PI_CODING_AGENT_DIR/models.json makes it print `Model <provider>/<id> not
// found` to stderr and exit 1 at startup.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Step struct {
	Turns        int               `json:"turns"` // tool turns before the final answer
	Write        map[string]string `json:"write"`
	HangTool     bool              `json:"hang_tool"`    // start a bash call that never ends
	IgnoreAbort  bool              `json:"ignore_abort"` // keep hanging after abort
	Silent       bool              `json:"silent"`       // emit nothing after accepting the prompt
	Stop         string            `json:"stop"`         // final stopReason (default "stop")
	ErrorMessage string            `json:"error_message"`
	RetryFail    bool              `json:"retry_fail"`
	Tokens       int64             `json:"tokens"` // per assistant message
	Then         *Step             `json:"then"`   // continuation prompt
}

var (
	wmu     sync.Mutex
	out     = bufio.NewWriter(os.Stdout)
	aborted atomic.Bool
	abortCh = make(chan struct{}, 1)
	tokens  atomic.Int64
	cwd, _  = os.Getwd()
	logDir  = os.Getenv("FAKE_PI_LOG")
)

func emit(v any) {
	b, _ := json.Marshal(v)
	wmu.Lock()
	out.Write(b)
	out.WriteByte('\n')
	out.Flush()
	wmu.Unlock()
}

func respond(id, cmd string, data any) {
	emit(map[string]any{"id": id, "type": "response", "command": cmd, "success": true, "data": data})
}

func logf(name, s string) {
	if logDir == "" {
		return
	}
	os.MkdirAll(logDir, 0o755)
	f, _ := os.OpenFile(filepath.Join(logDir, name), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	fmt.Fprintln(f, s)
	f.Close()
}

var (
	reTask  = regexp.MustCompile(`(?m)^spine-task: (\S+)`)
	reRound = regexp.MustCompile(`(?m)^spine-round: (\d+)`)
)

func plan(prompt string) Step {
	m := map[string]Step{}
	if b, err := os.ReadFile(os.Getenv("FAKE_PI_PLAN")); err == nil {
		json.Unmarshal(b, &m)
	}
	if t := reTask.FindStringSubmatch(prompt); t != nil {
		if r := reRound.FindStringSubmatch(prompt); r != nil {
			if s, ok := m[t[1]+"/r"+r[1]]; ok {
				return s
			}
		}
		if s, ok := m[t[1]]; ok {
			return s
		}
	}
	return m["*"]
}

func usage(n int64) map[string]any {
	tokens.Add(n)
	return map[string]any{"input": n / 2, "output": n - n/2, "cacheRead": 0, "cacheWrite": 0, "totalTokens": n,
		"cost": map[string]any{"total": float64(n) / 1e6}}
}

func assistant(stop, text string, n int64, errMsg string) map[string]any {
	m := map[string]any{"role": "assistant", "stopReason": stop, "content": []any{map[string]any{"type": "text", "text": text}}, "usage": usage(n)}
	if errMsg != "" {
		m["errorMessage"] = errMsg
	}
	return m
}

func run(s Step) {
	aborted.Store(false)
	emit(map[string]any{"type": "agent_start"})
	if s.Silent {
		<-abortCh
		emit(map[string]any{"type": "agent_settled", "aborted": true})
		return
	}
	finish := func(stop, text, errMsg string) {
		emit(map[string]any{"type": "message_end", "message": assistant(stop, text, s.Tokens, errMsg)})
		emit(map[string]any{"type": "agent_end", "willRetry": false})
		emit(map[string]any{"type": "agent_settled", "aborted": stop == "aborted"})
	}
	turns := s.Turns
	if turns == 0 && len(s.Write) > 0 {
		turns = 1
	}
	for i := 0; i < turns; i++ {
		if aborted.Load() {
			finish("aborted", "", "")
			return
		}
		emit(map[string]any{"type": "turn_start"})
		emit(map[string]any{"type": "message_end", "message": assistant("toolUse", "", s.Tokens, "")})
		id := fmt.Sprintf("call_%d", i)
		if i == 0 {
			for p, c := range s.Write {
				emit(map[string]any{"type": "tool_execution_start", "toolCallId": id + p, "toolName": "write", "args": map[string]any{"path": p}})
				full := filepath.Join(cwd, p)
				os.MkdirAll(filepath.Dir(full), 0o755)
				os.WriteFile(full, []byte(c), 0o644)
				emit(map[string]any{"type": "tool_execution_end", "toolCallId": id + p, "toolName": "write", "isError": false})
			}
		} else {
			emit(map[string]any{"type": "tool_execution_start", "toolCallId": id, "toolName": "read", "args": map[string]any{"path": "x"}})
			emit(map[string]any{"type": "tool_execution_end", "toolCallId": id, "toolName": "read", "isError": false})
		}
		emit(map[string]any{"type": "turn_end"})
		time.Sleep(20 * time.Millisecond)
	}
	if s.HangTool {
		emit(map[string]any{"type": "turn_start"})
		emit(map[string]any{"type": "tool_execution_start", "toolCallId": "hang", "toolName": "bash", "args": map[string]any{"command": "npm run dev"}})
		for {
			<-abortCh
			if !s.IgnoreAbort {
				break
			}
		}
		emit(map[string]any{"type": "tool_execution_end", "toolCallId": "hang", "toolName": "bash", "isError": true})
		finish("aborted", "", "")
		return
	}
	if aborted.Load() {
		finish("aborted", "", "")
		return
	}
	if s.RetryFail {
		emit(map[string]any{"type": "auto_retry_end", "success": false, "attempt": 3, "finalError": "connection refused"})
	}
	stop := s.Stop
	if stop == "" {
		stop = "stop"
	}
	emit(map[string]any{"type": "turn_start"})
	finish(stop, "Done.", s.ErrorMessage)
}

// modelArgs reads the configured model from the command line.
func modelArgs(args []string) (provider, id string) {
	for i := 0; i+1 < len(args); i++ {
		switch args[i] {
		case "--provider":
			provider = args[i+1]
		case "--model":
			id = args[i+1]
		}
	}
	if p, rest, ok := strings.Cut(id, "/"); ok {
		provider, id = p, rest
	}
	return provider, id
}

// modelListed reports whether models.json in the agent dir lists the model.
func modelListed(provider, id string) bool {
	var cfg struct {
		Providers map[string]struct {
			Models []struct {
				ID string `json:"id"`
			} `json:"models"`
		} `json:"providers"`
	}
	b, _ := os.ReadFile(filepath.Join(os.Getenv("PI_CODING_AGENT_DIR"), "models.json"))
	json.Unmarshal(b, &cfg)
	for _, m := range cfg.Providers[provider].Models {
		if m.ID == id {
			return true
		}
	}
	return false
}

func main() {
	for _, a := range os.Args[1:] {
		if a == "--version" {
			v := os.Getenv("FAKE_PI_VERSION")
			if v == "" {
				v = "1.5.0"
			}
			fmt.Println(v)
			return
		}
	}
	logf("argv", strings.Join(os.Args[1:], " "))
	provider, modelID := modelArgs(os.Args[1:])
	if os.Getenv("FAKE_PI_CHECK_MODELS") != "" && modelID != "" && !modelListed(provider, modelID) {
		fmt.Fprintf(os.Stderr, "Model %s/%s not found\n", provider, modelID)
		os.Exit(1)
	}
	var cur Step
	prompts := 0
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		var c struct {
			ID      string `json:"id"`
			Type    string `json:"type"`
			Message string `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &c) != nil {
			emit(map[string]any{"type": "response", "command": "parse", "success": false, "error": "bad json"})
			continue
		}
		logf("commands", c.Type)
		switch c.Type {
		case "get_state":
			var model any
			if modelID != "" {
				model = map[string]any{"provider": provider, "id": modelID}
			}
			respond(c.ID, "get_state", map[string]any{"model": model})
		case "prompt":
			prompts++
			logf("prompts", fmt.Sprintf("=== prompt %d\n%s", prompts, c.Message))
			if prompts == 1 {
				cur = plan(c.Message)
			} else if cur.Then != nil {
				cur = *cur.Then
			} else {
				cur = Step{}
			}
			respond(c.ID, "prompt", map[string]any{"disposition": "started"})
			go run(cur)
		case "steer":
			logf("steers", c.Message)
			respond(c.ID, "steer", map[string]any{"disposition": "queued"})
		case "abort":
			logf("aborts", "abort")
			aborted.Store(true)
			select {
			case abortCh <- struct{}{}:
			default:
			}
			respond(c.ID, "abort", nil)
		case "get_session_stats":
			respond(c.ID, "get_session_stats", map[string]any{"tokens": map[string]any{"total": tokens.Load()}, "cost": float64(tokens.Load()) / 1e6})
		default:
			emit(map[string]any{"id": c.ID, "type": "response", "command": c.Type, "success": false, "error": "unsupported in fakepi"})
		}
	}
}
