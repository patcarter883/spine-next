package worktree

import (
	"context"
	"fmt"
	"strings"

	"github.com/patcarter883/spine-next/internal/exec"
	gcgit "github.com/patcarter883/spine-next/third_party/gascity/git"
)

// GitError is a git invocation that ran and exited non-zero.
type GitError struct {
	Args     []string
	ExitCode int
	Stderr   string
}

func (e *GitError) Error() string {
	return fmt.Sprintf("git %s: exit %d: %s", strings.Join(e.Args, " "), e.ExitCode, strings.TrimSpace(e.Stderr))
}

// gitBaseArgs disable behaviours Spine must not trigger implicitly: automatic
// gc/maintenance (§6.2 rule 5) and repository hooks, which could run
// arbitrary code inside a supervised operation.
var gitBaseArgs = []string{"-c", "gc.auto=0", "-c", "maintenance.auto=false", "-c", "core.hooksPath=/dev/null"}

func (m *Manager) gitEnv(extra []string) []string {
	env := gcgit.SanitizedEnv()
	env = append(env,
		"GIT_AUTHOR_NAME="+m.cfg.Identity.Name, "GIT_AUTHOR_EMAIL="+m.cfg.Identity.Email,
		"GIT_COMMITTER_NAME="+m.cfg.Identity.Name, "GIT_COMMITTER_EMAIL="+m.cfg.Identity.Email,
		"GIT_TERMINAL_PROMPT=0", "LC_ALL=C",
		// Read-only commands (status, diff-files) must never take index.lock
		// to refresh stat info: a kill at that moment would strand a lock in
		// a checkout Spine does not own (found by the chaos harness, M0).
		"GIT_OPTIONAL_LOCKS=0")
	return append(env, extra...)
}

// gitRC runs git and returns stdout and the exit code. err is non-nil only if
// git could not run or was killed.
func (m *Manager) gitRC(ctx context.Context, dir string, env []string, args ...string) (string, int, string, error) {
	argv := append(append([]string{"git"}, gitBaseArgs...), args...)
	res, err := exec.Run(ctx, exec.Cmd{Argv: argv, Dir: dir, Env: m.gitEnv(env), Timeout: m.cfg.GitTimeout})
	if err != nil {
		return "", -1, "", err
	}
	if res.Signal != "" || res.TimedOut || res.Cancelled {
		return "", -1, string(res.Stderr), fmt.Errorf("git %s: interrupted (signal=%q timeout=%v cancelled=%v)",
			strings.Join(args, " "), res.Signal, res.TimedOut, res.Cancelled)
	}
	return string(res.Stdout), res.ExitCode, string(res.Stderr), nil
}

// git runs git and requires exit 0. Output is returned untrimmed.
func (m *Manager) git(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	out, code, stderr, err := m.gitRC(ctx, dir, env, args...)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return out, &GitError{Args: args, ExitCode: code, Stderr: stderr}
	}
	return out, nil
}

func (m *Manager) git1(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := m.git(ctx, dir, nil, args...)
	return strings.TrimSpace(out), err
}

// revParse resolves rev to an object id; ok=false if it does not exist.
func (m *Manager) revParse(ctx context.Context, dir, rev string) (string, bool, error) {
	out, code, _, err := m.gitRC(ctx, dir, nil, "rev-parse", "--verify", "--quiet", rev)
	if err != nil {
		return "", false, err
	}
	if code != 0 {
		return "", false, nil
	}
	return strings.TrimSpace(out), true, nil
}
