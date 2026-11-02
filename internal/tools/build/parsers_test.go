package build

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/patcarter883/spine-next/internal/tools"
)

func load(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func codes(ds []tools.Diagnostic) map[string]tools.Diagnostic {
	m := map[string]tools.Diagnostic{}
	for _, d := range ds {
		m[d.Code+"|"+d.File] = d
	}
	return m
}

func TestGoTestJSON(t *testing.T) {
	ds, m, err := parseGoTestJSON(ParseInput{Stdout: load(t, "gotest.json"), Root: "/ROOT", Dir: "/ROOT"})
	if err != nil {
		t.Fatal(err)
	}
	c := codes(ds)
	if _, ok := c["ex.com/m/p.TestBad|"]; !ok {
		t.Fatalf("TestBad missing: %v", ds)
	}
	if _, ok := c["ex.com/m/p.TestSub/inner|"]; !ok {
		t.Fatalf("leaf subtest missing: %v", ds)
	}
	if _, ok := c["ex.com/m/p.TestSub|"]; ok {
		t.Fatalf("parent of failing subtest reported: %v", ds)
	}
	if len(ds) != 2 || m["tests_passed"] != 1 || m["tests_failed"] != 3 {
		t.Fatalf("diags=%v metrics=%v", ds, m)
	}
	if got := c["ex.com/m/p.TestBad|"].Message; got != "p_test.go:4: got 1 want 2" {
		t.Fatalf("message %q", got)
	}
}

func TestGoTestJSONBuildFailure(t *testing.T) {
	ds, _, err := parseGoTestJSON(ParseInput{Stdout: load(t, "gobuild.json"), Root: "/ROOT", Dir: "/ROOT"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 2 || ds[0].File != "q/q.go" || ds[0].Line != 2 || ds[1].Message != "undefined: undefinedThing" {
		t.Fatalf("%+v", ds)
	}
	// The same error at another line has the same fingerprint.
	moved, _, _ := parseGoBuild(ParseInput{Stderr: []byte("q/q.go:9:1: undefined: undefinedThing\n"), Root: "/R"})
	if moved[0].Fingerprint != ds[1].Fingerprint {
		t.Fatal("fingerprint depends on line number")
	}
}

func TestGoVetPrefix(t *testing.T) {
	ds, _, _ := parseGoBuild(ParseInput{Stderr: []byte("# ex.com/m/q\n# [ex.com/m/q]\nvet: q/q.go:3:9: undefined: undefinedThing\n"), Root: "/R"})
	if len(ds) != 1 || ds[0].File != "q/q.go" || ds[0].Col != 9 {
		t.Fatalf("%+v", ds)
	}
}

func TestPytestJUnit(t *testing.T) {
	ds, m, err := parsePytestJUnit(ParseInput{Report: load(t, "junit.xml"), Root: "/ROOT"})
	if err != nil {
		t.Fatal(err)
	}
	c := codes(ds)
	if _, ok := c["tests.test_a::test_bad|"]; !ok || len(ds) != 2 || m["tests_passed"] != 1 || m["tests_failed"] != 2 {
		t.Fatalf("diags=%v metrics=%v", ds, m)
	}
	if _, _, err := parsePytestJUnit(ParseInput{}); err == nil {
		t.Fatal("missing report must be a format error")
	}
}

func TestESLintJSON(t *testing.T) {
	ds, m, err := parseESLintJSON(ParseInput{Stdout: load(t, "eslint.json"), Root: "/ROOT"})
	if err != nil {
		t.Fatal(err)
	}
	c := codes(ds)
	if _, ok := c["no-var|src/b.js"]; !ok {
		t.Fatalf("%v", ds)
	}
	if _, ok := c["parse|src/a.ts"]; !ok {
		t.Fatalf("fatal parse error missing: %v", ds)
	}
	if m["errors"] != float64(len(ds)) {
		t.Fatalf("metrics %v", m)
	}
}

func TestTSC(t *testing.T) {
	ds, _, err := parseTSC(ParseInput{Stdout: load(t, "tsc.txt"), Root: "/ROOT", Dir: "/ROOT"})
	if err != nil || len(ds) != 1 || ds[0].Code != "TS2322" || ds[0].File != "src/a.ts" || ds[0].Line != 2 {
		t.Fatalf("%+v %v", ds, err)
	}
}

func TestRuffJSON(t *testing.T) {
	ds, _, err := parseRuffJSON(ParseInput{Stdout: load(t, "ruff.json"), Root: "/ROOT"})
	if err != nil {
		t.Fatal(err)
	}
	c := codes(ds)
	if _, ok := c["F401|pkg/lint.py"]; !ok {
		t.Fatalf("%v", ds)
	}
}

func TestPHPLint(t *testing.T) {
	ds, _, _ := parsePHPLint(ParseInput{Stdout: load(t, "php.txt"), Root: "/ROOT", Dir: "/ROOT"})
	if len(ds) != 1 || ds[0].File != "bad.php" || ds[0].Line != 2 || ds[0].Code != "php.parse" {
		t.Fatalf("%+v", ds)
	}
}

func TestRegex(t *testing.T) {
	in := ParseInput{Stdout: []byte("W lib/x.py:3 E12 bad thing\nnoise\n"), Root: "/R",
		Regex: &RegexParser{Pattern: `^(?P<severity>[EW]) (?P<file>\S+):(?P<line>\d+) (?P<code>\S+) (?P<message>.*)$`}}
	in.Regex.Pattern = `^(?P<sev>[EW]) (?P<file>[^:]+):(?P<line>\d+) (?P<code>\S+) (?P<message>.*)$`
	ds, _, err := parseRegex(in)
	if err != nil || len(ds) != 1 || ds[0].File != "lib/x.py" || ds[0].Line != 3 || ds[0].Code != "E12" {
		t.Fatalf("%+v %v", ds, err)
	}
}
