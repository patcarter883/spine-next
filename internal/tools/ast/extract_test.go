//go:build cgo

package ast

import (
	"sort"
	"strings"
	"testing"
)

func ids(syms []Symbol) string {
	var s []string
	for _, x := range syms {
		s = append(s, x.Kind+" "+x.ID)
	}
	sort.Strings(s)
	return strings.Join(s, "\n")
}

func TestExtractPython(t *testing.T) {
	src := "import a.b as c\nfrom .x import y\nX_MAX: int = 3\n_hidden = 1\n@dec\ndef f(a, b: int = 2) -> int:\n    pass\ndef _p(): pass\nclass C(B):\n    def __init__(self, x): pass\n    def m(self, x): pass\n    def _q(self): pass\n    @property\n    def p(self): pass\nclass _H:\n    def m(self): pass\n"
	syms, imps, err := Default().Extract(Python, "pkg/mod.py", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{"class pkg.mod:C", "const pkg.mod:X_MAX", "function pkg.mod:f", "method pkg.mod:C.__init__", "method pkg.mod:C.m", "method pkg.mod:C.p"}, "\n")
	if got := ids(syms); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	if len(imps) != 2 || imps[0].Path != "a.b" || imps[1].Path != ".x" {
		t.Fatalf("imports %+v", imps)
	}
}

func TestExtractTypeScript(t *testing.T) {
	src := "import x from './a';\nimport {y} from \"lodash\";\nconst z = require('fs');\nexport function f(a: number): string { return '' }\nfunction hidden() {}\nexport class C extends B { private p(): void {} protected q() {} m(x: number): void {} }\nexport interface I { a: string }\nexport type T = string | number;\nexport enum E { A }\nexport const K: number = 1, L = 2;\nexport * from './z';\n"
	syms, imps, err := Default().Extract(TypeScript, "src/a.ts", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{"class src/a:C", "const src/a:K", "const src/a:L", "enum src/a:E", "function src/a:f", "interface src/a:I", "method src/a:C.m", "type src/a:T"}, "\n")
	if got := ids(syms); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	var paths []string
	for _, i := range imps {
		paths = append(paths, i.Path)
	}
	sort.Strings(paths)
	if strings.Join(paths, ",") != "./a,./z,fs,lodash" {
		t.Fatalf("imports %v", paths)
	}
	// JSX through the TSX grammar.
	if _, _, err := Default().Extract(TSX, "src/c.jsx", []byte("export function App(){ return <div/> }\n")); err != nil {
		t.Fatal(err)
	}
}

func TestExtractPHP(t *testing.T) {
	src := "<?php\nnamespace App\\Models;\nuse Foo\\Bar;\nfunction f(int $a): int { return $a; }\nclass C extends B { public function m(string $s): void {} private function p() {} function d() {} }\ninterface I { public function i(): int; }\n"
	syms, imps, err := Default().Extract(PHP, "app/Models/C.php", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{`class App\Models\C`, `function App\Models\f`, `interface App\Models\I`, `method App\Models\C::d`, `method App\Models\C::m`, `method App\Models\I::i`}, "\n")
	if got := ids(syms); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	if len(imps) != 1 || imps[0].Path != `Foo\Bar` {
		t.Fatalf("imports %+v", imps)
	}
}

func TestExtractGoAndSignatureStability(t *testing.T) {
	a := "package p\nimport \"fmt\"\ntype S struct { A int; b string }\nfunc (s *S) M(x int, y string) error { return nil }\nfunc F(a, b int) (int, error) { return 0, nil }\nconst K = 1\nfunc hidden() {}\n"
	b := "package p\nimport \"fmt\"\ntype S struct { A int; c bool }\nfunc (s *S) M(renamed int, other string) error { return fmt.Errorf(\"\") }\nfunc F(x, y int) (int, error) { return 1, nil }\nconst K = 2\n"
	sa, ia, err := Default().Extract(Go, "p/a.go", []byte(a))
	if err != nil {
		t.Fatal(err)
	}
	sb, _, _ := Default().Extract(Go, "p/a.go", []byte(b))
	if ids(sa) != "const p:K\nfunction p:F\nmethod p:S.M\ntype p:S" || len(ia) != 1 {
		t.Fatalf("%s %v", ids(sa), ia)
	}
	for i := range sa {
		if sa[i].SigHash != sb[i].SigHash {
			t.Errorf("%s: signature changed by a non-API edit: %q vs %q", sa[i].ID, sa[i].Signature, sb[i].Signature)
		}
	}
}

func TestErrors(t *testing.T) {
	for lang, src := range map[Lang]string{
		Go:         "package p\nfunc F( {\n",
		Python:     "def f(:\n  pass\n",
		TypeScript: "export function f(a: number { }\n",
		PHP:        "<?php\nfunction f( {\n",
	} {
		errs, err := Default().Errors(lang, []byte(src))
		if err != nil || len(errs) == 0 {
			t.Errorf("%s: %v %v", lang, errs, err)
		}
	}
	if errs, _ := Default().Errors(Python, []byte("def f():\n    return 1\n")); len(errs) != 0 {
		t.Fatalf("clean file: %v", errs)
	}
}
