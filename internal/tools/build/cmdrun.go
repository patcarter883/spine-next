// Package build implements cmd.run and its specializations go.build, go.vet
// and go.test (RFC §5.1). cmd.run executes an argv in a directory whose
// content is exactly the evaluated tree, then turns the output into
// Diagnostics with a named parser. It is Hermetic: cached by tree, args and
// an environment key (the resolved binary plus pass-through variables).
package build

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/patcarter883/spine-next/internal/artifacts"
	"github.com/patcarter883/spine-next/internal/exec"
	"github.com/patcarter883/spine-next/internal/tools"
)

// Runner holds the execution environment shared by cmd.run and the go.*
// tools.
type Runner struct {
	// PassEnv lists parent variables passed to tools (default DefaultPassEnv).
	PassEnv []string
	// Artifacts, when set, receives the full stdout+stderr of every run.
	Artifacts *artifacts.Store
	// ScratchDir holds per-run HOME/TMPDIR directories (default os.TempDir()).
	ScratchDir string
	// MaxOutput caps captured stdout and stderr each (default 8 MiB).
	MaxOutput int
}

// DefaultPassEnv is the allow-list of parent variables a tool sees. GIT_* is
// never passed, so a tool cannot redirect GIT_DIR (RFC §5.3).
var DefaultPassEnv = []string{
	"PATH", "LANG", "LC_ALL", "TZ", "USER", "TERM",
	"GOCACHE", "GOMODCACHE", "GOPATH", "GOFLAGS", "GOTOOLCHAIN", "GOPROXY", "GONOSUMDB", "GOPRIVATE", "GOSUMDB",
	"CGO_ENABLED", "CC", "CXX",
	"VIRTUAL_ENV", "PYTHONPATH", "PIP_CACHE_DIR", "UV_CACHE_DIR",
	"NODE_PATH", "npm_config_cache",
	"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "SSL_CERT_FILE",
}

// Tools returns cmd.run, go.build, go.vet and go.test sharing r.
func (r *Runner) Tools() []tools.Tool {
	return []tools.Tool{&CmdRun{r: r}, goTool(r, "go.build"), goTool(r, "go.vet"), goTool(r, "go.test")}
}

// Args are cmd.run's arguments.
type Args struct {
	Argv   []string          `json:"argv"`
	Parser string            `json:"parser,omitempty"`
	Dir    string            `json:"dir,omitempty"` // relative to the tree root
	Env    map[string]string `json:"env,omitempty"`
	// Report is a file the parser reads instead of stdout (e.g. a JUnit
	// file). "{tmp}" expands to the run's scratch directory.
	Report string `json:"report,omitempty"`
	// OKExit lists exit codes that count as success (default [0]).
	OKExit []int        `json:"ok_exit,omitempty"`
	Regex  *RegexParser `json:"regex,omitempty"`
}

const cmdRunSchema = `{
  "type": "object",
  "required": ["argv"],
  "additionalProperties": false,
  "properties": {
    "argv":   {"type": "array", "minItems": 1, "items": {"type": "string"}},
    "parser": {"enum": ["none", "go-build", "gotest-json", "pytest-junit", "eslint-json", "tsc", "ruff-json", "php-lint", "regex"]},
    "dir":    {"type": "string", "pattern": "^[^/]"},
    "env":    {"type": "object", "propertyNames": {"pattern": "^[A-Za-z_][A-Za-z0-9_]*$", "not": {"pattern": "^GIT_"}}, "additionalProperties": {"type": "string"}},
    "report": {"type": "string"},
    "ok_exit": {"type": "array", "items": {"type": "integer"}},
    "regex":  {"type": "object", "required": ["pattern"], "additionalProperties": false,
               "properties": {"pattern": {"type": "string"}, "severity": {"enum": ["error", "warning", "info"]}, "code": {"type": "string"}}}
  }
}`

// CmdRun is the generic cmd.run tool.
type CmdRun struct{ r *Runner }

func (c *CmdRun) Spec() tools.Spec {
	return tools.Spec{Name: "cmd.run", Version: "1", Determinism: tools.Hermetic, NeedsDir: true,
		Timeout: 10 * time.Minute, InputSchema: json.RawMessage(cmdRunSchema)}
}

func (c *CmdRun) EnvKey(ctx context.Context, inv tools.Invocation) (string, error) {
	var a Args
	if err := json.Unmarshal(inv.Args, &a); err != nil {
		return "", err
	}
	return c.r.envKey(a.Argv[0])
}

