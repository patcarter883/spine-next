// Package repoctx loads the repository context given to agents: the module
// map (.spine/modules.yaml) and a short factual summary (.spine/context.md:
// build and test commands, layout, conventions, no-go areas). Both are read
// from the base commit, never from a task's worktree, and both are capped
// in tokens: this is a few hundred words of facts, not reference docs.
package repoctx

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strings"

	"github.com/patcarter883/spine-next/internal/gitro"
	"github.com/patcarter883/spine-next/internal/modmap"
)

// NotesPath is the summary's location.
const NotesPath = ".spine/context.md"

// Defaults for the caps (≈ tokens; 4 bytes a token).
const (
	DefaultNotesTokens   = 1200
	DefaultModulesTokens = 1500
	DefaultStaleAfter    = 200
)

// Tokens estimates a text's token count.
func Tokens(s string) int { return (len(s) + 3) / 4 }

// Context is what one revision provides.
type Context struct {
	Notes     string      // context.md, at most the cap
	Truncated bool        // context.md was over the cap
	NotesSize int         // its estimated tokens before truncation
	Modules   *modmap.Map // nil if absent
	ModErr    error       // modules.yaml present but invalid
}

// Load reads both files at rev. Absent files are not errors.
func Load(ctx context.Context, repo, rev string, notesCap int) (Context, error) {
	if notesCap <= 0 {
		notesCap = DefaultNotesTokens
	}
	var c Context
	b, ok, err := (gitro.Repo{Dir: repo}).Show(ctx, rev, NotesPath)
	if err != nil {
		return c, err
	}
	if ok {
		s := strings.TrimSpace(string(b))
		c.NotesSize = Tokens(s)
		if c.NotesSize > notesCap {
			s, c.Truncated = s[:notesCap*4], true
			if i := strings.LastIndexByte(s, '\n'); i > len(s)/2 {
				s = s[:i]
			}
		}
		c.Notes = s
	}
	m, ok, err := modmap.Load(ctx, repo, rev)
	if ok && err == nil {
		c.Modules = m
	} else if err != nil {
		c.ModErr = err
	}
	return c, nil
}

// Control reports whether a task is in the experiment's control arm (run
// without context): a stable hash of its id below fraction.
func Control(task string, fraction float64) bool {
	if fraction <= 0 {
		return false
	}
	h := sha256.Sum256([]byte(task))
	return float64(binary.BigEndian.Uint32(h[:4]))/float64(1<<32) < fraction
}

// Staleness counts commits on rev since the map was generated (-1 unknown).
func Staleness(ctx context.Context, repo, rev string, m *modmap.Map) int {
	if m == nil || m.Generated.Commit == "" {
		return -1
	}
	// Only commits that change code count: committing the map itself (or
	// other .spine/ files) does not make it stale.
	out, err := (gitro.Repo{Dir: repo}).Line(ctx, "rev-list", "--count", m.Generated.Commit+".."+rev, "--", ".", ":(exclude).spine")
	if err != nil {
		return -1
	}
	var n int
	fmt.Sscanf(out, "%d", &n)
	return n
}
