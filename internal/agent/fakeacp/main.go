// Command fakeacp is a scripted ACP v1 agent for tests. It speaks the agent
// side of the protocol Spine's client uses and edits files in its session
// cwd according to a plan.
//
// FAKE_ACP_PLAN names a JSON file: {"<task>": {"<key>": Step}} where key is
// "a<attempt>r<round>", "r<round>" or "*" (tried in that order), read from
// the prompt's spine-task / spine-attempt / spine-round header. Task "*"
// applies to any task. FAKE_ACP_LOG, if set, is a directory where every
// received prompt is appended to <task>.log.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Step is what the agent does for one prompt.
type Step struct {
	Write      map[string]string `json:"write"`
	Append     map[string]string `json:"append"`
	Delete     []string          `json:"delete"`
	SleepMS    int               `json:"sleep_ms"`
	Crash      bool              `json:"crash"`      // exit 3 mid-turn
	Hang       bool              `json:"hang"`       // never answer (until cancelled)
	Silent     bool              `json:"silent"`     // with hang: send no traffic at all
	Stop       string            `json:"stop"`       // stopReason (default end_turn)
	Tokens     int64             `json:"tokens"`     // reported usage
	Permission string            `json:"permission"` // ask permission for this path; skip writes if rejected
	GitCommit  bool              `json:"git_commit"` // commit changes (agents sometimes do)
	Message    string            `json:"message"`
}

type msg struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

var (
	wmu       sync.Mutex
	out       = bufio.NewWriter(os.Stdout)
	cwd       string
	cancelled = make(chan struct{}, 1)
	pendingMu sync.Mutex
	pending   = map[string]chan msg{}
	nextID    = 1000
)

func send(m msg) {
	m.JSONRPC = "2.0"
	b, _ := json.Marshal(m)
	wmu.Lock()
	out.Write(b)
	out.WriteByte('\n')
	out.Flush()
	wmu.Unlock()
}

func reply(id json.RawMessage, v any) {
	b, _ := json.Marshal(v)
	send(msg{ID: id, Result: b})
}

func notify(method string, v any) {
	b, _ := json.Marshal(v)
	send(msg{Method: method, Params: b})
}

func call(method string, v any) msg {
	pendingMu.Lock()
	nextID++
	id := json.RawMessage(fmt.Sprint(nextID))
	ch := make(chan msg, 1)
	pending[string(id)] = ch
	pendingMu.Unlock()
	b, _ := json.Marshal(v)
	send(msg{ID: id, Method: method, Params: b})
	return <-ch
}

func update(u map[string]any) {
	notify("session/update", map[string]any{"sessionId": "fake-1", "update": u})
}

var (
	reTask    = regexp.MustCompile(`(?m)^spine-task: (\S+)`)
	reAttempt = regexp.MustCompile(`(?m)^spine-attempt: (\d+)`)
	reRound   = regexp.MustCompile(`(?m)^spine-round: (\d+)`)
)

