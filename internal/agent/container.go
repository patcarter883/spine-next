package agent

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/patcarter883/spine-next/internal/exec"
)

// Container runs each agent process in a fresh container (Pi has no sandbox
// and never asks for approval; Claude Code can be contained the same way).
// Only the task's worktree and its private git dir are writable; the
// repository's shared git dir is mounted read-only, so the agent can read
// history and run git status/diff but cannot move refs. The container is
// removed when the run ends, and a restarted controller removes any it left
// behind (ReapContainers).
type Container struct {
	Runtime string `yaml:"runtime"` // docker (default) or podman
	Image   string `yaml:"image"`
	// Network is passed to --network. Default "host", so a local model
	// server on the host (vLLM, SGLang, llama.cpp) is reachable; use
	// "none" for models reached another way, or a dedicated network.
	Network string `yaml:"network"`
	// User is passed to --user; default the controller's uid:gid, so files
	// the agent writes belong to the operator.
	User string `yaml:"user"`
	// Mounts are extra -v specs ("src:dst[:ro]").
	Mounts []string `yaml:"mounts"`
	// Args are extra `run` arguments (e.g. --memory 8g, --cpus 4).
	Args []string `yaml:"args"`
}

func (c *Container) runtime() string {
	if c.Runtime == "" {
		return "docker"
	}
	return c.Runtime
}

// ContainerLabel marks Spine's agent containers; its value identifies the
// worktree root (one per repository), so reaping never touches another
// repository's or another program's containers.
const ContainerLabel = "dev.spine.worktree-root"

// RootLabel is the label value for a worktree root.
func RootLabel(root string) string {
	h := sha256.Sum256([]byte(root))
	return hex.EncodeToString(h[:8])
}

var reNameUnsafe = regexp.MustCompile(`[^a-zA-Z0-9_.-]+`)

type wrapped struct {
	argv    []string
	env     []string // for the container CLI itself
	name    string
	envFile string
}

// wrap turns an agent command into a `<runtime> run` command. env is the
// agent's environment (passed through a 0600 env file, never on the
// command line); extraMounts are added after the worktree mounts.
func (c *Container) wrap(rootLabel, worktree, task string, attempt, round int, argv, env []string, extraMounts, extraEnv []string) (*wrapped, error) {
	if c.Image == "" {
		return nil, fmt.Errorf("agent: container.image is required")
	}
	gitdir, commondir, err := worktreeGitDirs(worktree)
	if err != nil {
		return nil, err
	}
	var rnd [3]byte
	rand.Read(rnd[:])
	name := reNameUnsafe.ReplaceAllString(fmt.Sprintf("spine-%s-a%d-r%d-%s", task, attempt, round, hex.EncodeToString(rnd[:])), "-")
	f, err := os.CreateTemp("", "spine-agent-env-*")
	if err != nil {
		return nil, err
	}
	for _, kv := range append(append([]string{}, env...), extraEnv...) {
		if strings.ContainsAny(kv, "\n\r") || !strings.Contains(kv, "=") {
			continue // env files cannot carry multi-line values
		}
		fmt.Fprintln(f, kv)
	}
	f.Close()
	network := c.Network
	if network == "" {
		network = "host"
	}
	user := c.User
	if user == "" {
		user = fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
	}
	a := []string{c.runtime(), "run", "--rm", "-i", "--init", "--name", name,
		"--label", ContainerLabel + "=" + rootLabel,
		"--network", network, "--user", user, "--workdir", worktree,
		"--env-file", f.Name(), "--env", "HOME=/tmp/spine-home",
		"--volume", commondir + ":" + commondir + ":ro",
		"--volume", gitdir + ":" + gitdir,
		"--volume", worktree + ":" + worktree,
	}
	for _, m := range append(append([]string{}, extraMounts...), c.Mounts...) {
		a = append(a, "--volume", m)
	}
	a = append(a, c.Args...)
	a = append(a, c.Image)
	a = append(a, argv...)
	return &wrapped{argv: a, env: cliEnv(), name: name, envFile: f.Name()}, nil
}

// cleanup force-removes the container (it may outlive a killed CLI client)
// and the env file.
func (c *Container) cleanup(w *wrapped) {
	if w == nil {
		return
	}
	os.Remove(w.envFile)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	exec.Run(ctx, exec.Cmd{Argv: []string{c.runtime(), "rm", "-f", w.name}, Env: cliEnv()})
}

// cliEnv is the environment for the container CLI itself.
// RuntimeName is the container command (docker unless configured).
func (c *Container) RuntimeName() string { return c.runtime() }

// CLIEnv is the environment for running the container CLI.
func CLIEnv() []string { return cliEnv() }

func cliEnv() []string {
	var env []string
	for _, k := range []string{"PATH", "HOME", "DOCKER_HOST", "DOCKER_CONFIG", "DOCKER_CONTEXT", "CONTAINER_HOST", "XDG_RUNTIME_DIR", "XDG_CONFIG_HOME"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// worktreeGitDirs returns a linked worktree's private git dir and the
// repository's common dir.
func worktreeGitDirs(worktree string) (gitdir, commondir string, err error) {
	b, err := os.ReadFile(filepath.Join(worktree, ".git"))
	if err != nil {
		return "", "", fmt.Errorf("agent: %s is not a linked worktree: %w", worktree, err)
	}
	gd, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir: ")
	if !ok {
		return "", "", fmt.Errorf("agent: unexpected .git file in %s", worktree)
	}
	if !filepath.IsAbs(gd) {
		gd = filepath.Join(worktree, gd)
	}
	cd := filepath.Dir(filepath.Dir(gd))
	if rel, err := os.ReadFile(filepath.Join(gd, "commondir")); err == nil {
		c := strings.TrimSpace(string(rel))
		if !filepath.IsAbs(c) {
			c = filepath.Join(gd, c)
		}
		cd = filepath.Clean(c)
	}
	return filepath.Clean(gd), cd, nil
}

// ReapContainers removes agent containers left by a controller that died
// for the worktree root (a repository). Called before a controller starts.
func ReapContainers(ctx context.Context, runtime, root string) (int, error) {
	if runtime == "" {
		runtime = "docker"
	}
	res, err := exec.Run(ctx, exec.Cmd{Argv: []string{runtime, "ps", "-aq", "--filter", "label=" + ContainerLabel + "=" + RootLabel(root)},
		Env: cliEnv(), Timeout: 30 * time.Second})
	if err != nil {
		return 0, err
	}
	if !res.OK() {
		return 0, fmt.Errorf("%s ps: %s", runtime, strings.TrimSpace(string(res.Stderr)))
	}
	ids := strings.Fields(string(res.Stdout))
	if len(ids) == 0 {
		return 0, nil
	}
	rm, err := exec.Run(ctx, exec.Cmd{Argv: append([]string{runtime, "rm", "-f"}, ids...), Env: cliEnv(), Timeout: time.Minute})
	if err != nil {
		return 0, err
	}
	if !rm.OK() {
		return 0, fmt.Errorf("%s rm: %s", runtime, strings.TrimSpace(string(rm.Stderr)))
	}
	return len(ids), nil
}
