package ast

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/patcarter883/spine-next/internal/gitro"
	"github.com/patcarter883/spine-next/internal/tools"
	"github.com/patcarter883/spine-next/internal/tools/diff"
)

// MaxFileBytes skips larger files (generated or vendored code).
const MaxFileBytes = 2 << 20

// Tools returns ts.parse, ts.symbols and ts.api_contract on p (nil = Default()).
func Tools(p Parser) []tools.Tool {
	if p == nil {
		p = Default()
	}
	return []tools.Tool{ParseTool{p}, SymbolsTool{p}, ContractTool{p}}
}

type fileArgs struct {
	Languages []string `json:"languages,omitempty"`
	Paths     []string `json:"paths,omitempty"`
	Exclude   []string `json:"exclude,omitempty"`
	All       bool     `json:"all,omitempty"` // every file in the tree, not only changed files
}

const fileArgsProps = `"languages":{"type":"array","items":{"enum":["go","python","typescript","php"]}},
  "paths":{"type":"array","items":{"type":"string"}},
  "exclude":{"type":"array","items":{"type":"string"}},
  "all":{"type":"boolean"}`

func (a fileArgs) want(p string) (Lang, bool) {
	l, ok := LangFor(p)
	if !ok {
		return "", false
	}
	if len(a.Languages) > 0 {
		found := false
		for _, x := range a.Languages {
			if x == l.Family() {
				found = true
			}
		}
		if !found {
			return "", false
		}
	}
	if len(a.Paths) > 0 && !diff.InScope(a.Paths, p) {
		return "", false
	}
	if diff.InScope(a.Exclude, p) || diff.InScope(defaultExclude, p) {
		return "", false
	}
	return l, true
}

var defaultExclude = []string{"**/node_modules/**", "vendor/**", "**/vendor/**", "**/*.min.js", "**/*.d.ts"}

type file struct {
	path string
	oid  string
	lang Lang
}

// targetFiles lists the files to examine on the evaluated tree: changed
// (added, modified, renamed) files by default, or all files with All.
func targetFiles(ctx context.Context, inv tools.Invocation, a fileArgs) ([]file, int, error) {
	r := gitro.Repo{Dir: inv.Repo}
	var fs []file
	skipped := 0
	if a.All {
		es, err := r.LsTree(ctx, inv.Tree)
		if err != nil {
			return nil, 0, err
		}
		for _, e := range es {
			if l, ok := a.want(e.Path); ok {
				if e.Size > MaxFileBytes {
					skipped++
					continue
				}
				fs = append(fs, file{e.Path, e.OID, l})
			}
		}
		return fs, skipped, nil
	}
	cs, _, err := diff.Changes(ctx, inv)
	if err != nil {
		return nil, 0, err
	}
	for _, c := range cs {
		if c.Status == 'D' || c.NewMode == "160000" {
			continue
		}
		if l, ok := a.want(c.Path); ok {
			fs = append(fs, file{c.Path, c.NewOID, l})
		}
	}
	return fs, 0, nil
}

func readAll(ctx context.Context, repo string, fs []file) (map[string][]byte, error) {
	oids := make([]string, 0, len(fs))
	seen := map[string]bool{}
	for _, f := range fs {
		if !seen[f.oid] {
			seen[f.oid] = true
			oids = append(oids, f.oid)
		}
	}
	return gitro.Repo{Dir: repo}.Blobs(ctx, oids)
}

// - ts.parse -

// ParseTool fails on any ERROR or MISSING node in the examined files. It is
// the fast check for truncated or mangled LLM edits.
type ParseTool struct{ p Parser }

func (ParseTool) Spec() tools.Spec {
	return tools.Spec{Name: "ts.parse", Version: "1", Determinism: tools.Pure, ReadOnly: true, UsesBase: true,
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{` + fileArgsProps + `}}`)}
}

func (t ParseTool) Run(ctx context.Context, inv tools.Invocation) (tools.Result, error) {
	var a fileArgs
	if err := json.Unmarshal(inv.Args, &a); err != nil {
		return tools.Result{}, err
	}
	fs, skipped, err := targetFiles(ctx, inv, a)
	if err != nil {
		return tools.Result{}, err
	}
	blobs, err := readAll(ctx, inv.Repo, fs)
	if err != nil {
		return tools.Result{}, err
	}
	var res tools.Result
	for _, f := range fs {
		src := blobs[f.oid]
		if len(src) > MaxFileBytes {
			skipped++
			continue
		}
		errs, err := t.p.Errors(f.lang, src)
		if err != nil {
			return tools.Result{}, fmt.Errorf("%s: %w", f.path, err)
		}
		for _, e := range errs {
			code := "SyntaxError"
			msg := "syntax error near " + quoteSnippet(e.Text)
			if e.Missing {
				code, msg = "MissingNode", "missing "+e.Text
			}
			res.Diagnostics = append(res.Diagnostics, tools.Diagnostic{Severity: tools.Error, Code: code, File: f.path,
				Line: e.Line, Col: e.Col, Message: msg, Fingerprint: tools.Fingerprint("ts.parse", f.path, code, e.Text)})
		}
	}
	if len(res.Diagnostics) > 0 {
		res.ExitCode = 1
	}
	res.Metrics = map[string]float64{"files_parsed": float64(len(fs) - skipped), "files_skipped": float64(skipped)}
	return res, nil
}

func quoteSnippet(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 60 {
		s = s[:60] + "…"
	}
	return fmt.Sprintf("%q", s)
}

// - ts.symbols -

// SymbolsTool returns the exported symbols of the examined files as Data:
// {"files": {"path": [Symbol...]}}.
type SymbolsTool struct{ p Parser }

func (SymbolsTool) Spec() tools.Spec {
	return tools.Spec{Name: "ts.symbols", Version: "1", Determinism: tools.Pure, ReadOnly: true, UsesBase: true,
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{` + fileArgsProps + `}}`)}
}

