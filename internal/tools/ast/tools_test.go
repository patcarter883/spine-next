//go:build cgo

package ast_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/patcarter883/spine-next/internal/testrepo"
	"github.com/patcarter883/spine-next/internal/tools"
	"github.com/patcarter883/spine-next/internal/tools/ast"
)

func reg() *tools.Registry {
	r := tools.New(tools.Options{Cache: tools.NewMemCache()})
	r.MustRegister(ast.Tools(nil)...)
	return r
}

func byCode(ds []tools.Diagnostic) map[string]int {
	m := map[string]int{}
	for _, d := range ds {
		m[d.Code]++
	}
	return m
}

func TestContractGoAndTS(t *testing.T) {
	r := testrepo.New(t)
	r.Write(map[string]string{
		"go.mod":       "module ex.com/m\n\ngo 1.22\n\nrequire github.com/known/dep v1.0.0\n",
		"lib/lib.go":   "package lib\n\nfunc Keep(a int) int { return a }\nfunc Gone() {}\nfunc Change(a int) {}\n",
		"util/u.go":    "package util\n",
		"package.json": `{"name":"app","dependencies":{"lodash":"^4"}}`,
		"src/a.ts":     "export function f(a: number): number { return a }\nexport interface I { a: string }\n",
		"src/b.ts":     "export const B = 1\n",
	})
	base := r.Commit("base")
	r.Write(map[string]string{
		"lib/lib.go": "package lib\n\nimport (\n\t\"fmt\"\n\t\"ex.com/m/util\"\n\t\"ex.com/m/nope\"\n\t\"github.com/known/dep/sub\"\n\t\"github.com/unknown/x\"\n)\n\nfunc Keep(renamed int) int { return renamed }\nfunc Change(a string) {}\nfunc New() {}\n",
		"src/a.ts":   "import _ from 'lodash';\nimport { B } from './b';\nimport { z } from './missing';\nimport left from 'left-pad';\nimport fs from 'node:fs';\nexport function f(a: number): number { return a }\nexport interface I { a: string; b?: number }\n",
	})
	c := r.Commit("work")
	inv := tools.Invocation{Repo: r.Dir, Tree: r.Tree(c), Commit: c, Base: base}
	res, err := reg().Invoke(context.Background(), "ts.api_contract", inv)
	if err != nil {
		t.Fatal(err)
	}
	got := byCode(res.Diagnostics)
	// Go: Gone removed, Change changed, New added; nope + unknown unresolved.
	// TS: I changed; ./missing and left-pad unresolved.
	if res.ExitCode != 1 || got["Removed"] != 1 || got["SignatureChanged"] != 2 || got["Added"] != 1 || got["UnresolvedImport"] != 4 {
		for _, d := range res.Diagnostics {
			t.Logf("%s %s %s", d.Code, d.File, d.Message)
		}
		t.Fatalf("codes %v exit %d", got, res.ExitCode)
	}

	// Allow-listing the API changes leaves only the import failures.
	args, _ := json.Marshal(map[string]any{"allow": []string{"lib:*", "src/a:I"}, "imports": false})
	inv.Args = args
	res, err = reg().Invoke(context.Background(), "ts.api_contract", inv)
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("allow-listed: %+v %v", res.Diagnostics, err)
	}
}

func TestParseOnlyChangedFiles(t *testing.T) {
	r := testrepo.New(t)
	r.Write(map[string]string{"old_broken.py": "def f(:\n", "ok.py": "x = 1\n"})
	base := r.Commit("base")
	r.Write(map[string]string{"ok.py": "def g(:\n    pass\n", "new.ts": "export const a = 1;\n"})
	c := r.Commit("work")
	res, err := reg().Invoke(context.Background(), "ts.parse", tools.Invocation{Repo: r.Dir, Tree: r.Tree(c), Commit: c, Base: base})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 1 || len(res.Diagnostics) == 0 || res.Diagnostics[0].File != "ok.py" || res.Metrics["files_parsed"] != 2 {
		t.Fatalf("%+v", res)
	}
	all, _ := json.Marshal(map[string]any{"all": true, "languages": []string{"typescript"}})
	res, err = reg().Invoke(context.Background(), "ts.parse", tools.Invocation{Repo: r.Dir, Tree: r.Tree(c), Commit: c, Base: base, Args: all})
	if err != nil || res.ExitCode != 0 || res.Metrics["files_parsed"] != 1 {
		t.Fatalf("all/ts: %+v %v", res, err)
	}
}

func TestSymbolsData(t *testing.T) {
	r := testrepo.New(t)
	base := r.Commit("base")
	r.Write(map[string]string{"app/M.php": "<?php\nnamespace App;\nclass M { public function a(): int { return 1; } }\n"})
	c := r.Commit("work")
	res, err := reg().Invoke(context.Background(), "ts.symbols", tools.Invocation{Repo: r.Dir, Tree: r.Tree(c), Commit: c, Base: base})
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		Files map[string][]ast.Symbol `json:"files"`
	}
	if err := json.Unmarshal(res.Data, &d); err != nil || len(d.Files["app/M.php"]) != 2 {
		t.Fatalf("%s %v", res.Data, err)
	}
}
