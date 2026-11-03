package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/patcarter883/spine-next/internal/api"
)

// fakeLocalModel is an OpenAI-compatible chat-completions endpoint (the API
// vLLM, SGLang and llama.cpp serve) that scripts a coding session: it asks
// for a tool call, then answers once the tool result comes back. Requests
// whose prompt contains HANG first ask for a shell command that never ends.
type fakeLocalModel struct {
	url      string
	requests atomic.Int32
}

func startFakeModel(t *testing.T, host string) *fakeLocalModel {
	t.Helper()
	m := &fakeLocalModel{}
	ln, err := net.Listen("tcp", host+":0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(m.serve)}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	m.url = "http://" + ln.Addr().String() + "/v1"
	return m
}

func (m *fakeLocalModel) serve(w http.ResponseWriter, r *http.Request) {
	if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
		http.NotFound(w, r)
		return
	}
	m.requests.Add(1)
	var req struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	all, last := "", ""
	for _, msg := range req.Messages {
		all += string(msg.Content)
		last = msg.Role
	}
	lastUser := ""
	for _, msg := range req.Messages {
		if msg.Role == "user" {
			lastUser = string(msg.Content)
		}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	fl := w.(http.Flusher)
	chunk := func(delta map[string]any, finish any, usage any) {
		c := map[string]any{"id": "c1", "object": "chat.completion.chunk", "created": time.Now().Unix(), "model": "fake-coder",
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}}
		if usage != nil {
			c["usage"] = usage
		}
		b, _ := json.Marshal(c)
		fmt.Fprintf(w, "data: %s\n\n", b)
		fl.Flush()
	}
	toolCall := func(name string, args map[string]any) {
		a, _ := json.Marshal(args)
		chunk(map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": fmt.Sprintf("call_%d", m.requests.Load()),
			"type": "function", "function": map[string]any{"name": name, "arguments": string(a)}}}}, nil, nil)
		chunk(map[string]any{}, "tool_calls", map[string]any{"prompt_tokens": 1000, "completion_tokens": 50, "total_tokens": 1050})
	}
	switch {
	case last == "tool":
		chunk(map[string]any{"role": "assistant", "content": "Done."}, nil, nil)
		chunk(map[string]any{}, "stop", map[string]any{"prompt_tokens": 1200, "completion_tokens": 5, "total_tokens": 1205})
	case strings.Contains(all, "ESCAPE"):
		toolCall("write", map[string]any{"path": "../escape.txt", "content": "outside the worktree\n"})
	case strings.Contains(all, "HANG") && !strings.Contains(lastUser, "ran longer than"):
		toolCall("bash", map[string]any{"command": "sleep 600"})
	default:
		toolCall("write", map[string]any{"path": "hello.txt", "content": "written by pi\n"})
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
	fl.Flush()
}

