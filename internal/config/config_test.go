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

func TestApplyPiAdapter(t *testing.T) {
	src := []byte(`# top comment
agent: claude
adapters:
  pi:
    protocol: pi-rpc # keep me
    version: 1.4.0
    pass_env: [PATH, HOME, MY_KEY]
    tool_timeout: 5m
    container:
      image: old:1
      user: "1000:1000"
      mounts: [/data:/data:ro]
      args: [--cpus, "2"]
parallel: 3
`)
	out, err := ApplyPiAdapter(src, "pi", PiSettings{Version: "1.5.0", Model: "local/m", MakeDefault: true,
		Container: &PiContainer{Image: "spine-pi:1.5.0", Network: "none"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# top comment", "# keep me", "parallel: 3", "pass_env"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("lost %q:\n%s", want, out)
		}
	}
	c, err := Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	a := c.Adapters["pi"]
	if c.Agent != "pi" || a.Version != "1.5.0" || a.Model != "local/m" || a.ToolTimeout != 0 {
		t.Fatalf("adapter: %+v (default %s)", a, c.Agent)
	}
	if a.Container == nil || a.Container.Image != "spine-pi:1.5.0" || a.Container.Network != "none" || a.Container.User != "1000:1000" || len(a.Container.Mounts) != 1 || len(a.Container.Args) != 0 {
		t.Fatalf("container: %+v", a.Container)
	}

	// No container: the block goes; an empty file gets a whole adapter.
	out, err = ApplyPiAdapter(out, "pi", PiSettings{Version: "1.5.0"})
	if err != nil {
		t.Fatal(err)
	}
	if c, _ = Parse(out); c.Adapters["pi"].Container != nil || c.Agent != "pi" {
		t.Fatalf("after removal: %+v\n%s", c.Adapters["pi"], out)
	}
	out, err = ApplyPiAdapter(nil, "pi", PiSettings{Version: "1.5.0", MaxTurns: 40, ToolTimeout: "2m"})
	if err != nil {
		t.Fatal(err)
	}
	if c, err = Parse(out); err != nil || c.Adapters["pi"].MaxTurns != 40 || c.Agent != "claude" {
		t.Fatalf("%v %+v", err, c)
	}
	if _, err := ApplyPiAdapter(nil, "pi", PiSettings{Version: "1.5.0", ToolTimeout: "soon"}); err == nil {
		t.Fatal("a bad duration must not validate")
	}
}

func TestSupervisorProvider(t *testing.T) {
	local, model, none := "local", "m", ""
	out, err := ApplySettings([]byte("# keep\nsupervisor:\n  type: llm\n"), Settings{SupervisorProvider: &local, SupervisorModel: &model})
	if err != nil {
		t.Fatal(err)
	}
	c, err := Parse(out)
	if err != nil || c.Supervisor.Provider != "local" || c.Supervisor.Model != "m" || !strings.Contains(string(out), "# keep") {
		t.Fatalf("%v %+v\n%s", err, c.Supervisor, out)
	}
	if _, err := ApplySettings(out, Settings{SupervisorModel: &none}); err == nil {
		t.Fatal("a provider without a model must not validate")
	}
	out, err = ApplySettings(out, Settings{SupervisorProvider: &none, SupervisorModel: &none})
	if err != nil {
		t.Fatal(err)
	}
	if c, _ = Parse(out); c.Supervisor.Provider != "" || c.Supervisor.Type != "llm" || strings.Contains(string(out), "provider") {
		t.Fatalf("%+v\n%s", c.Supervisor, out)
	}
}

func TestAdapterEdits(t *testing.T) {
	src := []byte(`# top
agent: claude
adapters:
  glm:
    protocol: pi-rpc # pinned
    version: 1.5.0
parallel: 2
`)
	y, err := AdapterYAML(src, "glm")
	if err != nil || !strings.Contains(y, "version: 1.5.0") || !strings.Contains(y, "# pinned") {
		t.Fatalf("yaml %q %v", y, err)
	}
	if y, _ := AdapterYAML(src, "claude"); y != "" {
		t.Fatalf("built-in not in the file: %q", y)
	}
	out, err := ApplyAdapter(src, "glm", "protocol: pi-rpc\nversion: 1.5.0\nmodel: openrouter/m\n")
	if err != nil {
		t.Fatal(err)
	}
	c, err := Parse(out)
	if err != nil || c.Adapters["glm"].Model != "openrouter/m" || c.Parallel != 2 || !strings.Contains(string(out), "# top") {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	if out, err = ApplyAdapter(out, "local", "protocol: pi-rpc\nversion: 1.5.0\n"); err != nil {
		t.Fatal(err)
	}
	if c, _ := Parse(out); c.Adapters["local"].Protocol != "pi-rpc" {
		t.Fatal("new adapter missing")
	}
	for _, bad := range []string{"protocol: pi-rpc\n", "- a\n", "protocol: [\n"} {
		if _, err := ApplyAdapter(src, "glm", bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if _, err := ApplyAdapter(src, "bad name", "argv: [x]\n"); err == nil {
		t.Error("accepted a bad name")
	}
	if out, err = RemoveAdapter(out, "local"); err != nil {
		t.Fatal(err)
	}
	if c, _ := Parse(out); c.Adapters["local"].Name != "" || c.Adapters["glm"].Name == "" {
		t.Fatalf("remove: %+v", c.Adapters)
	}
	if _, err := RemoveAdapter(out, "claude"); err == nil {
		t.Error("removed an adapter the file doesn't define")
	}
	if _, err := RemoveAdapter([]byte("agent: glm\nadapters:\n  glm: {argv: [x]}\n"), "glm"); err == nil {
		t.Error("removed the default agent")
	}
}
