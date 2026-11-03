package config

import (
	"strings"
	"testing"
	"time"
)

func TestPiAdapterConfig(t *testing.T) {
	c, err := Parse([]byte(`
agent: pi
adapters:
  pi:
    protocol: pi-rpc
    version: 1.5.0
    model: vllm/qwen3-coder-30b
    tool_timeout: 5m
    max_turns: 40
    container:
      image: spine-pi:1.5.0
      network: host
      args: [--memory, 8g]
`))
	if err != nil {
		t.Fatal(err)
	}
	a := c.Adapters["pi"]
	if a.Protocol != "pi-rpc" || a.Argv[0] != "pi" || a.Version != "1.5.0" || a.ToolTimeout != 5*time.Minute ||
		a.MaxTurns != 40 || a.Container == nil || a.Container.Image != "spine-pi:1.5.0" || len(a.Container.Args) != 2 {
		t.Fatalf("%+v %+v", a, a.Container)
	}
	for bad, want := range map[string]string{
		"agent: local\nadapters:\n  local: {protocol: pi-rpc}\n":                                       "pinned version",
		"agent: pi\nadapters:\n  pi: {protocol: smoke-signals, argv: [x]}\n":                           "protocol must be",
		"agent: pi\nadapters:\n  pi: {protocol: pi-rpc, version: 1.5.0, container: {network: host}}\n": "container.image",
	} {
		if _, err := Parse([]byte(bad)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestBuiltinPiAdapter(t *testing.T) {
	d := Default()
	if a, ok := d.Adapters["pi"]; !ok || a.Protocol != "pi-rpc" || a.Version == "" || a.Argv[0] != "pi" {
		t.Fatalf("built-in pi: %+v", a)
	}
	c, err := Parse([]byte("agent: pi\nadapters:\n  pi: {model: llama/qwen3-coder, container: {image: spine-pi:1.5.0}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	a := c.Adapters["pi"]
	if a.Protocol != "pi-rpc" || a.Version != "1.5.0" || a.Model != "llama/qwen3-coder" || a.Container == nil {
		t.Fatalf("overlay: %+v", a)
	}
	if got := strings.Join(c.AdapterNames(), ","); got != "claude,pi" {
		t.Fatalf("adapters %s", got)
	}
}

func TestApplySettingsKeepsComments(t *testing.T) {
	src := []byte("# my config\nagent: claude # default\nbudget:\n  max_attempts: 2\nadapters:\n  claude: {argv: [claude-agent-acp]}\n")
	p, a := 6, "pi"
	r := 5
	out, err := ApplySettings(src, Settings{Parallel: &p, Agent: &a, MaxPatchRounds: &r})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{"# my config", "# default", "agent: pi", "parallel: 6", "max_patch_rounds: 5", "max_attempts: 2", "claude-agent-acp"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in\n%s", want, s)
		}
	}
	bad := "nope"
	if _, err := ApplySettings(src, Settings{Agent: &bad}); err == nil {
		t.Fatal("unknown agent accepted")
	}
	if _, err := ApplySettings(nil, Settings{Parallel: &p}); err != nil {
		t.Fatal(err)
	}
}

func TestTemplates(t *testing.T) {
	dir := t.TempDir()
	u := UserTemplates{Path: dir + "/t.yaml"}
	if err := u.Put(Template{Name: "docs", Scope: []string{"docs/**"}, Gates: []string{"scope"}}); err != nil {
		t.Fatal(err)
	}
	if err := u.Put(Template{Name: "bugfix", Intent: "Fix {{bug}} in {{pkg}} and add a regression test."}); err != nil {
		t.Fatal(err)
	}
	ts, err := u.Load()
	if err != nil || len(ts) != 2 || ts[0].Name != "bugfix" || ts[1].Source != "user" {
		t.Fatalf("%v %+v", err, ts)
	}
	u.Delete("docs")
	if ts, _ := u.Load(); len(ts) != 1 {
		t.Fatal(ts)
	}
	if _, err := ParseTemplates([]byte("templates:\n- {name: a}\n- {name: a}\n")); err == nil {
		t.Fatal("duplicate accepted")
	}
}