func piAgentDirFor(t *testing.T, modelURL string) string {
	dir := t.TempDir()
	models := map[string]any{"providers": map[string]any{"local": map[string]any{
		"baseUrl": modelURL, "api": "openai-completions", "apiKey": "unused",
		"models": []any{map[string]any{"id": "fake-coder", "contextWindow": 32000, "maxTokens": 4096}},
	}}}
	b, _ := json.Marshal(models)
	if err := os.WriteFile(filepath.Join(dir, "models.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func realPi(t *testing.T) string {
	bin := os.Getenv("SPINE_PI_BIN")
	if bin == "" {
		t.Skip("set SPINE_PI_BIN to a pi 1.5.0 binary to run against the real agent")
	}
	return bin
}

func (e env) runRealPi(t *testing.T, a Adapter, intent string, timeout time.Duration) (api.Outcome, []Event) {
	t.Helper()
	var evs []Event
	out, err := (&Runner{Procs: e.m, WorktreeRoot: filepath.Dir(e.lease.Path)}).Run(context.Background(), RunRequest{
		Lease: e.lease, Adapter: a, Timeout: timeout,
		Prompt: Prompt{Task: "t1", Attempt: 1, Intent: intent}, Events: func(ev Event) { evs = append(evs, ev) }})
	if err != nil {
		t.Fatal(err)
	}
	return out, evs
}

// The real Pi 1.5.0, driven over RPC by Spine, against a local
// OpenAI-compatible model.
func TestRealPiLocalModel(t *testing.T) {
	bin := realPi(t)
	model := startFakeModel(t, "127.0.0.1")
	e := setup(t, map[string]map[string]any{})
	a := Adapter{Name: "pi", Protocol: "pi-rpc", Argv: []string{bin}, Version: "1.5.0", Model: "local/fake-coder",
		ToolTimeout: 3 * time.Second, Env: map[string]string{PiAgentDirEnv: piAgentDirFor(t, model.url), "PI_OFFLINE": "1"}}

	out, evs := e.runRealPi(t, a, "Create hello.txt.", time.Minute)
	if out.Reason != api.OutcomeCompleted || out.StopReason != "stop" || out.TokensUsed < 2000 {
		t.Fatalf("%+v\nevents %+v", out, evs)
	}
	if b, _ := os.ReadFile(filepath.Join(e.lease.Path, "hello.txt")); string(b) != "written by pi\n" {
		t.Fatalf("hello.txt = %q", b)
	}

	// A shell command with no timeout is stopped by Spine's tool timeout and
	// the session continues to completion.
	e2 := setup(t, map[string]map[string]any{})
	start := time.Now()
	out, evs = e2.runRealPi(t, a, "HANG then create hello.txt.", time.Minute)
	if out.Reason != api.OutcomeCompleted || time.Since(start) > 45*time.Second {
		t.Fatalf("%+v after %s\nevents %+v", out, time.Since(start), evs)
	}
	if _, err := os.Stat(filepath.Join(e2.lease.Path, "hello.txt")); err != nil {
		t.Fatalf("continuation did not finish the task: %+v", evs)
	}
}

// The same, with Pi inside a container (SPINE_PI_IMAGE: an image whose pi
// is 1.5.0).
func TestRealPiInContainer(t *testing.T) {
	image := os.Getenv("SPINE_PI_IMAGE")
	if image == "" {
		t.Skip("set SPINE_PI_IMAGE to run Pi in a container")
	}
	model := startFakeModel(t, "127.0.0.1")
	e := setup(t, map[string]map[string]any{})
	a := Adapter{Name: "pi", Protocol: "pi-rpc", Argv: []string{"pi"}, Version: "1.5.0", Model: "local/fake-coder",
		Container: &Container{Image: image, Network: "host"},
		Env:       map[string]string{PiAgentDirEnv: piAgentDirFor(t, model.url), "PI_OFFLINE": "1"}}
	out, evs := e.runRealPi(t, a, "Create hello.txt.", 2*time.Minute)
	if out.Reason != api.OutcomeCompleted {
		t.Fatalf("%+v\nevents %+v", out, evs)
	}
	fi, err := os.Stat(filepath.Join(e.lease.Path, "hello.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if st := fi.Sys(); st != nil {
		if uid := fileUID(fi); uid != os.Getuid() {
			t.Fatalf("file owned by uid %d, want %d", uid, os.Getuid())
		}
	}
	if n, err := ReapContainers(context.Background(), "docker", filepath.Dir(e.lease.Path)); err != nil || n != 0 {
		t.Fatalf("containers left behind: %d %v", n, err)
	}

	// Writes outside the worktree land in the container, not on the host.
	e2 := setup(t, map[string]map[string]any{})
	out, evs = e2.runRealPi(t, a, "ESCAPE: write outside the worktree.", 2*time.Minute)
	if out.Reason != api.OutcomeCompleted {
		t.Fatalf("%+v\nevents %+v", out, evs)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(e2.lease.Path), "escape.txt")); err == nil {
		t.Fatal("a containerized agent wrote outside its worktree on the host")
	}
}

// A container left by a killed controller is removed by ReapContainers,
// and only containers of that worktree root are touched.
func TestReapOrphanContainers(t *testing.T) {
	image := os.Getenv("SPINE_PI_IMAGE")
	if image == "" {
		t.Skip("set SPINE_PI_IMAGE to test container reaping")
	}
	root, other := t.TempDir(), t.TempDir()
	start := func(r string) string {
		out, err := execCommand("docker", "run", "-d", "--label", ContainerLabel+"="+RootLabel(r), "--network", "none", image, "sleep", "300")
		if err != nil {
			t.Fatalf("%v %s", err, out)
		}
		return strings.TrimSpace(out)
	}
	mine, theirs := start(root), start(other)
	defer execCommand("docker", "rm", "-f", theirs)
	n, err := ReapContainers(context.Background(), "docker", root)
	if err != nil || n != 1 {
		t.Fatalf("reaped %d %v", n, err)
	}
	if out, _ := execCommand("docker", "ps", "-aq", "--filter", "id="+mine); strings.TrimSpace(out) != "" {
		t.Fatal("orphan still present")
	}
	if out, _ := execCommand("docker", "ps", "-q", "--filter", "id="+theirs); strings.TrimSpace(out) == "" {
		t.Fatal("reaped another root's container")
	}
}
