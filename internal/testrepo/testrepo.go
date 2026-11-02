// Package testrepo builds throwaway git repositories for tests.
package testrepo

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Repo is a scratch repository on branch main.
type Repo struct {
	t   testing.TB
	Dir string
}

// New creates an empty repository.
func New(t testing.TB) *Repo {
	t.Helper()
	r := &Repo{t: t, Dir: t.TempDir()}
	r.Git("init", "-q", "-b", "main")
	return r
}

// Git runs git in the repository and returns trimmed output.
func (r *Repo) Git(args ...string) string {
	r.t.Helper()
	c := exec.Command("git", append([]string{"-c", "core.hooksPath=/dev/null"}, args...)...)
	c.Dir = r.Dir
	c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t",
		"GIT_COMMITTER_EMAIL=t@t", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := c.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// Write sets file contents (relative path).
func (r *Repo) Write(files map[string]string) {
	r.t.Helper()
	for rel, content := range files {
		p := filepath.Join(r.Dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			r.t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			r.t.Fatal(err)
		}
	}
}

// Remove deletes files.
func (r *Repo) Remove(paths ...string) {
	for _, p := range paths {
		os.Remove(filepath.Join(r.Dir, filepath.FromSlash(p)))
	}
}

// Commit stages everything and commits; it returns the commit OID.
func (r *Repo) Commit(msg string) string {
	r.t.Helper()
	r.Git("add", "-A")
	r.Git("commit", "-q", "--allow-empty", "-m", msg)
	return r.Git("rev-parse", "HEAD")
}

// Tree returns rev's tree OID.
func (r *Repo) Tree(rev string) string { return r.Git("rev-parse", rev+"^{tree}") }

// Branch creates and checks out a branch at rev.
func (r *Repo) Branch(name, rev string) { r.Git("checkout", "-q", "-B", name, rev) }
