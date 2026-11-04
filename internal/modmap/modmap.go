// Package modmap derives a repository's module map (.spine/modules.yaml):
// module and package boundaries from build files (go.mod, package.json,
// pyproject.toml) and source layout, the imports between modules (Go via
// go/parser, other languages via the tree-sitter extractor), where each
// module's tests live, and which modules change together in git history.
//
// Everything here is derived, deterministic and recomputed on refresh.
// Only `label` and `caveats` are written by an agent (the survey task) and
// kept across refreshes. Like gates.yaml, the map that counts is the one on
// the base branch: it is reviewed and integrated, never read from a task's
// worktree.
package modmap

import (
	"bytes"
	"context"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/patcarter883/spine-next/internal/gitro"
	"github.com/patcarter883/spine-next/internal/tools/ast"
	"github.com/patcarter883/spine-next/internal/tools/diff"
)

// Path is the module map's location in a repository.
const Path = ".spine/modules.yaml"

// Module is one unit of code: a Go package, or a source directory of a
// JavaScript/TypeScript, Python or PHP project.
type Module struct {
	Name     string `yaml:"name" json:"name"` // its path; "." for the repository root
	Language string `yaml:"language" json:"language"`
	Root     string `yaml:"root" json:"root"`                           // build root: the go.mod/package.json/pyproject directory
	Package  string `yaml:"package,omitempty" json:"package,omitempty"` // Go import path, npm or Python package name
	Files    int    `yaml:"files" json:"files"`
	// Tests are the module's own tests: next to its code, or in a test
	// directory named for it (up to 20 listed; NTests counts all).
	Tests  []string `yaml:"tests,omitempty" json:"tests,omitempty"`
	NTests int      `yaml:"test_files" json:"test_files"`
	// CoveredBy are other tests that import the module (integration and
	// end-to-end tests; up to 10).
	CoveredBy []string `yaml:"covered_by,omitempty" json:"covered_by,omitempty"`
	Imports   []string `yaml:"imports,omitempty" json:"imports,omitempty"` // modules it imports
	Users     int      `yaml:"imported_by" json:"imported_by"`
	// Written by the survey agent, kept across refreshes.
	Label   string   `yaml:"label,omitempty" json:"label,omitempty"`
	Caveats []string `yaml:"caveats,omitempty" json:"caveats,omitempty"`
}

// Pair is two modules that change together.
type Pair struct {
	A        string  `yaml:"a" json:"a"`
	B        string  `yaml:"b" json:"b"`
	Commits  int     `yaml:"commits" json:"commits"`
	Strength float64 `yaml:"strength" json:"strength"` // commits together / commits of the less active one
}

// Map is .spine/modules.yaml.
type Map struct {
	Version   int `yaml:"version" json:"version"`
	Generated struct {
		Commit  string    `yaml:"commit" json:"commit"`
		At      time.Time `yaml:"at" json:"at"`
		History int       `yaml:"history_commits" json:"history_commits"`
	} `yaml:"generated" json:"generated"`
	Modules  []Module `yaml:"modules" json:"modules"`
	CoChange []Pair   `yaml:"co_change,omitempty" json:"co_change,omitempty"`
}

// Options tune derivation.
type Options struct {
	History  int // commits of history mined for co-change (default 1000)
	MinPair  int // co-change: minimum shared commits (default 3)
	MinScore float64
	// MaxModules caps the map; deeper directories fold into their parents
	// until it fits (default 300).
	MaxModules int
}

func (o Options) withDefaults() Options {
	if o.History <= 0 {
		o.History = 1000
	}
	if o.MinPair <= 0 {
		o.MinPair = 3
	}
	if o.MinScore <= 0 {
		o.MinScore = 0.5
	}
	if o.MaxModules <= 0 {
		o.MaxModules = 300
	}
	return o
}

var skipDir = regexp.MustCompile(`(^|/)(node_modules|vendor|dist|build|out|target|\.git|\.venv|venv|__pycache__|\.next|coverage|\.tox|\.mypy_cache)(/|$)`)

