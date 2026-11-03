package api

import (
	"crypto/rand"
	"encoding/hex"
	"regexp"
	"strings"
)

var (
	reSlug   = regexp.MustCompile(`[^a-z0-9]+`)
	reTaskID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
)

// ValidTaskID reports whether id can name a task (it is also the worktree
// lease name).
func ValidTaskID(id TaskID) bool { return reTaskID.MatchString(string(id)) }

// NewTaskID derives a readable, unique id from the intent:
// "add-input-validation-to-the-sign-3fa91c".
func NewTaskID(intent string) TaskID {
	s := strings.Trim(reSlug.ReplaceAllString(strings.ToLower(intent), "-"), "-")
	if len(s) > 32 {
		s = strings.Trim(s[:32], "-")
	}
	if s == "" {
		s = "task"
	}
	var b [3]byte
	rand.Read(b[:])
	return TaskID(s + "-" + hex.EncodeToString(b[:]))
}

// ValidDependency checks "task" or "repo:task".
func ValidDependency(d TaskID) bool {
	repo, id, cross := strings.Cut(string(d), ":")
	if !cross {
		return ValidTaskID(d)
	}
	return reTaskID.MatchString(repo) && ValidTaskID(TaskID(id))
}
