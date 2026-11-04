package config

import (
	"bytes"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Settings are the fields the dashboard edits with a form. Nil fields are
// left as they are in the file.
type Settings struct {
	Agent          *string `json:"agent,omitempty"`
	Parallel       *int    `json:"parallel,omitempty"`
	Base           *string `json:"base,omitempty"`
	MaxAttempts    *int    `json:"max_attempts,omitempty"`
	MaxPatchRounds *int    `json:"max_patch_rounds,omitempty"`
	MaxTokens      *int64  `json:"max_tokens,omitempty"`
	MaxWallClock   *string `json:"max_wall_clock,omitempty"`
	PerRunTimeout  *string `json:"per_run_timeout,omitempty"`
	Supervisor     *string `json:"supervisor,omitempty"` // rules | llm
}

// ApplySettings sets fields in a config file's YAML, keeping comments and
// every other field, and validates the result with Parse.
func ApplySettings(src []byte, s Settings) ([]byte, error) {
	doc, root, err := parseDoc(src)
	if err != nil {
		return nil, err
	}
	set := func(path string, v any) { setPath(root, v, strings.Split(path, ".")...) }
	if s.Agent != nil {
		set("agent", *s.Agent)
	}
	if s.Parallel != nil {
		set("parallel", *s.Parallel)
	}
	if s.Base != nil {
		set("base", *s.Base)
	}
	if s.MaxAttempts != nil {
		set("budget.max_attempts", *s.MaxAttempts)
	}
	if s.MaxPatchRounds != nil {
		set("budget.max_patch_rounds", *s.MaxPatchRounds)
	}
	if s.MaxTokens != nil {
		set("budget.max_tokens", *s.MaxTokens)
	}
	if s.MaxWallClock != nil {
		set("budget.max_wall_clock", *s.MaxWallClock)
	}
	if s.PerRunTimeout != nil {
		set("budget.per_run_timeout", *s.PerRunTimeout)
	}
	if s.Supervisor != nil {
		set("supervisor.type", *s.Supervisor)
	}
	return encodeDoc(doc)
}

// PiSettings are the adapter fields the dashboard's Pi form edits.
type PiSettings struct {
	Version     string       `json:"version"`
	Model       string       `json:"model"`
	ToolTimeout string       `json:"tool_timeout"`
	MaxTurns    int          `json:"max_turns"`
	Container   *PiContainer `json:"container"` // nil = run on the host
	MakeDefault bool         `json:"make_default"`
}

// PiContainer is the part of a Pi adapter's container the form edits; other
// fields (user, mounts) stay as they are.
type PiContainer struct {
	Image   string   `json:"image"`
	Runtime string   `json:"runtime"`
	Network string   `json:"network"`
	Args    []string `json:"args"`
}

// ApplyPiAdapter sets a pi-rpc adapter's fields in a config file's YAML,
// keeping comments and every other field, and validates the result with
// Parse. Empty values remove their key, so the defaults apply.
func ApplyPiAdapter(src []byte, name string, s PiSettings) ([]byte, error) {
	doc, root, err := parseDoc(src)
	if err != nil {
		return nil, err
	}
	put := func(key string, v any, empty bool, parents ...string) {
		path := append(append([]string{"adapters", name}, parents...), key)
		if empty {
			deletePath(root, path...)
		} else {
			setPath(root, v, path...)
		}
	}
	put("protocol", "pi-rpc", false)
	put("version", s.Version, false)
	put("model", s.Model, s.Model == "")
	put("tool_timeout", s.ToolTimeout, s.ToolTimeout == "")
	put("max_turns", s.MaxTurns, s.MaxTurns == 0)
	if c := s.Container; c != nil {
		put("image", c.Image, false, "container")
		put("runtime", c.Runtime, c.Runtime == "", "container")
		put("network", c.Network, c.Network == "", "container")
		put("args", c.Args, len(c.Args) == 0, "container")
	} else {
		deletePath(root, "adapters", name, "container")
	}
	if s.MakeDefault {
		setPath(root, name, "agent")
	}
	return encodeDoc(doc)
}

func parseDoc(src []byte) (*yaml.Node, *yaml.Node, error) {
	doc := &yaml.Node{}
	if len(bytes.TrimSpace(src)) > 0 {
		if err := yaml.Unmarshal(src, doc); err != nil {
			return nil, nil, fmt.Errorf("config: %w", err)
		}
	}
	if doc.Kind == 0 {
		doc = &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, nil, fmt.Errorf("config: the file is not a mapping")
	}
	return doc, root, nil
}

func encodeDoc(doc *yaml.Node) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	enc.Close()
	if _, err := Parse(buf.Bytes()); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// setPath sets a nested key, creating the mappings on the way.
func setPath(node *yaml.Node, v any, parts ...string) {
	for i, p := range parts {
		var child *yaml.Node
		for j := 0; j+1 < len(node.Content); j += 2 {
			if node.Content[j].Value == p {
				child = node.Content[j+1]
			}
		}
		if i == len(parts)-1 {
			var val yaml.Node
			val.Encode(v)
			if child != nil {
				child.Kind, child.Tag, child.Value, child.Content, child.Style = val.Kind, val.Tag, val.Value, val.Content, 0
			} else {
				node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: p}, &val)
			}
			return
		}
		if child == nil {
			child = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: p}, child)
		}
		node = child
	}
}

// deletePath removes a nested key if it exists.
func deletePath(node *yaml.Node, parts ...string) {
	for i, p := range parts {
		var child *yaml.Node
		for j := 0; j+1 < len(node.Content); j += 2 {
			if node.Content[j].Value != p {
				continue
			}
			if i == len(parts)-1 {
				node.Content = append(node.Content[:j], node.Content[j+2:]...)
				return
			}
			child = node.Content[j+1]
			break
		}
		if child == nil {
			return
		}
		node = child
	}
}