// IsTest reports whether p is a test file.
func IsTest(p string) bool {
	base := path.Base(p)
	switch {
	case strings.HasSuffix(base, "_test.go"):
		return true
	case strings.Contains(base, ".test.") || strings.Contains(base, ".spec."):
		return true
	case strings.HasSuffix(base, ".py") && (strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "_test.py") || base == "conftest.py"):
		return true
	case strings.HasSuffix(base, "Test.php"):
		return true
	}
	for _, d := range strings.Split(path.Dir(p), "/") {
		if d == "__tests__" || d == "tests" || d == "test" || d == "testdata" || d == "spec" {
			return true
		}
	}
	return false
}

type root struct {
	dir, lang, pkg string
}

// Derive computes the map at rev. prev (may be nil) supplies labels and
// caveats to keep.
func Derive(ctx context.Context, repo, rev string, prev *Map, o Options) (*Map, error) {
	o = o.withDefaults()
	git := gitro.Repo{Dir: repo}
	commit, err := git.Line(ctx, "rev-parse", "--verify", "-q", rev+"^{commit}")
	if err != nil {
		return nil, fmt.Errorf("modmap: %s is not a commit", rev)
	}
	es, err := git.LsTree(ctx, commit)
	if err != nil {
		return nil, err
	}
	var files []gitro.Entry
	var roots []root
	var manifests []gitro.Entry
	for _, e := range es {
		if skipDir.MatchString(e.Path) {
			continue
		}
		switch path.Base(e.Path) {
		case "go.mod", "package.json", "pyproject.toml", "composer.json":
			if e.Size < 1<<20 {
				manifests = append(manifests, e)
			}
		}
		if _, ok := ast.LangFor(e.Path); ok && e.Size < 1<<20 {
			files = append(files, e)
		}
	}
	mb, err := blobs(ctx, git, manifests)
	if err != nil {
		return nil, err
	}
	for _, e := range manifests {
		dir := path.Dir(e.Path)
		b := mb[e.OID]
		switch path.Base(e.Path) {
		case "go.mod":
			if m := reGoModule.FindSubmatch(b); m != nil {
				roots = append(roots, root{dir, "go", string(m[1])})
			}
		case "package.json":
			roots = append(roots, root{dir, "typescript", jsonName(b)})
		case "pyproject.toml":
			roots = append(roots, root{dir, "python", tomlName(b)})
		case "composer.json":
			roots = append(roots, root{dir, "php", jsonName(b)})
		}
	}
	sort.Slice(roots, func(i, j int) bool { return len(roots[i].dir) > len(roots[j].dir) })
	rootOf := func(p string) root {
		for _, r := range roots {
			if r.dir == "." || p == r.dir || strings.HasPrefix(p, r.dir+"/") {
				return r
			}
		}
		return root{dir: "."}
	}

	// Source directories → modules. Go: one per package directory. Others:
	// a directory at most 2 levels below its build root (or below src/).
	depth := 2
	var modOf func(file string) string
	for {
		mods := map[string]bool{}
		modOf = func(file string) string {
			dir := path.Dir(file)
			lang, _ := ast.LangFor(file)
			if lang == ast.Go {
				return dir
			}
			r := rootOf(dir)
			rel := strings.TrimPrefix(strings.TrimPrefix(dir, r.dir), "/")
			if r.dir == "." {
				rel = dir
				if rel == "." {
					rel = ""
				}
			}
			parts := strings.Split(rel, "/")
			if rel == "" {
				parts = nil
			}
			keep := depth
			if len(parts) > 0 && (parts[0] == "src" || parts[0] == "lib" || parts[0] == "app" || parts[0] == "packages") {
				keep++
			}
			if len(parts) > keep {
				parts = parts[:keep]
			}
			return cleanJoin(r.dir, strings.Join(parts, "/"))
		}
		for _, f := range files {
			if !IsTest(f.Path) {
				mods[modOf(f.Path)] = true
			}
		}
		if len(mods) <= o.MaxModules || depth == 0 {
			break
		}
		depth--
	}

	byName := map[string]*Module{}
	moduleFor := func(dir string) *Module {
		m := byName[dir]
		if m == nil {
			r := rootOf(dir)
			m = &Module{Name: dir, Root: r.dir, Language: r.lang}
			byName[dir] = m
		}
		return m
	}
	var srcFiles, testFiles []gitro.Entry
	for _, f := range files {
		if IsTest(f.Path) {
			testFiles = append(testFiles, f)
			continue
		}
		srcFiles = append(srcFiles, f)
		m := moduleFor(modOf(f.Path))
		m.Files++
		lang, _ := ast.LangFor(f.Path)
		if m.Language == "" || m.Root == "." && m.Language != string(lang.Family()) {
			m.Language = lang.Family()
		}
		if r := rootOf(m.Name); r.pkg != "" && m.Package == "" {
			if r.lang == "go" {
				m.Package = strings.TrimSuffix(r.pkg+"/"+strings.TrimPrefix(strings.TrimPrefix(m.Name, r.dir), "/"), "/")
				if m.Name == r.dir {
					m.Package = r.pkg
				}
			} else {
				m.Package = r.pkg
			}
		}
	}
	names := make([]string, 0, len(byName))
	for n := range byName {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool { return len(names[i]) > len(names[j]) })
	// owner maps any path to the module whose directory contains it.
	owner := func(p string) string {
		for _, n := range names {
			if n == "." || p == n || strings.HasPrefix(p, n+"/") {
				return n
			}
		}
		return ""
	}

	// Imports, from source and test files.
	parser := ast.Default()
	all := append(append([]gitro.Entry{}, srcFiles...), testFiles...)
	src, err := blobs(ctx, git, all)
	if err != nil {
		return nil, err
	}
	fileSet := map[string]bool{}
	for _, f := range files {
		fileSet[f.Path] = true
	}
	goPkgs := map[string]string{} // import path → module
	for _, n := range names {
		if p := byName[n].Package; p != "" && byName[n].Language == "go" {
			goPkgs[p] = n
		}
	}
	npm := map[string]string{} // package name → its root module
	for _, r := range roots {
		if r.lang == "typescript" && r.pkg != "" {
			if m := owner(path.Join(r.dir, "x")); m != "" {
				npm[r.pkg] = m
			}
		}
	}
	resolve := func(file, imp string) string {
		lang, _ := ast.LangFor(file)
		switch lang {
		case ast.Go:
			return goPkgs[imp]
		case ast.TypeScript, ast.TSX:
			if strings.HasPrefix(imp, ".") {
				return owner(path.Join(path.Dir(file), imp))
			}
			for pkg, m := range npm {
				if imp == pkg || strings.HasPrefix(imp, pkg+"/") {
					return m
				}
			}
		case ast.Python:
			if strings.HasPrefix(imp, ".") {
				n := len(imp) - len(strings.TrimLeft(imp, "."))
				base := path.Dir(file)
				for i := 1; i < n; i++ {
					base = path.Dir(base)
				}
				return owner(path.Join(base, strings.ReplaceAll(imp[n:], ".", "/")))
			}
			rel := strings.ReplaceAll(imp, ".", "/")
			for _, r := range append([]string{"", "src"}, rootDirs(roots, "python")...) {
				p := path.Join(r, rel)
				if fileSet[p+"/__init__.py"] {
					return owner(p + "/__init__.py")
				}
				if fileSet[p+".py"] {
					return owner(p + ".py")
				}
			}
		}
		return ""
	}
	importsOf := map[string]map[string]bool{}
	testImports := map[string][]string{} // test file → modules it imports
	for _, f := range all {
		lang, _ := ast.LangFor(f.Path)
		_, imps, err := parser.Extract(lang, f.Path, src[f.OID])
		if err != nil {
			continue // unparseable or no tree-sitter in this build
		}
		from := owner(f.Path)
		for _, im := range imps {
			to := resolve(f.Path, im.Path)
			if to == "" {
				continue
			}
			if IsTest(f.Path) {
				testImports[f.Path] = append(testImports[f.Path], to)
				continue
			}
			if to != from && from != "" {
				if importsOf[from] == nil {
					importsOf[from] = map[string]bool{}
				}
				importsOf[from][to] = true
			}
		}
	}
	for from, tos := range importsOf {
		m := byName[from]
		for to := range tos {
			m.Imports = append(m.Imports, to)
			byName[to].Users++
		}
		sort.Strings(m.Imports)
	}

	// Tests belong to the module whose directory holds them; tests elsewhere
	// (tests/, __tests__/) to the modules they import, else by name.
	for _, f := range testFiles {
		own := ""
		if o := owner(f.Path); o != "" && o != "." && !inTestDir(f.Path, o) {
			own = o // next to the module's code
		} else if m := byStem(f.Path, names); m != "" {
			own = m // tests/test_foo.py, test/foo.test.ts
		} else if ims := testImports[f.Path]; len(ims) > 0 && inTestDir(f.Path, "") {
			// A dedicated test directory (tests/, __tests__/) testing the
			// first module it imports from the same build root.
			for _, m := range ims {
				if rootOf(m).dir == rootOf(path.Dir(f.Path)).dir {
					own = m
					break
				}
			}
		}
		if own != "" {
			mod := byName[own]
			mod.NTests++
			if len(mod.Tests) < 20 {
				mod.Tests = append(mod.Tests, f.Path)
			}
		}
		seen := map[string]bool{own: true}
		for _, m := range testImports[f.Path] {
			if seen[m] {
				continue
			}
			seen[m] = true
			if mod := byName[m]; len(mod.CoveredBy) < 10 {
				mod.CoveredBy = append(mod.CoveredBy, f.Path)
			}
		}
	}

	out := &Map{Version: 1}
	out.Generated.Commit, out.Generated.At = commit, time.Now().UTC().Truncate(time.Second)
	for _, n := range names {
		m := byName[n]
		sort.Strings(m.Tests)
		sort.Strings(m.CoveredBy)
		out.Modules = append(out.Modules, *m)
	}
	sort.Slice(out.Modules, func(i, j int) bool { return out.Modules[i].Name < out.Modules[j].Name })

	pairs, n, err := coChange(ctx, git, commit, owner, o)
	if err != nil {
		return nil, err
	}
	out.CoChange, out.Generated.History = pairs, n
	if prev != nil {
		keep := map[string]Module{}
		for _, m := range prev.Modules {
			keep[m.Name] = m
		}
		for i := range out.Modules {
			if p, ok := keep[out.Modules[i].Name]; ok {
				out.Modules[i].Label, out.Modules[i].Caveats = p.Label, p.Caveats
			}
		}
	}
	return out, nil
}

