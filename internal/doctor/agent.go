package doctor

import (
	"context"
	"fmt"
	"os"
	osexec "os/exec"
	"strings"

	"github.com/patcarter883/spine-next/internal/agent"
	"github.com/patcarter883/spine-next/internal/config"
	"github.com/patcarter883/spine-next/internal/gate"
	"github.com/patcarter883/spine-next/internal/gitro"
	"github.com/patcarter883/spine-next/internal/tools/ast"
	"github.com/patcarter883/spine-next/internal/worktree"
)

// Agent checks the controller configuration, the default agent adapter and
// the gates file at the base branch. With probe, it also starts the adapter
// and performs the ACP handshake (initialize + session/new) in a scratch
// directory; no prompt is sent.
func Agent(ctx context.Context, repoDir string, probe bool) []Finding {
	var out []Finding
	add := func(l Level, check, format string, args ...any) {
		out = append(out, Finding{Level: l, Check: check, Message: fmt.Sprintf(format, args...)})
	}
	primary, err := worktree.PrimaryOf(ctx, repoDir)
	if err != nil {
		return out
	}
	cfg, err := config.Load(primary)
	if err != nil {
		add(Fail, "config", "%v", err)
		return out
	}
	add(OK, "config", "agent %q, %d parallel, base %s", cfg.Agent, cfg.Parallel, cfg.Base)
	if sv := cfg.Supervisor; sv.Type == "llm" {
		if sv.Provider != "" {
			p, err := agent.LoadPiProvider(agent.PiAgentDir(cfg.PiAdapter()), sv.Provider)
			if err == nil {
				_, err = p.ResolveKey()
			}
			if err != nil {
				add(Warn, "supervisor", "%v: decisions will fall back to the rules", err)
			} else {
				add(OK, "supervisor", "llm %s/%s at %s", sv.Provider, sv.Model, p.BaseURL)
			}
		} else if os.Getenv("ANTHROPIC_API_KEY") == "" && os.Getenv("ANTHROPIC_AUTH_TOKEN") == "" {
			add(Warn, "supervisor", "llm uses the Anthropic API, but neither ANTHROPIC_API_KEY nor ANTHROPIC_AUTH_TOKEN is set: without an `ant auth login` profile, decisions fall back to the rules")
		}
	}
	if !ast.Available() {
		add(Warn, "tree-sitter", "built without CGO: ts.* gates are unavailable")
	}
	if _, err := (gitro.Repo{Dir: primary}).Line(ctx, "rev-parse", "--verify", "-q", cfg.Base); err != nil {
		add(Fail, "base", "%s does not exist (set base: in %s)", cfg.Base, config.Path)
	} else if gc, ok, err := gate.LoadConfig(ctx, primary, cfg.Base); err != nil {
		add(Fail, "gates", "%s at %s: %v", gate.ConfigPath, cfg.Base, err)
	} else if !ok {
		add(Warn, "gates", "no %s at %s: only the default gates (parse, scope, nonempty) apply", gate.ConfigPath, cfg.Base)
	} else {
		add(OK, "gates", "%s", strings.Join(gc.Order, ", "))
	}
	a := cfg.Adapters[cfg.Agent]
	if a.Container != nil {
		rt := a.Container.Runtime
		if rt == "" {
			rt = "docker"
		}
		if _, err := osexec.LookPath(rt); err != nil {
			add(Fail, "agent", "container runtime %s not found on PATH", rt)
			return out
		}
		add(OK, "agent", "%s: %s in container %s (network %s)", a.Name, strings.Join(a.Argv, " "), a.Container.Image, orDefault(a.Container.Network, "host"))
	} else if _, err := osexec.LookPath(a.Argv[0]); err != nil {
		hint := ""
		if a.Protocol == "pi-rpc" {
			hint = " (install the pinned version: npm install -g @earendil-works/pi-coding-agent@" + a.Version + ")"
		}
		if a.Argv[0] == "claude-agent-acp" {
			hint = " (install: npm install -g @agentclientprotocol/claude-agent-acp)"
		}
		add(Fail, "agent", "%s not found on PATH%s", a.Argv[0], hint)
		return out
	} else {
		add(OK, "agent", "%s: %s", a.Name, strings.Join(a.Argv, " "))
	}
	if a.Name == "claude" && os.Getenv("ANTHROPIC_API_KEY") == "" && os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") == "" {
		add(Warn, "agent", "neither ANTHROPIC_API_KEY nor CLAUDE_CODE_OAUTH_TOKEN is set; Claude Code must find credentials in ~/.claude")
	}
	if a.Protocol == "pi-rpc" {
		r := &agent.Runner{}
		if probe {
			dir, err := os.MkdirTemp("", "spine-probe-")
			if err != nil {
				add(Fail, "pi", "%v", err)
				return out
			}
			defer os.RemoveAll(dir)
			if msg, err := r.ProbePi(ctx, a, dir); err != nil {
				add(Fail, "pi", "%v", err)
			} else {
				add(OK, "pi", "%s", msg)
			}
		} else if err := r.CheckPiVersion(ctx, a); err != nil {
			add(Fail, "pi", "%v", err)
		} else {
			add(OK, "pi", "version %s (pinned)", a.Version)
		}
		if a.Container == nil {
			add(Warn, "pi", "pi has no sandbox and never asks for approval: give this adapter a container:")
		}
		return out
	}
	if probe {
		dir, err := os.MkdirTemp("", "spine-probe-")
		if err != nil {
			add(Fail, "acp", "%v", err)
			return out
		}
		defer os.RemoveAll(dir)
		r, err := agent.Probe(ctx, a, dir)
		if err != nil {
			add(Fail, "acp", "%v", err)
		} else {
			add(OK, "acp", "%s %s speaks ACP v%d; modes %s", r.Agent, r.Version, r.Protocol, strings.Join(r.Modes, ","))
			if a.Mode != "" && len(r.Modes) > 0 && !contains(r.Modes, a.Mode) {
				add(Warn, "acp", "configured mode %q is not offered by the agent", a.Mode)
			}
		}
	}
	return out
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
