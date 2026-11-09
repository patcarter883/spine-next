// Package gitro runs git commands that read repository objects and never
// change repository state: no refs, no index of any checkout, no config.
//
// The tool registry and gate engine use it (RFC §2: "Tools may run read-only
// git through exec"). Every call uses Gas City's hermetic environment
// (GIT_CONFIG_NOSYSTEM, GIT_CONFIG_GLOBAL=/dev/null, discovery variables
// scrubbed), GIT_OPTIONAL_LOCKS=0, and the same hook/gc suppression as
// internal/worktree. Materialize is the one helper that writes files, and it
// writes only into a caller-owned directory through a temporary index;
// EditTree adds tree objects, never refs.
package gitro

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/patcarter883/spine-next/internal/exec"
	gcgit "github.com/patcarter883/spine-next/third_party/gascity/git"
)

// DefaultTimeout bounds a single read-only git call.
const DefaultTimeout = 2 * time.Minute

// Error is a git invocation that ran and exited non-zero.
type Error struct {
	Args     []string
	ExitCode int
	Stderr   string
}

func (e *Error) Error() string {
	return fmt.Sprintf("git %s: exit %d: %s", strings.Join(e.Args, " "), e.ExitCode, strings.TrimSpace(e.Stderr))
}

var baseArgs = []string{"-c", "gc.auto=0", "-c", "maintenance.auto=false", "-c", "core.hooksPath=/dev/null"}

// Env returns the environment for read-only git calls, plus extra entries.
func Env(extra ...string) []string {
	env := gcgit.HermeticEnv()
	env = append(env, "GIT_TERMINAL_PROMPT=0", "LC_ALL=C", "GIT_OPTIONAL_LOCKS=0")
	return append(env, extra...)
}

// Repo is a handle on a repository directory (a checkout, linked worktree or
// bare git dir). It holds no state beyond the path.
type Repo struct {
	Dir     string
	Timeout time.Duration
}

// RunRC runs git and returns stdout, exit code and stderr. err is non-nil only
// when git could not run or was interrupted.
func (r Repo) RunRC(ctx context.Context, env []string, stdin []byte, args ...string) ([]byte, int, string, error) {
	t := r.Timeout
	if t <= 0 {
		t = DefaultTimeout
	}
	c := exec.Cmd{
		Argv:      append(append([]string{"git"}, baseArgs...), args...),
		Dir:       r.Dir,
		Env:       Env(env...),
		Timeout:   t,
		MaxOutput: 256 << 20,
	}
	if stdin != nil {
		c.Stdin = stdin
	}
	res, err := exec.Run(ctx, c)
	if err != nil {
		return nil, -1, "", err
	}
	if res.Signal != "" || res.TimedOut || res.Cancelled {
		if ctx.Err() != nil {
			return nil, -1, "", ctx.Err()
		}
		return nil, -1, string(res.Stderr), fmt.Errorf("git %s: interrupted (signal=%q timeout=%v)", strings.Join(args, " "), res.Signal, res.TimedOut)
	}
	if res.Truncated {
		return nil, -1, "", fmt.Errorf("git %s: output exceeded cap", strings.Join(args, " "))
	}
	return res.Stdout, res.ExitCode, string(res.Stderr), nil
}

// Run runs git and requires exit 0.
func (r Repo) Run(ctx context.Context, args ...string) ([]byte, error) {
	out, code, stderr, err := r.RunRC(ctx, nil, nil, args...)
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, &Error{Args: args, ExitCode: code, Stderr: stderr}
	}
	return out, nil
}

// Line runs git and returns trimmed stdout.
func (r Repo) Line(ctx context.Context, args ...string) (string, error) {
	out, err := r.Run(ctx, args...)
	return strings.TrimSpace(string(out)), err
}

// TreeOf resolves rev to a tree OID.
func (r Repo) TreeOf(ctx context.Context, rev string) (string, error) {
	return r.Line(ctx, "rev-parse", "--verify", "-q", rev+"^{tree}")
}

// MergeBase returns the merge base of a and b.
func (r Repo) MergeBase(ctx context.Context, a, b string) (string, error) {
	return r.Line(ctx, "merge-base", a, b)
}