func inTestDir(p, module string) bool {
	rel := strings.TrimPrefix(path.Dir(p), module)
	for _, d := range strings.Split(rel, "/") {
		if d == "tests" || d == "test" || d == "__tests__" || d == "spec" {
			return true
		}
	}
	return false
}

// byStem maps tests/test_foo.py or test/foo.test.ts to a module named foo.
func byStem(p string, modules []string) string {
	stem := path.Base(p)
	stem = strings.TrimSuffix(stem, path.Ext(stem))
	for _, cut := range []string{".test", ".spec", "_test", "Test"} {
		stem = strings.TrimSuffix(stem, cut)
	}
	stem = strings.TrimPrefix(stem, "test_")
	for _, m := range modules {
		if path.Base(m) == stem {
			return m
		}
	}
	return ""
}

func rootDirs(roots []root, lang string) []string {
	var out []string
	for _, r := range roots {
		if r.lang == lang && r.dir != "." {
			out = append(out, r.dir, path.Join(r.dir, "src"))
		}
	}
	return out
}

func cleanJoin(a, b string) string {
	if b == "" {
		return a
	}
	if a == "." || a == "" {
		return b
	}
	return a + "/" + b
}

var (
	reGoModule = regexp.MustCompile(`(?m)^module\s+"?([^\s"]+)`)
	reJSONName = regexp.MustCompile(`"name"\s*:\s*"([^"]+)"`)
	reTOMLName = regexp.MustCompile(`(?m)^name\s*=\s*"([^"]+)"`)
)