func step(prompt string) Step {
	task, att, rnd := "", "0", "0"
	if m := reTask.FindStringSubmatch(prompt); m != nil {
		task = m[1]
	}
	if m := reAttempt.FindStringSubmatch(prompt); m != nil {
		att = m[1]
	}
	if m := reRound.FindStringSubmatch(prompt); m != nil {
		rnd = m[1]
	}
	if dir := os.Getenv("FAKE_ACP_LOG"); dir != "" {
		os.MkdirAll(dir, 0o755)
		f, err := os.OpenFile(filepath.Join(dir, task+".log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err == nil {
			fmt.Fprintf(f, "=== a%s r%s\n%s\n", att, rnd, prompt)
			f.Close()
		}
	}
	plan := map[string]map[string]Step{}
	if p := os.Getenv("FAKE_ACP_PLAN"); p != "" {
		b, err := os.ReadFile(p)
		if err == nil {
			json.Unmarshal(b, &plan)
		}
	}
	for _, t := range []string{task, "*"} {
		steps, ok := plan[t]
		if !ok {
			continue
		}
		for _, k := range []string{"a" + att + "r" + rnd, "r" + rnd, "*"} {
			if s, ok := steps[k]; ok {
				return s
			}
		}
	}
	return Step{}
}

func run(id json.RawMessage, prompt string) {
	s := step(prompt)
	if s.SleepMS > 0 {
		time.Sleep(time.Duration(s.SleepMS) * time.Millisecond)
	}
	if s.Crash {
		fmt.Fprintln(os.Stderr, "fakeacp: crashing as planned")
		os.Exit(3)
	}
	if s.Hang {
		if s.Tokens > 0 {
			update(map[string]any{"sessionUpdate": "usage_update", "used": s.Tokens, "size": 200000})
		}
		tk := time.NewTicker(200 * time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-cancelled:
				reply(id, map[string]any{"stopReason": "cancelled"})
				return
			case <-tk.C:
				if !s.Silent {
					update(map[string]any{"sessionUpdate": "agent_thought_chunk", "content": map[string]any{"type": "text", "text": "."}})
				}
			}
		}
	}
	allowed := true
	if s.Permission != "" {
		r := call("session/request_permission", map[string]any{
			"sessionId": "fake-1",
			"toolCall":  map[string]any{"toolCallId": "perm-1", "title": "Write " + s.Permission, "kind": "edit", "locations": []map[string]any{{"path": s.Permission}}},
			"options": []map[string]any{
				{"optionId": "yes", "name": "Allow", "kind": "allow_once"},
				{"optionId": "no", "name": "Reject", "kind": "reject_once"},
			},
		})
		var res struct {
			Outcome struct {
				Outcome  string `json:"outcome"`
				OptionID string `json:"optionId"`
			} `json:"outcome"`
		}
		json.Unmarshal(r.Result, &res)
		allowed = res.Outcome.Outcome == "selected" && res.Outcome.OptionID == "yes"
		if dir := os.Getenv("FAKE_ACP_LOG"); dir != "" {
			os.WriteFile(filepath.Join(dir, "permission-"+filepath.Base(s.Permission)), []byte(fmt.Sprint(allowed)), 0o644)
		}
	}
	if allowed {
		n := 0
		for p, c := range s.Write {
			n++
			id := fmt.Sprintf("w%d", n)
			update(map[string]any{"sessionUpdate": "tool_call", "toolCallId": id, "title": "Write " + p, "kind": "edit", "status": "in_progress",
				"locations": []map[string]any{{"path": filepath.Join(cwd, p)}}})
			full := filepath.Join(cwd, p)
			os.MkdirAll(filepath.Dir(full), 0o755)
			os.WriteFile(full, []byte(c), 0o644)
			update(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": id, "status": "completed"})
		}
		for p, c := range s.Append {
			full := filepath.Join(cwd, p)
			f, err := os.OpenFile(full, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
			if err == nil {
				f.WriteString(c)
				f.Close()
			}
		}
		for _, p := range s.Delete {
			os.Remove(filepath.Join(cwd, p))
		}
		if s.GitCommit {
			c := exec.Command("sh", "-c", "git add -A && git -c user.name=agent -c user.email=a@a commit -qm 'agent commit'")
			c.Dir = cwd
			c.Run()
		}
	}
	m := s.Message
	if m == "" {
		m = "Done."
	}
	update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": m}})
	if s.Tokens > 0 {
		update(map[string]any{"sessionUpdate": "usage_update", "used": s.Tokens, "size": 200000})
	}
	stop := s.Stop
	if stop == "" {
		stop = "end_turn"
	}
	reply(id, map[string]any{"stopReason": stop, "usage": map[string]any{"inputTokens": s.Tokens / 2, "outputTokens": s.Tokens - s.Tokens/2, "totalTokens": s.Tokens}})
}

func main() {
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 64<<10), 64<<20)
	for sc.Scan() {
		var m msg
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		switch {
		case m.Method == "" && len(m.ID) > 0: // response to our request
			pendingMu.Lock()
			ch := pending[string(m.ID)]
			delete(pending, string(m.ID))
			pendingMu.Unlock()
			if ch != nil {
				ch <- m
			}
		case m.Method == "initialize":
			reply(m.ID, map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{}, "authMethods": []any{},
				"agentInfo": map[string]any{"name": "fakeacp", "version": "1"}})
		case m.Method == "session/new":
			var p struct {
				Cwd string `json:"cwd"`
			}
			json.Unmarshal(m.Params, &p)
			cwd = p.Cwd
			reply(m.ID, map[string]any{"sessionId": "fake-1", "modes": map[string]any{"currentModeId": "default",
				"availableModes": []map[string]any{{"id": "default", "name": "Default"}, {"id": "acceptEdits", "name": "Accept edits"}}}})
		case m.Method == "session/set_mode":
			reply(m.ID, map[string]any{})
		case m.Method == "session/prompt":
			var p struct {
				Prompt []struct {
					Text string `json:"text"`
				} `json:"prompt"`
			}
			json.Unmarshal(m.Params, &p)
			var b strings.Builder
			for _, c := range p.Prompt {
				b.WriteString(c.Text)
			}
			go run(m.ID, b.String())
		case m.Method == "session/cancel":
			select {
			case cancelled <- struct{}{}:
			default:
			}
		case len(m.ID) > 0:
			b, _ := json.Marshal(map[string]any{"code": -32601, "message": "method not found"})
			send(msg{ID: m.ID, Error: b})
		}
	}
}
