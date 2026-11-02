package ast

import (
	"context"
	"encoding/json"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/patcarter883/spine-next/internal/gitro"
)

// resolver answers "does this new import resolve in the tree?" Checks are
// deliberately conservative: an import is reported only when the tree gives
// positive evidence it cannot resolve.
//
//   - Go: standard library paths pass; paths inside a module of the tree must
//     name a directory with .go files; other paths must be covered by a
//     require in that module's go.mod.
//   - Python: relative imports and imports of a top-level package that exists
//     in the tree must name an existing module or package. Third-party and
//     standard-library imports are not checked (import names do not map to
//     distribution names reliably).
//   - TypeScript/JavaScript: relative specifiers must resolve to a file (with
//     the usual extensions or index file); bare specifiers must be Node
//     built-ins, tsconfig "paths" aliases, or dependencies in the nearest
//     package.json. With no package.json in the tree, bare specifiers pass.
//   - PHP: not checked (autoloading is configuration-driven).
type resolver struct {
	files   map[string]bool
	dirs    map[string]bool
	goDirs  map[string]bool // dirs containing .go files
	goMods  []goMod
	pkgJSON map[string]map[string]bool // dir -> dependency names
	tsAlias map[string][]string        // dir -> alias prefixes
}

type goMod struct {
	dir, path string
	requires  []string
}

func newResolver(ctx context.Context, repo, tree string) (*resolver, error) {
	r := gitro.Repo{Dir: repo}
	es, err := r.LsTree(ctx, tree)
	if err != nil {
		return nil, err
	}
	rv := &resolver{files: map[string]bool{}, dirs: map[string]bool{"": true, ".": true}, goDirs: map[string]bool{},
		pkgJSON: map[string]map[string]bool{}, tsAlias: map[string][]string{}}
	var want []gitro.Entry
	for _, e := range es {
		rv.files[e.Path] = true
		for d := path.Dir(e.Path); d != "." && !rv.dirs[d]; d = path.Dir(d) {
			rv.dirs[d] = true
		}
		base := path.Base(e.Path)
		if strings.HasSuffix(e.Path, ".go") {
			rv.goDirs[path.Dir(e.Path)] = true
		}
		if (base == "go.mod" || base == "package.json" || base == "tsconfig.json") && !strings.Contains(e.Path, "node_modules/") && e.Size < 1<<20 {
			want = append(want, e)
		}
	}
	oids := make([]string, len(want))
	for i, e := range want {
		oids[i] = e.OID
	}
	blobs, err := r.Blobs(ctx, oids)
	if err != nil {
		return nil, err
	}
	for _, e := range want {
		dir := path.Dir(e.Path)
		b := blobs[e.OID]
		switch path.Base(e.Path) {
		case "go.mod":
			rv.goMods = append(rv.goMods, parseGoMod(dir, b))
		case "package.json":
			rv.pkgJSON[dir] = parsePackageJSON(b)
		case "tsconfig.json":
			rv.tsAlias[dir] = parseTSPaths(b)
		}
	}
	// Longest module directory first, so nested modules win.
	sort.Slice(rv.goMods, func(i, j int) bool { return len(rv.goMods[i].dir) > len(rv.goMods[j].dir) })
	return rv, nil
}

var (
	reModule  = regexp.MustCompile(`(?m)^module\s+(\S+)`)
	reRequire = regexp.MustCompile(`(?m)^\s*(?:require\s+)?([A-Za-z0-9][^\s()]*\.[^\s()]+)\s+v\S+`)
)

func parseGoMod(dir string, b []byte) goMod {
	m := goMod{dir: dir}
	if s := reModule.FindSubmatch(b); s != nil {
		m.path = strings.Trim(string(s[1]), `"`)
	}
	for _, s := range reRequire.FindAllSubmatch(b, -1) {
		m.requires = append(m.requires, string(s[1]))
	}
	return m
}

func parsePackageJSON(b []byte) map[string]bool {
	var pj map[string]json.RawMessage
	deps := map[string]bool{}
	if json.Unmarshal(b, &pj) != nil {
		return deps
	}
	if n, ok := pj["name"]; ok {
		var s string
		if json.Unmarshal(n, &s) == nil && s != "" {
			deps[s] = true // self-reference
		}
	}
	for _, k := range []string{"dependencies", "devDependencies", "peerDependencies", "optionalDependencies"} {
		var m map[string]string
		if raw, ok := pj[k]; ok && json.Unmarshal(raw, &m) == nil {
			for d := range m {
				deps[d] = true
			}
		}
	}
	return deps
}

var reJSONComment = regexp.MustCompile(`(?m)^\s*//.*$|/\*[\s\S]*?\*/`)
var reTrailingComma = regexp.MustCompile(`,\s*([}\]])`)

func parseTSPaths(b []byte) []string {
	clean := reTrailingComma.ReplaceAll(reJSONComment.ReplaceAll(b, nil), []byte("$1"))
	var tc struct {
		CompilerOptions struct {
			Paths map[string][]string `json:"paths"`
		} `json:"compilerOptions"`
	}
	if json.Unmarshal(clean, &tc) != nil {
		return nil
	}
	var out []string
	for k := range tc.CompilerOptions.Paths {
		out = append(out, strings.TrimSuffix(k, "*"))
	}
	return out
}