func (t SymbolsTool) Run(ctx context.Context, inv tools.Invocation) (tools.Result, error) {
	var a fileArgs
	if err := json.Unmarshal(inv.Args, &a); err != nil {
		return tools.Result{}, err
	}
	fs, _, err := targetFiles(ctx, inv, a)
	if err != nil {
		return tools.Result{}, err
	}
	blobs, err := readAll(ctx, inv.Repo, fs)
	if err != nil {
		return tools.Result{}, err
	}
	out := map[string][]Symbol{}
	var res tools.Result
	n := 0
	for _, f := range fs {
		syms, _, err := t.p.Extract(f.lang, f.path, blobs[f.oid])
		if errors.Is(err, ErrSyntax) {
			res.Diagnostics = append(res.Diagnostics, tools.Diagnostic{Severity: tools.Warning, Code: "Unparseable", File: f.path,
				Message: err.Error(), Fingerprint: tools.Fingerprint("ts.symbols", f.path, "Unparseable")})
			continue
		}
		if err != nil {
			return tools.Result{}, fmt.Errorf("%s: %w", f.path, err)
		}
		if syms == nil {
			syms = []Symbol{}
		}
		out[f.path] = syms
		n += len(syms)
	}
	res.Data, _ = json.Marshal(map[string]any{"files": out})
	res.Metrics = map[string]float64{"files": float64(len(out)), "symbols": float64(n)}
	return res, nil
}

// - ts.api_contract -

// ContractTool fails when exported symbols were removed or changed their
// signature against the merge base (and are not allow-listed), and when a
// changed file adds an import that cannot resolve in the tree.
type ContractTool struct{ p Parser }

type contractArgs struct {
	Languages []string `json:"languages,omitempty"`
	Paths     []string `json:"paths,omitempty"`
	Exclude   []string `json:"exclude,omitempty"`
	// Allow lists symbol IDs (globs) whose removal or change is accepted.
	Allow []string `json:"allow,omitempty"`
	// Imports enables the unresolved-import check (default true).
	Imports *bool `json:"imports,omitempty"`
}