func jsonName(b []byte) string {
	if m := reJSONName.FindSubmatch(b); m != nil {
		return string(m[1])
	}
	return ""
}

func tomlName(b []byte) string {
	if m := reTOMLName.FindSubmatch(b); m != nil {
		return string(m[1])
	}
	return ""
}

func blobs(ctx context.Context, git gitro.Repo, es []gitro.Entry) (map[string][]byte, error) {
	out := map[string][]byte{}
	for i := 0; i < len(es); i += 500 {
		j := min(i+500, len(es))
		oids := make([]string, 0, j-i)
		for _, e := range es[i:j] {
			oids = append(oids, e.OID)
		}
		b, err := git.Blobs(ctx, oids)
		if err != nil {
			return nil, err
		}
		for k, v := range b {
			out[k] = v
		}
	}
	return out, nil
}

// coChange mines history for modules that change in the same commits.
// Commits touching many modules (renames, formatting sweeps) are skipped.
func coChange(ctx context.Context, git gitro.Repo, commit string, owner func(string) string, o Options) ([]Pair, int, error) {
	out, err := git.Run(ctx, "log", "--no-merges", "--no-renames", "--format=%x00", "--name-only", "-n", fmt.Sprint(o.History), commit)
	if err != nil {
		return nil, 0, err
	}
	count := map[string]int{}
	pair := map[[2]string]int{}
	n := 0
	for _, rec := range bytes.Split(out, []byte{0}) {
		mods := map[string]bool{}
		for _, f := range strings.Split(string(rec), "\n") {
			if f = strings.TrimSpace(f); f == "" {
				continue
			}
			if m := owner(f); m != "" {
				mods[m] = true
			}
		}
		if len(mods) == 0 {
			continue
		}
		n++
		if len(mods) > 12 {
			continue
		}
		ms := make([]string, 0, len(mods))
		for m := range mods {
			ms = append(ms, m)
			count[m]++
		}
		sort.Strings(ms)
		for i := range ms {
			for j := i + 1; j < len(ms); j++ {
				pair[[2]string{ms[i], ms[j]}]++
			}
		}
	}
	var ps []Pair
	for k, c := range pair {
		if c < o.MinPair {
			continue
		}
		s := float64(c) / float64(min(count[k[0]], count[k[1]]))
		if s < o.MinScore {
			continue
		}
		ps = append(ps, Pair{A: k[0], B: k[1], Commits: c, Strength: float64(int(s*100+0.5)) / 100})
	}
	sort.Slice(ps, func(i, j int) bool {
		if ps[i].Commits != ps[j].Commits {
			return ps[i].Commits > ps[j].Commits
		}
		return ps[i].A+ps[i].B < ps[j].A+ps[j].B
	})
	if len(ps) > 200 {
		ps = ps[:200]
	}
	return ps, n, nil
}

