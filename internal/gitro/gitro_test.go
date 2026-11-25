package gitro

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/patcarter883/spine-next/internal/testrepo"
)

func TestFsmonitorInRepoConfigNeverRuns(t *testing.T) {
	r := testrepo.New(t)
	r.Write(map[string]string{"a.txt": "one\n"})
	r.Commit("init")
	marker := filepath.Join(t.TempDir(), "ran")
	hook := filepath.Join(t.TempDir(), "fsmonitor.sh")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\necho ran >> "+marker+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	r.Git("config", "core.fsmonitor", hook)
	r.Git("status", "--porcelain")
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("fixture's fsmonitor hook did not run under plain git: %v", err)
	}
	os.Remove(marker)

	r.Write(map[string]string{"a.txt": "two\n"})
	repo := Repo{Dir: r.Dir}
	if _, err := repo.Run(context.Background(), "status", "--porcelain"); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Run(context.Background(), "diff-files", "--quiet"); err == nil {
		t.Fatal("diff-files should report the modified file")
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("gitro ran the repository's core.fsmonitor hook: %v", err)
	}
}
