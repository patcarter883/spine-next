package modmap

import (
	"context"
	"strings"
	"testing"

	"github.com/patcarter883/spine-next/internal/testrepo"
	"github.com/patcarter883/spine-next/internal/tools/ast"
)

func names(ms []*Module) string {
	var out []string
	for _, m := range ms {
		out = append(out, m.Name)
	}
	return strings.Join(out, ",")
}

func TestDerive(t *testing.T) {
	r := testrepo.New(t)
	r.Write(map[string]string{
		"go.mod":                    "module ex.com/app\n\ngo 1.22\n",
		"api/handler.go":            "package api\n\nimport \"ex.com/app/store\"\n\nfunc H() { store.Get() }\n",
		"api/handler_test.go":       "package api\n\nimport \"testing\"\n\nfunc TestH(t *testing.T) {}\n",
		"store/store.go":            "package store\n\nfunc Get() {}\n",
		"e2e/flow_test.go":          "package e2e\n\nimport (\n\t\"testing\"\n\t\"ex.com/app/api\"\n)\n\nfunc TestFlow(t *testing.T) { api.H() }\n",
		"web/package.json":          `{"name": "@ex/web", "version": "1.0.0"}`,
		"web/src/cart/cart.ts":      "import { price } from '../pricing/price';\nexport const total = () => price();\n",
		"web/src/cart/cart.test.ts": "import { total } from './cart';\n",
		"web/src/pricing/price.ts":  "export const price = () => 1;\n",
		"py/pyproject.toml":         "[project]\nname = \"tools\"\n",
		"py/tools/__init__.py":      "",
		"py/tools/fmt.py":           "def f():\n    return 1\n",
		"py/tests/test_fmt.py":      "from tools import fmt\n",
		"node_modules/x/index.js":   "module.exports = 1\n",
	})
	r.Commit("base")
	// History: api and store change together three times.
	for i := 0; i < 3; i++ {
		r.Write(map[string]string{"api/handler.go": "package api\n\nimport \"ex.com/app/store\"\n\n// v" + string(rune('a'+i)) + "\nfunc H() { store.Get() }\n",
			"store/store.go": "package store\n\n// v" + string(rune('a'+i)) + "\nfunc Get() {}\n"})
		r.Commit("api+store")
	}
	m, err := Derive(context.Background(), r.Dir, "HEAD", nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]Module{}
	for _, x := range m.Modules {
		by[x.Name] = x
	}
	if _, ok := by["node_modules/x"]; ok {
		t.Fatal("node_modules mapped")
	}
	api := by["api"]
	if api.Language != "go" || api.Package != "ex.com/app/api" || strings.Join(api.Imports, ",") != "store" || api.NTests != 1 ||
		api.Tests[0] != "api/handler_test.go" || strings.Join(api.CoveredBy, ",") != "e2e/flow_test.go" {
		t.Fatalf("api %+v", api)
	}
	if by["store"].Users != 1 {
		t.Fatalf("store %+v", by["store"])
	}
	if _, ok := by["e2e"]; ok {
		t.Fatal("a test-only directory became a module")
	}
	if p, ok := m.Pair("store", "api"); !ok || p.Commits != 4 || p.Strength != 1 {
		t.Fatalf("co-change %+v %v", p, m.CoChange)
	}
	if ast.Available() { // tree-sitter imports for TS and Python
		cart := by["web/src/cart"]
		if strings.Join(cart.Imports, ",") != "web/src/pricing" || cart.NTests != 1 || cart.Package != "@ex/web" {
			t.Fatalf("cart %+v", cart)
		}
		if f := by["py/tools"]; f.NTests != 1 || f.Tests[0] != "py/tests/test_fmt.py" {
			t.Fatalf("py/tools %+v", f)
		}
	}
	// Scope matching.
	for glob, want := range map[string]string{"api/**": "api", "api/handler.go": "api", "web/src/**": "web/src/cart,web/src/pricing", "store/": "store"} {
		if got := names(m.Touched(glob)); got != want {
			t.Errorf("Touched(%q) = %s, want %s", glob, got, want)
		}
	}
	// Labels survive a refresh; Marshal/Parse round-trip; Render is capped.
	m.Modules[0].Label, m.Modules[0].Caveats = "HTTP handlers", []string{"keep handlers thin"}
	b, err := m.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	back, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	m2, err := Derive(context.Background(), r.Dir, "HEAD", back, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if m2.Modules[0].Label != "HTTP handlers" || len(m2.Modules[0].Caveats) != 1 {
		t.Fatalf("label lost: %+v", m2.Modules[0])
	}
	if txt := m2.Render(60); len(txt) > 60*4+100 || !strings.Contains(m2.Render(2000), "HTTP handlers") || !strings.Contains(m2.Render(2000), "api + store") {
		t.Fatalf("render:\n%s", m2.Render(2000))
	}
	if _, err := Parse([]byte("version: 1\nmodules: []\nsurprise: 1\n")); err == nil {
		t.Fatal("unknown field accepted")
	}
}