func (c *CmdRun) Run(ctx context.Context, inv tools.Invocation) (tools.Result, error) {
	var a Args
	if err := json.Unmarshal(inv.Args, &a); err != nil {
		return tools.Result{}, err
	}
	return c.r.run(ctx, inv.Dir, a)
}

// envKey identifies the binary that will run and the environment values that
// can change its behaviour. Replacing the toolchain invalidates the cache.
func (r *Runner) envKey(prog string) (string, error) {
	h := sha256.New()
	path, err := lookPath(prog, os.Getenv("PATH"))
	if err != nil {
		fmt.Fprintf(h, "missing:%s\n", prog) // the run will fail as infra; keep the key stable
	} else if fi, err := os.Stat(path); err == nil {
		fmt.Fprintf(h, "%s\x00%d\x00%d\n", path, fi.Size(), fi.ModTime().UnixNano())
	}
	for _, k := range r.passEnv() {
		if k == "PATH" || strings.HasSuffix(k, "PROXY") {
			continue // PATH only matters through the resolved binary
		}
		if v, ok := os.LookupEnv(k); ok {
			fmt.Fprintf(h, "%s=%s\n", k, v)
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func lookPath(prog, pathEnv string) (string, error) {
	if strings.Contains(prog, "/") {
		return prog, nil
	}
	for _, d := range filepath.SplitList(pathEnv) {
		p := filepath.Join(d, prog)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", osexec.ErrNotFound
}

func (r *Runner) passEnv() []string {
	if r.PassEnv != nil {
		return r.PassEnv
	}
	return DefaultPassEnv
}

// env builds the child environment: allow-listed parent variables, cache
// locations pinned to the real user's (HOME is replaced, which would
// otherwise cold-start every compiler cache), a per-run HOME and TMPDIR, and
// the gate's own variables.
func (r *Runner) env(scratch string, extra map[string]string) []string {
	var env []string
	for _, k := range r.passEnv() {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	has := func(k string) bool { _, ok := os.LookupEnv(k); return ok }
	if cache, err := os.UserCacheDir(); err == nil {
		if !has("GOCACHE") {
			env = append(env, "GOCACHE="+filepath.Join(cache, "go-build"))
		}
		if !has("XDG_CACHE_HOME") {
			env = append(env, "XDG_CACHE_HOME="+cache)
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		if !has("GOPATH") {
			env = append(env, "GOPATH="+filepath.Join(home, "go"))
		}
		if !has("GOMODCACHE") && !has("GOPATH") {
			env = append(env, "GOMODCACHE="+filepath.Join(home, "go", "pkg", "mod"))
		}
	}
	home := filepath.Join(scratch, "home")
	tmp := filepath.Join(scratch, "tmp")
	os.MkdirAll(home, 0o700)
	os.MkdirAll(tmp, 0o700)
	env = append(env, "HOME="+home, "TMPDIR="+tmp, "CI=1", "NO_COLOR=1", "FORCE_COLOR=0", "PYTHONDONTWRITEBYTECODE=1")
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}

func (r *Runner) run(ctx context.Context, root string, a Args) (tools.Result, error) {
	dir := root
	if a.Dir != "" {
		dir = filepath.Join(root, filepath.FromSlash(a.Dir))
		if rel, err := filepath.Rel(root, dir); err != nil || strings.HasPrefix(rel, "..") {
			return tools.Result{}, fmt.Errorf("cmd.run: dir %q escapes the tree", a.Dir)
		}
	}
	scratchRoot := r.ScratchDir
	if scratchRoot == "" {
		scratchRoot = os.TempDir()
	}
	if err := os.MkdirAll(scratchRoot, 0o755); err != nil {
		return tools.Result{}, err
	}
	scratch, err := os.MkdirTemp(scratchRoot, "spine-run-*")
	if err != nil {
		return tools.Result{}, err
	}
	defer os.RemoveAll(scratch)
	expand := func(s string) string { return strings.ReplaceAll(s, "{tmp}", scratch) }
	argv := make([]string, len(a.Argv))
	for i, s := range a.Argv {
		argv[i] = expand(s)
	}
	env := r.env(scratch, a.Env)
	if _, err := lookPath(argv[0], envValue(env, "PATH")); err != nil {
		return tools.Result{}, fmt.Errorf("cmd.run: %s: %w", argv[0], err)
	}

	maxOut := r.MaxOutput
	if maxOut <= 0 {
		maxOut = 8 << 20
	}
	var art *artifacts.Writer
	if r.Artifacts != nil {
		if art, err = r.Artifacts.Create(); err != nil {
			return tools.Result{}, err
		}
		defer art.Abort()
	}
	var artMu sync.Mutex
	stdout := &capture{max: maxOut, mu: &artMu}
	stderr := &capture{max: maxOut, mu: &artMu}
	if art != nil { // a nil *Writer in an io.Writer field is not nil
		stdout.tee, stderr.tee = art, art
	}
	// The registry's deadline becomes a Timeout verdict, not an error: exec
	// gets it as its own timeout, and a ctx deadline that wins the race is
	// treated the same way below.
	deadline, hasDeadline := ctx.Deadline()
	var timeout time.Duration
	if hasDeadline {
		timeout = time.Until(deadline)
		if timeout <= 0 {
			return tools.Result{}, context.DeadlineExceeded
		}
	}
	er, err := exec.Run(ctx, exec.Cmd{
		Argv: argv, Dir: dir, Env: env, Timeout: timeout, Stdout: stdout, Stderr: stderr,
	})
	if err != nil {
		return tools.Result{}, fmt.Errorf("cmd.run: %w", err)
	}
	if er.Cancelled && ctx.Err() != nil && !errorsIsDeadline(ctx) {
		return tools.Result{}, ctx.Err()
	}
	res := tools.Result{Duration: er.Duration, Truncated: stdout.over || stderr.over}
	if art != nil {
		if ref, err := art.Commit(); err == nil {
			res.Artifacts = append(res.Artifacts, tools.ArtifactRef{Name: "output", Digest: ref.Digest, Size: ref.Size})
		}
	}
	if er.TimedOut || (er.Cancelled && errorsIsDeadline(ctx)) {
		res.ExitCode = 124
		res.NoCache = true // a timeout is not a function of the tree
		res.Diagnostics = []tools.Diagnostic{{Severity: tools.Error, Code: "Timeout",
			Message: fmt.Sprintf("%s did not finish within %s", strings.Join(a.Argv, " "), timeout.Round(time.Second)),
			Fingerprint: tools.Fingerprint("cmd.run", "Timeout", strings.Join(a.Argv, " "))}}
		return res, nil
	}
	if er.Signal != "" {
		// Killed by something other than our deadline (OOM killer, operator):
		// the check did not run to completion. That is an infrastructure
		// fault, not a verdict.
		return tools.Result{}, fmt.Errorf("cmd.run: %s killed by %s", argv[0], er.Signal)
	}
	ok := er.ExitCode == 0
	for _, c := range a.OKExit {
		if er.ExitCode == c {
			ok = true
		}
	}
	if !ok {
		res.ExitCode = er.ExitCode
		if res.ExitCode == 0 {
			res.ExitCode = 1
		}
	}
	var report []byte
	if a.Report != "" {
		p := expand(a.Report)
		if !filepath.IsAbs(p) {
			p = filepath.Join(dir, p)
		}
		report, _ = os.ReadFile(p) // a missing report leaves failures unparsed
	}
	in := ParseInput{Stdout: stdout.buf.Bytes(), Stderr: stderr.buf.Bytes(), Report: report, Root: root, Dir: dir, Regex: a.Regex}
	p := Parsers[a.Parser]
	if a.Parser == "" || a.Parser == "none" {
		p = nil
	}
	if p != nil {
		ds, metrics, perr := p(in)
		if perr == nil {
			res.Diagnostics, res.Metrics = ds, metrics
		} else {
			res.Unparsed = res.ExitCode != 0
		}
	}
	return res, nil
}

func envValue(env []string, k string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if v, ok := strings.CutPrefix(env[i], k+"="); ok {
			return v
		}
	}
	return ""
}

func errorsIsDeadline(ctx context.Context) bool { return ctx.Err() == context.DeadlineExceeded }

// capture keeps the first max bytes of a stream and tees everything into the
// artifact writer.
type capture struct {
	max  int
	buf  bytes.Buffer
	over bool
	tee  io.Writer
	mu   *sync.Mutex
}

func (c *capture) Write(p []byte) (int, error) {
	if c.tee != nil {
		c.mu.Lock()
		c.tee.Write(p)
		c.mu.Unlock()
	}
	room := c.max - c.buf.Len()
	if room <= 0 {
		c.over = c.over || len(p) > 0
		return len(p), nil
	}
	if len(p) > room {
		c.buf.Write(p[:room])
		c.over = true
		return len(p), nil
	}
	return c.buf.Write(p)
}