// Change is one entry of a tree diff.
type Change struct {
	Status  byte   // A, M, D, R, T (copies are reported as A)
	Path    string // new path (old path for D)
	OldPath string // set for R
	OldOID  string
	NewOID  string
	OldMode string
	NewMode string
}

// DiffTrees lists changes from tree a to tree b, with rename detection.
func (r Repo) DiffTrees(ctx context.Context, a, b string) ([]Change, error) {
	out, err := r.Run(ctx, "diff-tree", "-r", "-z", "--no-commit-id", "-M", "--raw", a, b)
	if err != nil {
		return nil, err
	}
	return parseRawZ(out)
}

func parseRawZ(out []byte) ([]Change, error) {
	f := bytes.Split(out, []byte{0})
	var cs []Change
	for i := 0; i < len(f); i++ {
		meta := string(f[i])
		if meta == "" {
			continue
		}
		if meta[0] != ':' {
			return nil, fmt.Errorf("gitro: unexpected diff-tree record %q", meta)
		}
		p := strings.Fields(meta[1:])
		if len(p) != 5 || i+1 >= len(f) {
			return nil, fmt.Errorf("gitro: malformed diff-tree record %q", meta)
		}
		c := Change{OldMode: p[0], NewMode: p[1], OldOID: p[2], NewOID: p[3], Status: p[4][0]}
		i++
		switch c.Status {
		case 'R', 'C':
			if i+1 >= len(f) {
				return nil, fmt.Errorf("gitro: malformed rename record %q", meta)
			}
			c.OldPath, c.Path = string(f[i]), string(f[i+1])
			i++
			if c.Status == 'C' {
				c.Status, c.OldPath = 'A', ""
			}
		default:
			c.Path = string(f[i])
		}
		cs = append(cs, c)
	}
	return cs, nil
}

// Entry is one blob in a tree listing.
type Entry struct {
	Mode string
	OID  string
	Size int64
	Path string
}

// LsTree lists every blob (recursively) in tree.
func (r Repo) LsTree(ctx context.Context, tree string) ([]Entry, error) {
	out, err := r.Run(ctx, "ls-tree", "-r", "-z", "--long", tree)
	if err != nil {
		return nil, err
	}
	var es []Entry
	for _, rec := range bytes.Split(out, []byte{0}) {
		if len(rec) == 0 {
			continue
		}
		meta, path, ok := bytes.Cut(rec, []byte{'\t'})
		if !ok {
			return nil, fmt.Errorf("gitro: malformed ls-tree record %q", rec)
		}
		p := strings.Fields(string(meta))
		if len(p) != 4 || p[1] != "blob" {
			continue // submodules (commit) are not files
		}
		size, _ := strconv.ParseInt(p[3], 10, 64)
		es = append(es, Entry{Mode: p[0], OID: p[2], Size: size, Path: string(path)})
	}
	return es, nil
}

// ErrBlobTooLarge is returned by Blobs for objects above the limit.
var ErrBlobTooLarge = errors.New("gitro: blob exceeds size limit")

// Blobs reads the named blobs in one cat-file --batch process. Missing
// objects are an error.
func (r Repo) Blobs(ctx context.Context, oids []string) (map[string][]byte, error) {
	res := make(map[string][]byte, len(oids))
	if len(oids) == 0 {
		return res, nil
	}
	in := []byte(strings.Join(oids, "\n") + "\n")
	out, code, stderr, err := r.RunRC(ctx, nil, in, "cat-file", "--batch")
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, &Error{Args: []string{"cat-file", "--batch"}, ExitCode: code, Stderr: stderr}
	}
	br := bufio.NewReader(bytes.NewReader(out))
	for range oids {
		hdr, err := br.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("gitro: cat-file: %w", err)
		}
		p := strings.Fields(hdr)
		if len(p) == 2 && p[1] == "missing" {
			return nil, fmt.Errorf("gitro: object %s missing", p[0])
		}
		if len(p) != 3 {
			return nil, fmt.Errorf("gitro: cat-file header %q", hdr)
		}
		n, err := strconv.Atoi(p[2])
		if err != nil {
			return nil, err
		}
		buf := make([]byte, n+1) // content + trailing LF
		if _, err := io.ReadFull(br, buf); err != nil {
			return nil, fmt.Errorf("gitro: cat-file body: %w", err)
		}
		res[p[0]] = buf[:n]
	}
	return res, nil
}

