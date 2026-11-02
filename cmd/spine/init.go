package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/patcarter883/spine-next/internal/config"
	"github.com/patcarter883/spine-next/internal/gate"
	"github.com/patcarter883/spine-next/internal/worktree"
)

// runInit scaffolds .spine/config.yaml and .spine/gates.yaml from what the
// repository contains. Gates are read from the base branch, so the gates
// file must be committed before tasks can use it.
func runInit(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	repo := fs.String("repo", ".", "repository")
	force := fs.Bool("force", false, "overwrite existing files")
	if err := fs.Parse(args); err != nil {
		return err
	}
	root, err := worktree.PrimaryOf(ctx, *repo)
	if err != nil {
		return err
	}
	exists := func(p string) bool { _, err := os.Stat(filepath.Join(root, p)); return err == nil }
	have := func(bin string) bool { _, err := exec.LookPath(bin); return err == nil }

	var gates []string
	var order []string
	add := func(name, def string) {
		gates = append(gates, fmt.Sprintf("  %-9s %s", name+":", def))
		order = append(order, name)
	}
	add("parse", "{tool: ts.parse}")
	add("scope", "{tool: diff.scope}")
	add("nonempty", "{tool: diff.nonempty}")
	var link []string
	var detected []string
	if exists("go.mod") {
		detected = append(detected, "Go")
		add("build", "{tool: go.build}")
		add("vet", "{tool: go.vet}")
		add("unit", "{tool: go.test, policy: baseline}")
	}
	if exists("pyproject.toml") || exists("setup.py") || exists("requirements.txt") {
		detected = append(detected, "Python")
		if have("ruff") {
			add("lint", `{tool: cmd.run, policy: baseline, args: {argv: [ruff, check, --output-format=json, --no-cache, "."], parser: ruff-json}}`)
		}
		if have("pytest") {
			add("pytest", `{tool: cmd.run, policy: baseline, args: {argv: [pytest, -q, -p, no:cacheprovider, "--junitxml={tmp}/junit.xml"], report: "{tmp}/junit.xml", parser: pytest-junit, env: {PYTHONPATH: "."}}}`)
		}
		link = append(link, ".venv")
	}
	if exists("package.json") {
		detected = append(detected, "Node")
		link = append(link, "node_modules")
		if exists("tsconfig.json") {
			add("types", `{tool: cmd.run, policy: baseline, args: {argv: [npx, --no-install, tsc, -p, ., --noEmit, --pretty, "false"], parser: tsc}}`)
		}
		if b, err := os.ReadFile(filepath.Join(root, "package.json")); err == nil && strings.Contains(string(b), `"test"`) {
			add("test", `{tool: cmd.run, args: {argv: [npm, test, --silent]}, timeout: 15m}`)
		}
	}
	if exists("composer.json") {
		detected = append(detected, "PHP")
		add("php", `{tool: cmd.run, args: {argv: [sh, -c, "git ls-files -z -- '*.php' 2>/dev/null | xargs -0 -r -n1 php -l || find . -name '*.php' -not -path './vendor/*' | xargs -r -n1 php -l"], parser: php-lint}}`)
		link = append(link, "vendor")
	}
	add("api", "{tool: ts.api_contract}")

	var g strings.Builder
	g.WriteString("# Spine gates: deterministic checks a task must pass before it integrates.\n")
	g.WriteString("# Read from the base branch (commit this file). Policies: absolute (exit 0)\n")
	g.WriteString("# or baseline (no NEW failures compared with the base tree).\n")
	g.WriteString("gates:\n" + strings.Join(gates, "\n") + "\n")
	g.WriteString("order: [" + strings.Join(order, ", ") + "]\n")
	if len(link) > 0 {
		g.WriteString("baseline:\n  # ignored dependency dirs linked from the task worktree into checked-out trees\n  link: [" + strings.Join(link, ", ") + "]\n")
	}
	cfgText := `# Spine controller configuration (operator settings; not read from tasks).
agent: claude
parallel: 4
base: main
budget:
  max_attempts: 2
  max_patch_rounds: 3
  per_run_timeout: 45m
  max_wall_clock: 4h
adapters:
  claude:
    # Claude Code over ACP: npm install -g @agentclientprotocol/claude-agent-acp
    argv: [claude-agent-acp]
    mode: acceptEdits
    permissions: worktree   # allow tool calls inside the task worktree only
# hooks run in every new worktree before the agent starts, e.g. installing deps:
# hooks:
#   - {argv: [npm, ci], timeout: 10m}
`
	write := func(rel, content string) error {
		p := filepath.Join(root, rel)
		if _, err := os.Stat(p); err == nil && !*force {
			fmt.Printf("kept      %s (exists; --force to overwrite)\n", rel)
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			return err
		}
		fmt.Printf("wrote     %s\n", rel)
		return nil
	}
	if err := write(config.Path, cfgText); err != nil {
		return err
	}
	if err := write(gate.ConfigPath, g.String()); err != nil {
		return err
	}
	if _, err := gate.ParseConfig([]byte(g.String())); err != nil {
		return errors.New("init: generated an invalid gates file (bug): " + err.Error())
	}
	if len(detected) == 0 {
		detected = []string{"no known toolchain"}
	}
	fmt.Printf("\ndetected  %s\n", strings.Join(detected, ", "))
	fmt.Println("next      review the files, then: git add .spine && git commit -m 'Add Spine config'")
	fmt.Println("          spine doctor --agent")
	fmt.Println(`          spine run "describe the change"`)
	return nil
}
