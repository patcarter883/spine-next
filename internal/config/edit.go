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
	var doc yaml.Node
	if len(bytes.TrimSpace(src)) > 0 {
		if err := yaml.Unmarshal(src, &doc); err != nil {
			return nil, fmt.Errorf("config: %w", err)
		}
	}
	if doc.Kind == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("config: the file is not a mapping")
	}
	set := func(path string, v any) {
		node := root
		parts := strings.Split(path, ".")
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
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, err
	}
	enc.Close()
	if _, err := Parse(buf.Bytes()); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