// Show returns the content of rev:path, and false if the path does not exist
// in rev. rev must be a valid revision.
func (r Repo) Show(ctx context.Context, rev, path string) ([]byte, bool, error) {
	oid, code, stderr, err := r.RunRC(ctx, nil, nil, "rev-parse", "--verify", "-q", rev+":"+path)
	if err != nil {
		return nil, false, err
	}
	if code == 1 {
		return nil, false, nil
	}
	if code != 0 {
		return nil, false, &Error{Args: []string{"rev-parse", rev + ":" + path}, ExitCode: code, Stderr: stderr}
	}
	out, err := r.Run(ctx, "cat-file", "blob", strings.TrimSpace(string(oid)))
	return out, err == nil, err
}

// TreeEdit sets one path in a tree to a blob, or removes it when OID is "".
type TreeEdit struct {
	Path, Mode, OID string
}

// EditTree writes the tree that is tree with edits applied and returns its
// OID. It adds tree objects to the object store and touches no ref or index.
func (r Repo) EditTree(ctx context.Context, tree string, edits []TreeEdit) (string, error) {
	idx, err := os.CreateTemp("", "spine-edit-*.idx")
	if err != nil {
		return "", err
	}
	idxPath := idx.Name()
	idx.Close()
	os.Remove(idxPath)
	defer os.Remove(idxPath)
	env := []string{"GIT_INDEX_FILE=" + idxPath}
	var info bytes.Buffer
	for _, e := range edits {
		if e.OID == "" {
			fmt.Fprintf(&info, "0 %s\t%s\n", strings.Repeat("0", 40), e.Path)
		} else {
			fmt.Fprintf(&info, "%s %s\t%s\n", e.Mode, e.OID, e.Path)
		}
	}
	for _, step := range []struct {
		stdin []byte
		args  []string
	}{
		{nil, []string{"read-tree", tree}},
		{info.Bytes(), []string{"update-index", "--index-info"}},
	} {
		if _, code, stderr, err := r.RunRC(ctx, env, step.stdin, step.args...); err != nil {
			return "", err
		} else if code != 0 {
			return "", &Error{Args: step.args, ExitCode: code, Stderr: stderr}
		}
	}
	out, code, stderr, err := r.RunRC(ctx, env, nil, "write-tree")
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", &Error{Args: []string{"write-tree"}, ExitCode: code, Stderr: stderr}
	}
	return strings.TrimSpace(string(out)), nil
}

// Materialize writes the files of tree into dir, which must be empty or
// absent. It uses a temporary index outside the repository, so no checkout's
// index or any ref is touched. Files are written read-write; dir is the
// caller's to remove.
func (r Repo) Materialize(ctx context.Context, tree, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if ents, err := os.ReadDir(dir); err != nil {
		return err
	} else if len(ents) > 0 {
		return fmt.Errorf("gitro: materialize target %s is not empty", dir)
	}
	idx, err := os.CreateTemp("", "spine-mat-*.idx")
	if err != nil {
		return err
	}
	idxPath := idx.Name()
	idx.Close()
	os.Remove(idxPath) // git wants to create it
	defer os.Remove(idxPath)
	env := []string{"GIT_INDEX_FILE=" + idxPath}
	if _, code, stderr, err := r.RunRC(ctx, env, nil, "read-tree", tree); err != nil {
		return err
	} else if code != 0 {
		return &Error{Args: []string{"read-tree", tree}, ExitCode: code, Stderr: stderr}
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if _, code, stderr, err := r.RunRC(ctx, env, nil, "checkout-index", "-a", "-f", "--prefix="+abs+string(os.PathSeparator)); err != nil {
		return err
	} else if code != 0 {
		return &Error{Args: []string{"checkout-index"}, ExitCode: code, Stderr: stderr}
	}
	return nil
}
