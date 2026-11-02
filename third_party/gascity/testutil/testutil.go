// Package testutil is the minimal subset of Gas City's internal/testutil
// (gastownhall/gascity@4ebacb1) that the vendored package tests use.
package testutil

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/patcarter883/spine-next/third_party/gascity/pathutil"
)

// CanonicalPath normalizes a filesystem path for comparison.
func CanonicalPath(path string) string { return pathutil.NormalizePathForCompare(path) }

// InitGitRepo creates a test-owned repository with one commit and returns its
// path and initial branch.
func InitGitRepo(t testing.TB) (string, string) {
	t.Helper()
	dir := t.TempDir()
	RunGit(t, dir, "init")
	RunGit(t, dir, "config", "user.email", "test@test.com")
	RunGit(t, dir, "config", "user.name", "Test")
	RunGit(t, dir, "commit", "--allow-empty", "-m", "init")
	return dir, RunGit(t, dir, "rev-parse", "--abbrev-ref", "HEAD")
}

// RunGit runs git with repository-locating environment variables removed.
func RunGit(t testing.TB, dir string, args ...string) string {
	t.Helper()
	var env []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_COMMON_DIR":
			continue
		}
		env = append(env, entry)
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %s: %v", strings.Join(args, " "), out, err)
	}
	return strings.TrimSpace(string(out))
}
