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
		"agent: pi\nadapters:\n  pi: {protocol: pi-rpc}\n":                                             "pinned version",
		"agent: pi\nadapters:\n  pi: {protocol: smoke-signals, argv: [x]}\n":                           "protocol must be",
		"agent: pi\nadapters:\n  pi: {protocol: pi-rpc, version: 1.5.0, container: {network: host}}\n": "container.image",
	} {
		if _, err := Parse([]byte(bad)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}