// Marshal renders the map as YAML with a header.
func (m *Map) Marshal() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString("# Spine module map. Derived fields are recomputed by `spine modules` and the\n# survey task; edit only `label` and `caveats`, which are kept across refreshes.\n")
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(m); err != nil {
		return nil, err
	}
	enc.Close()
	return buf.Bytes(), nil
}

// Parse reads a map.
func Parse(b []byte) (*Map, error) {
	var m Map
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("%s: %w", Path, err)
	}
	if m.Version != 1 {
		return nil, fmt.Errorf("%s: unsupported version %d", Path, m.Version)
	}
	return &m, nil
}

// Load reads the map from rev (never from a worktree). ok=false if absent.
func Load(ctx context.Context, repo, rev string) (*Map, bool, error) {
	b, ok, err := (gitro.Repo{Dir: repo}).Show(ctx, rev, Path)
	if err != nil || !ok {
		return nil, ok, err
	}
	m, err := Parse(b)
	return m, err == nil, err
}

// Owner returns the module containing p ("" if none).
func (m *Map) Owner(p string) *Module {
	var best *Module
	for i := range m.Modules {
		n := m.Modules[i].Name
		if n == "." || p == n || strings.HasPrefix(p, n+"/") {
			if best == nil || len(n) > len(best.Name) {
				best = &m.Modules[i]
			}
		}
	}
	return best
}