func (ContractTool) Spec() tools.Spec {
	return tools.Spec{Name: "ts.api_contract", Version: "1", Determinism: tools.Pure, ReadOnly: true, UsesBase: true,
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{
  "languages":{"type":"array","items":{"enum":["go","python","typescript","php"]}},
  "paths":{"type":"array","items":{"type":"string"}},
  "exclude":{"type":"array","items":{"type":"string"}},
  "allow":{"type":"array","items":{"type":"string"}},
  "imports":{"type":"boolean"}}}`)}
}

func (t ContractTool) Run(ctx context.Context, inv tools.Invocation) (tools.Result, error) {
	var a contractArgs
	if err := json.Unmarshal(inv.Args, &a); err != nil {
		return tools.Result{}, err
	}
	fa := fileArgs{Languages: a.Languages, Paths: a.Paths, Exclude: a.Exclude}
	cs, _, err := diff.Changes(ctx, inv)
	if err != nil {
		return tools.Result{}, err
	}
	var oldFiles, newFiles []file
	renamedFrom := map[string]string{} // new path -> old path
	for _, c := range cs {
		if c.NewMode == "160000" || c.OldMode == "160000" {
			continue
		}
		oldPath := c.Path
		if c.OldPath != "" {
			oldPath = c.OldPath
			renamedFrom[c.Path] = c.OldPath
		}
		if c.Status != 'A' {
			if l, ok := fa.want(oldPath); ok {
				oldFiles = append(oldFiles, file{oldPath, c.OldOID, l})
			}
		}
		if c.Status != 'D' {
			if l, ok := fa.want(c.Path); ok {
				newFiles = append(newFiles, file{c.Path, c.NewOID, l})
			}
		}
	}
	blobs, err := readAll(ctx, inv.Repo, append(append([]file{}, oldFiles...), newFiles...))
	if err != nil {
		return tools.Result{}, err
	}
	var res tools.Result
	extract := func(fs []file, side string) (map[string]Symbol, map[string]map[string]bool, error) {
		syms := map[string]Symbol{}
		imps := map[string]map[string]bool{}
		for _, f := range fs {
			s, im, err := t.p.Extract(f.lang, f.path, blobs[f.oid])
			if errors.Is(err, ErrSyntax) {
				if side == "new" {
					res.Diagnostics = append(res.Diagnostics, tools.Diagnostic{Severity: tools.Error, Code: "Unparseable",
						File: f.path, Message: "cannot check API of a file that does not parse",
						Fingerprint: tools.Fingerprint("ts.api_contract", f.path, "Unparseable")})
				}
				continue
			}
			if err != nil {
				return nil, nil, fmt.Errorf("%s: %w", f.path, err)
			}
			for _, x := range s {
				syms[x.ID+"|"+x.Kind] = x
			}
			set := map[string]bool{}
			for _, i := range im {
				set[i.Path] = true
			}
			imps[f.path] = set
		}
		return syms, imps, nil
	}
	oldSyms, oldImps, err := extract(oldFiles, "old")
	if err != nil {
		return tools.Result{}, err
	}
	newSyms, _, err := extract(newFiles, "new")
	if err != nil {
		return tools.Result{}, err
	}
	allowed := func(id string) bool { return diff.InScope(a.Allow, id) || containsExact(a.Allow, id) }
	newByID := map[string]Symbol{}
	for _, s := range newSyms {
		newByID[s.ID] = s
	}
	keys := sortedKeys(oldSyms)
	removed, changed, added := 0, 0, 0
	for _, k := range keys {
		o := oldSyms[k]
		n, ok := newByID[o.ID]
		switch {
		case !ok:
			if allowed(o.ID) {
				continue
			}
			removed++
			res.Diagnostics = append(res.Diagnostics, tools.Diagnostic{Severity: tools.Error, Code: "Removed", File: o.File,
				Message:     fmt.Sprintf("exported %s %s was removed", o.Kind, o.Name),
				Fingerprint: tools.Fingerprint("ts.api_contract", "Removed", o.ID)})
		case n.SigHash != o.SigHash:
			if allowed(o.ID) {
				continue
			}
			changed++
			res.Diagnostics = append(res.Diagnostics, tools.Diagnostic{Severity: tools.Error, Code: "SignatureChanged", File: n.File, Line: n.Line,
				Message:     fmt.Sprintf("exported %s %s changed: %s -> %s", o.Kind, o.Name, o.Signature, n.Signature),
				Fingerprint: tools.Fingerprint("ts.api_contract", "SignatureChanged", o.ID, n.SigHash)})
		}
	}
	oldByID := map[string]bool{}
	for _, s := range oldSyms {
		oldByID[s.ID] = true
	}
	for _, k := range sortedKeys(newSyms) {
		if s := newSyms[k]; !oldByID[s.ID] {
			added++
			res.Diagnostics = append(res.Diagnostics, tools.Diagnostic{Severity: tools.Info, Code: "Added", File: s.File, Line: s.Line,
				Message: fmt.Sprintf("new exported %s %s", s.Kind, s.Name), Fingerprint: tools.Fingerprint("ts.api_contract", "Added", s.ID)})
		}
	}

	unresolved := 0
	if a.Imports == nil || *a.Imports {
		rv, err := newResolver(ctx, inv.Repo, inv.Tree)
		if err != nil {
			return tools.Result{}, err
		}
		for _, f := range newFiles {
			_, im, err := t.p.Extract(f.lang, f.path, blobs[f.oid])
			if err != nil {
				continue // reported above
			}
			prev := oldImps[f.path]
			if from, ok := renamedFrom[f.path]; ok {
				prev = oldImps[from]
			}
			for _, i := range im {
				if prev[i.Path] {
					continue // only new imports are checked
				}
				if ok, why := rv.resolves(f.lang, f.path, i.Path); !ok {
					unresolved++
					res.Diagnostics = append(res.Diagnostics, tools.Diagnostic{Severity: tools.Error, Code: "UnresolvedImport",
						File: f.path, Line: i.Line, Message: fmt.Sprintf("import %q does not resolve: %s", i.Path, why),
						Fingerprint: tools.Fingerprint("ts.api_contract", "UnresolvedImport", f.path, i.Path)})
				}
			}
		}
	}
	if removed+changed+unresolved > 0 || hasError(res.Diagnostics) {
		res.ExitCode = 1
	}
	res.Metrics = map[string]float64{"removed": float64(removed), "changed": float64(changed), "added": float64(added),
		"unresolved_imports": float64(unresolved)}
	return res, nil
}

func hasError(ds []tools.Diagnostic) bool {
	for _, d := range ds {
		if d.Severity == tools.Error {
			return true
		}
	}
	return false
}

func containsExact(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