func (rv *resolver) resolves(lang Lang, file, imp string) (bool, string) {
	switch lang {
	case Go:
		return rv.goImport(file, imp)
	case Python:
		return rv.pyImport(file, imp)
	case TypeScript, TSX:
		return rv.tsImport(file, imp)
	}
	return true, ""
}

func (rv *resolver) goImport(file, imp string) (bool, string) {
	first, _, _ := strings.Cut(imp, "/")
	if !strings.Contains(first, ".") {
		return true, "" // standard library
	}
	for _, m := range rv.goMods {
		if m.path != "" && (imp == m.path || strings.HasPrefix(imp, m.path+"/")) {
			dir := path.Join(m.dir, strings.TrimPrefix(strings.TrimPrefix(imp, m.path), "/"))
			if rv.goDirs[dir] {
				return true, ""
			}
			return false, "no Go package at " + dir
		}
	}
	var mod *goMod
	for i := range rv.goMods {
		if d := rv.goMods[i].dir; d == "." || strings.HasPrefix(file, d+"/") {
			mod = &rv.goMods[i]
			break
		}
	}
	if mod == nil {
		return true, "" // no go.mod: GOPATH or tooling we do not model
	}
	for _, req := range mod.requires {
		if imp == req || strings.HasPrefix(imp, req+"/") {
			return true, ""
		}
	}
	return false, "not provided by any require in " + path.Join(mod.dir, "go.mod")
}

func (rv *resolver) pyModuleExists(base, dotted string) bool {
	p := path.Join(base, strings.ReplaceAll(dotted, ".", "/"))
	return rv.files[p+".py"] || rv.files[p+".pyi"] || rv.files[p+"/__init__.py"] || rv.dirs[p]
}

func (rv *resolver) pyImport(file, imp string) (bool, string) {
	if strings.HasPrefix(imp, ".") {
		n := len(imp) - len(strings.TrimLeft(imp, "."))
		rest := imp[n:]
		base := path.Dir(file)
		for i := 1; i < n; i++ {
			base = path.Dir(base)
		}
		if rest == "" {
			if rv.dirs[base] {
				return true, ""
			}
			return false, "no package at " + base
		}
		if rv.pyModuleExists(base, rest) {
			return true, ""
		}
		return false, "no module " + rest + " under " + base
	}
	top, _, _ := strings.Cut(imp, ".")
	for _, root := range []string{".", "src"} {
		if rv.files[path.Join(root, top+".py")] || rv.files[path.Join(root, top, "__init__.py")] {
			if rv.pyModuleExists(root, imp) {
				return true, ""
			}
			return false, "local package " + top + " has no module " + imp
		}
	}
	return true, "" // third-party or standard library: not checked
}

var tsExts = []string{".ts", ".tsx", ".d.ts", ".js", ".jsx", ".mjs", ".cjs", ".json"}

var nodeBuiltins = map[string]bool{}

func init() {
	for _, m := range strings.Fields(`assert async_hooks buffer child_process cluster console constants crypto dgram
		diagnostics_channel dns domain events fs http http2 https inspector module net os path perf_hooks process
		punycode querystring readline repl stream string_decoder sys timers tls trace_events tty url util v8 vm
		wasi worker_threads zlib test`) {
		nodeBuiltins[m] = true
	}
}

func (rv *resolver) tsImport(file, imp string) (bool, string) {
	if strings.HasPrefix(imp, "./") || strings.HasPrefix(imp, "../") || imp == "." || imp == ".." {
		p := path.Join(path.Dir(file), imp)
		cands := []string{p}
		for _, e := range tsExts {
			cands = append(cands, p+e, p+"/index"+e)
		}
		for _, js := range []string{".js", ".jsx", ".mjs", ".cjs"} { // TS ESM: "./x.js" names x.ts
			if strings.HasSuffix(p, js) {
				stem := strings.TrimSuffix(p, js)
				cands = append(cands, stem+".ts", stem+".tsx", stem+".d.ts")
			}
		}
		for _, c := range cands {
			if rv.files[c] {
				return true, ""
			}
		}
		return false, "no file for " + p
	}
	if strings.HasPrefix(imp, "node:") || nodeBuiltins[strings.SplitN(imp, "/", 2)[0]] {
		return true, ""
	}
	for dir := path.Dir(file); ; dir = path.Dir(dir) {
		for _, a := range rv.tsAlias[dir] {
			if a != "" && (strings.HasPrefix(imp, a) || imp == strings.TrimSuffix(a, "/")) {
				return true, ""
			}
		}
		if dir == "." || dir == "/" || dir == "" {
			break
		}
	}
	pkg := imp
	if strings.HasPrefix(imp, "@") {
		parts := strings.SplitN(imp, "/", 3)
		if len(parts) >= 2 {
			pkg = parts[0] + "/" + parts[1]
		}
	} else {
		pkg = strings.SplitN(imp, "/", 2)[0]
	}
	found := false
	for dir := path.Dir(file); ; dir = path.Dir(dir) {
		if deps, ok := rv.pkgJSON[dir]; ok {
			found = true
			if deps[pkg] || deps["@types/"+pkg] {
				return true, ""
			}
		}
		if dir == "." || dir == "/" || dir == "" {
			break
		}
	}
	if !found {
		return true, "" // no package.json: nothing to check against
	}
	return false, "package " + pkg + " is not a dependency in package.json"
}