// Touched returns the modules a scope glob may change.
func (m *Map) Touched(glob string) []*Module {
	var out []*Module
	lit := glob
	if i := strings.IndexAny(glob, "*?["); i >= 0 {
		lit = glob[:i]
	}
	for i := range m.Modules {
		mod := &m.Modules[i]
		n := mod.Name
		switch {
		case n == "." && !strings.Contains(strings.TrimSuffix(lit, "/"), "/") && lit != glob:
			out = append(out, mod) // a root-level wildcard
		case n == ".":
		case strings.HasPrefix(n+"/", lit) || diff.MatchGlob(glob, n+"/x") || diff.MatchGlob(glob, n):
			out = append(out, mod) // the glob covers (part of) the module
		case strings.HasPrefix(lit, n+"/") && m.Owner(strings.TrimSuffix(lit, "/")+"/x") == mod:
			out = append(out, mod) // the glob is inside the module
		}
	}
	if len(out) == 0 && !strings.ContainsAny(glob, "*?[") {
		if o := m.Owner(glob); o != nil {
			out = append(out, o)
		}
	}
	return out
}

// Pair returns the co-change pair of a and b, if any.
func (m *Map) Pair(a, b string) (Pair, bool) {
	if a > b {
		a, b = b, a
	}
	for _, p := range m.CoChange {
		if p.A == a && p.B == b {
			return p, true
		}
	}
	return Pair{}, false
}

// Render is the map as prompt text, at most maxTokens (≈4 bytes a token):
// the most-used modules first, then the strongest co-change pairs.
func (m *Map) Render(maxTokens int) string {
	if maxTokens <= 0 {
		maxTokens = 1500
	}
	limit := maxTokens * 4
	mods := append([]Module{}, m.Modules...)
	sort.SliceStable(mods, func(i, j int) bool { return mods[i].Users+mods[i].Files > mods[j].Users+mods[j].Files })
	var b strings.Builder
	b.WriteString("Modules (path, language, files, tests; what it imports):\n")
	shown := 0
	for _, x := range mods {
		line := fmt.Sprintf("- %s (%s, %d files", x.Name, x.Language, x.Files)
		if x.NTests > 0 {
			line += fmt.Sprintf(", %d test files", x.NTests)
		} else {
			line += ", no tests"
		}
		line += ")"
		if x.Label != "" {
			line += ": " + x.Label
		}
		if len(x.Imports) > 0 {
			imps := x.Imports
			if len(imps) > 6 {
				imps = append(append([]string{}, imps[:6]...), "…")
			}
			line += "; imports " + strings.Join(imps, ", ")
		}
		for _, c := range x.Caveats {
			line += ". Caveat: " + c
		}
		line += "\n"
		if b.Len()+len(line) > limit*3/4 {
			break
		}
		b.WriteString(line)
		shown++
	}
	if shown < len(mods) {
		fmt.Fprintf(&b, "- … and %d smaller modules\n", len(mods)-shown)
	}
	if len(m.CoChange) > 0 {
		b.WriteString("Modules that usually change together (put them in one task, or order the tasks):\n")
		for _, p := range m.CoChange {
			line := fmt.Sprintf("- %s + %s (%d commits)\n", p.A, p.B, p.Commits)
			if b.Len()+len(line) > limit {
				break
			}
			b.WriteString(line)
		}
	}
	return b.String()
}
