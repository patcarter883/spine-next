// Package tools is the deterministic tool registry (RFC §3.3, §5). A tool
// returns a structured Result and nothing else; an error means the tool could
// not run (an infrastructure fault), never that a check failed.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/patcarter883/spine-next/internal/api"
)

// Determinism decides cacheability.
type Determinism int

const (
	// Pure: output = f(tree, args[, base]). Cached by tree OID.
	Pure Determinism = iota
	// Hermetic: pure but environment-sensitive (compilers, test runners).
	// Cached with an environment fingerprint.
	Hermetic
	// Effectful: never cached.
	Effectful
)

func (d Determinism) String() string {
	switch d {
	case Pure:
		return "pure"
	case Hermetic:
		return "hermetic"
	default:
		return "effectful"
	}
}

// Spec describes a tool.
type Spec struct {
	Name        string          // "go.test", "ts.api_contract", "diff.scope"
	Version     string          // bump to invalidate cached results
	InputSchema json.RawMessage // JSON Schema (draft 2020-12); args are validated before Run
	Determinism Determinism
	Timeout     time.Duration
	// ReadOnly tools may run concurrently on the same directory. Others take
	// that directory's write lock.
	ReadOnly bool
	// UsesBase marks tools whose output depends on the base commit (diffs,
	// API contracts, preflight). The base OID is then part of the cache key.
	UsesBase bool
	// NeedsDir marks tools that run against files on disk (cmd.run). The
	// others read git objects only.
	NeedsDir bool
	// MaxConcurrent bounds parallel runs of this tool (0 = global limit only).
	MaxConcurrent int
}

// Invocation is one request to run a tool against a tree.
type Invocation struct {
	// Repo is a directory git can resolve objects from (the primary checkout
	// or any linked worktree).
	Repo string
	// Tree is the tree OID being evaluated. Required for cacheable tools.
	Tree string
	// Commit is the snapshot commit whose tree is Tree (optional; diff tools
	// use it to find the merge base with Base).
	Commit string
	// Base is the base commit OID the change is measured against.
	Base string
	// BaseKey, when set, replaces Base and Commit in the cache key of
	// UsesBase tools. The gate engine sets it to "<merge-base tree>
	// <base tree>": base-dependent results are functions of those trees and
	// Tree, so identical content under different commits shares results.
	BaseKey string
	// Dir is a directory whose content is exactly Tree, for NeedsDir tools.
	// If empty, Materialize is called to obtain one, only on a cache miss.
	Dir         string
	Materialize func(ctx context.Context) (string, error)
	// MaterializeTree checks out any tree for the evaluation, with the same
	// dependency links, for tools that judge a variant of Tree.
	MaterializeTree func(ctx context.Context, tree string) (string, error)
	Args            json.RawMessage
	// Timeout overrides Spec.Timeout when positive.
	Timeout time.Duration
	// BaselineOf, on a baseline run, is "<from tree> <to tree>" of the change
	// being judged: {changed} expands to that change's files that exist in
	// Tree, so the base is checked on the same paths.
	BaselineOf string
	// Recheck runs a Hermetic tool again instead of returning a cached
	// failure: a test suite that failed once may pass on the same tree.
	Recheck bool
}

// WorkDir returns the directory a NeedsDir tool must run in.
func (inv Invocation) WorkDir(ctx context.Context) (string, error) {
	if inv.Dir != "" {
		return inv.Dir, nil
	}
	if inv.Materialize == nil {
		return "", ErrNoDir
	}
	return inv.Materialize(ctx)
}

// Diagnostic and Severity are the api types, re-exported for tool code.
type (
	Diagnostic = api.Diagnostic
	Severity   = api.Severity
)

const (
	Error   = api.SeverityError
	Warning = api.SeverityWarning
	Info    = api.SeverityInfo
)

// ArtifactRef points at a full log or payload in the artifact store.
type ArtifactRef = api.ArtifactRef

// Result is the only thing a tool returns. There is no free-form chat.
type Result struct {
	Tool        string             `json:"tool"`
	ExitCode    int                `json:"exit_code"` // 0 = pass; the gate keys on this
	Diagnostics []Diagnostic       `json:"diagnostics"`
	Metrics     map[string]float64 `json:"metrics,omitempty"`
	// Data is a typed, tool-specific payload (e.g. ts.symbols' symbol table).
	Data      json.RawMessage `json:"data,omitempty"`
	Artifacts []ArtifactRef   `json:"artifacts,omitempty"`
	Duration  time.Duration   `json:"duration_ns"`
	Cached    bool            `json:"cached"`
	Truncated bool            `json:"truncated"` // output hit a cap
	// Unparsed is set when the tool failed but its output could not be
	// turned into diagnostics; baseline policy never excuses it.
	Unparsed bool `json:"unparsed,omitempty"`
	// OutputTail is the end of a failing run's output when the run produced
	// no error diagnostics, so whoever acts on the failure can see why.
	OutputTail string `json:"output_tail,omitempty"`
	// NoCache marks results that must not be cached even for a cacheable
	// tool (e.g. a run cut off by its timeout).
	NoCache bool `json:"-"`
}

// Tool is a deterministic check.
type Tool interface {
	Spec() Spec
	// Run executes the tool. err != nil means the tool could not run
	// (binary missing, git failure, OOM): an infrastructure fault that the
	// reconciler retries without spending a patch round.
	Run(ctx context.Context, inv Invocation) (Result, error)
}

// EnvKeyer is implemented by Hermetic tools whose result depends on the
// environment (e.g. the resolved compiler binary). The key is folded into
// the cache key.
type EnvKeyer interface {
	EnvKey(ctx context.Context, inv Invocation) (string, error)
}

var (
	ErrUnknownTool = errors.New("tools: unknown tool")
	ErrInvalidArgs = errors.New("tools: invalid arguments")
	ErrNoTree      = errors.New("tools: cacheable tool invoked without a tree OID")
	ErrNoDir       = errors.New("tools: tool needs a directory and none was provided")
	ErrDuplicate   = errors.New("tools: tool already registered")
)

// Limits applied by the registry to every result.
const (
	MaxMessageBytes    = 2 << 10
	MaxDiagnostics     = 500
	MaxOutputTailBytes = 4 << 10
)
