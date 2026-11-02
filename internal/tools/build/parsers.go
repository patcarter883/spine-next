package build

import (
	"bufio"
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/patcarter883/spine-next/internal/tools"
)

// ParseInput is what a parser sees.
type ParseInput struct {
	Stdout, Stderr []byte
	Report         []byte // content of Args.Report, if any
	Root           string // tree root on disk (for relativizing paths)
	Dir            string // working directory of the run
	Regex          *RegexParser
}

// Parser turns tool output into diagnostics and metrics. An error means the
// output was not in the expected format; the run is then reported Unparsed
// if it failed.
type Parser func(in ParseInput) ([]tools.Diagnostic, map[string]float64, error)

// Parsers by name.
var Parsers = map[string]Parser{
	"go-build":     parseGoBuild,
	"gotest-json":  parseGoTestJSON,
	"pytest-junit": parsePytestJUnit,
	"eslint-json":  parseESLintJSON,
	"tsc":          parseTSC,
	"ruff-json":    parseRuffJSON,
	"php-lint":     parsePHPLint,
	"regex":        parseRegex,
}

var errFormat = errors.New("output not in the expected format")

// rel makes a tool-reported path relative to the tree root. Relative paths
// are taken relative to the run directory.
func (in ParseInput) rel(p string) string {
	if p == "" {
		return ""
	}
	if !strings.HasPrefix(p, "/") && in.Dir != "" && in.Dir != in.Root {
		p = in.Dir + "/" + p
	}
	if strings.HasPrefix(p, "/") {
		return tools.RelPath(in.Root, p)
	}
	return tools.RelPath("", p)
}

func (in ParseInput) norm(msg string) string { return tools.NormalizeMessage(msg, in.Root, in.Dir) }

// - go-build (also go vet and gotest build output) -

var reGoPos = regexp.MustCompile(`^(?:vet: )?((?:[A-Za-z]:)?[^\s:][^:]*\.(?:go|s|c|h|mod|sum)):(\d+)(?::(\d+))?: (.*)$`)

func parseGoBuild(in ParseInput) ([]tools.Diagnostic, map[string]float64, error) {
	ds := goBuildLines(in, append(append([]byte{}, in.Stdout...), in.Stderr...))
	return ds, map[string]float64{"errors": float64(len(ds))}, nil
}

func goBuildLines(in ParseInput, out []byte) []tools.Diagnostic {
	var ds []tools.Diagnostic
	seen := map[string]bool{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		line := sc.Text()
		if m := reGoPos.FindStringSubmatch(line); m != nil {
			file := in.rel(m[1])
			ln, _ := strconv.Atoi(m[2])
			col, _ := strconv.Atoi(m[3])
			msg := m[4]
			fp := tools.Fingerprint("go", file, in.norm(msg))
			if seen[fp] {
				continue
			}
			seen[fp] = true
			ds = append(ds, tools.Diagnostic{Severity: tools.Error, Code: "compile", File: file, Line: ln, Col: col,
				Message: msg, Fingerprint: fp})
			continue
		}
		if msg, ok := strings.CutPrefix(line, "go: "); ok && !strings.HasPrefix(msg, "downloading") && !strings.HasPrefix(msg, "finding") {
			fp := tools.Fingerprint("go", "toolchain", in.norm(msg))
			if !seen[fp] {
				seen[fp] = true
				ds = append(ds, tools.Diagnostic{Severity: tools.Error, Code: "go", Message: msg, Fingerprint: fp})
			}
		}
	}
	return ds
}

// - gotest-json -

type goTestEvent struct {
	Action     string
	Package    string
	ImportPath string
	Test       string
	Output      string
	OutputType  string
	FailedBuild string
}

func parseGoTestJSON(in ParseInput) ([]tools.Diagnostic, map[string]float64, error) {
	type testKey struct{ pkg, test string }
	output := map[testKey]*strings.Builder{}
	failed := map[testKey]bool{}
	pkgFailed := map[string]bool{}
	pkgBuildFailed := map[string]bool{}
	var buildOut bytes.Buffer
	var nonJSON bytes.Buffer
	metrics := map[string]float64{"tests_passed": 0, "tests_failed": 0, "tests_skipped": 0}
	events := 0
	sc := bufio.NewScanner(bytes.NewReader(in.Stdout))
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	for sc.Scan() {
		line := sc.Bytes()
		var ev goTestEvent
		if len(line) == 0 || line[0] != '{' || json.Unmarshal(line, &ev) != nil {
			nonJSON.Write(line)
			nonJSON.WriteByte('\n')
			continue
		}
		events++
		k := testKey{ev.Package, ev.Test}
		switch ev.Action {
		case "build-output":
			buildOut.WriteString(ev.Output)
		case "output":
			if ev.Test != "" && ev.OutputType != "frame" && !strings.HasPrefix(ev.Output, "=== ") && !strings.HasPrefix(ev.Output, "--- ") {
				b := output[k]
				if b == nil {
					b = &strings.Builder{}
					output[k] = b
				}
				if b.Len() < tools.MaxMessageBytes {
					b.WriteString(ev.Output)
				}
			}
		case "pass":
			if ev.Test != "" {
				metrics["tests_passed"]++
			}
		case "skip":
			if ev.Test != "" {
				metrics["tests_skipped"]++
			}
		case "fail":
			if ev.Test != "" {
				failed[k] = true
			} else {
				pkgFailed[ev.Package] = true
				if ev.FailedBuild != "" {
					pkgBuildFailed[ev.Package] = true
				}
			}
		}
	}
	if events == 0 && len(bytes.TrimSpace(in.Stdout)) > 0 {
		return nil, nil, errFormat
	}
	var ds []tools.Diagnostic
	// Compile errors (go ≥1.24 reports them as build-output; older versions
	// and module errors go to stderr or non-JSON stdout).
	ds = append(ds, goBuildLines(in, buildOut.Bytes())...)
	ds = append(ds, goBuildLines(in, nonJSON.Bytes())...)
	ds = append(ds, goBuildLines(in, in.Stderr)...)

	// Report leaf failures only: a parent test fails because its subtest did.
	keys := make([]testKey, 0, len(failed))
	for k := range failed {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].pkg != keys[j].pkg {
			return keys[i].pkg < keys[j].pkg
		}
		return keys[i].test < keys[j].test
	})
	pkgHasTestFail := map[string]bool{}
	for _, k := range keys {
		pkgHasTestFail[k.pkg] = true
		leaf := true
		for o := range failed {
			if o.pkg == k.pkg && strings.HasPrefix(o.test, k.test+"/") {
				leaf = false
				break
			}
		}
		metrics["tests_failed"]++
		if !leaf {
			continue
		}
		msg := ""
		if b := output[k]; b != nil {
			msg = strings.TrimSpace(b.String())
		}
		code := k.pkg + "." + k.test
		ds = append(ds, tools.Diagnostic{Severity: tools.Error, Code: code, Message: msg,
			Fingerprint: tools.Fingerprint("gotest", k.pkg, k.test)})
	}
	// A package that failed with no failing test and no compile error
	// (panic in init, TestMain exit, timeout) is still a failure.
	pkgs := make([]string, 0, len(pkgFailed))
	for p := range pkgFailed {
		pkgs = append(pkgs, p)
	}
	sort.Strings(pkgs)
	for _, p := range pkgs {
		if pkgHasTestFail[p] {
			continue
		}
		if pkgBuildFailed[p] && countSev(ds, tools.Error) > 0 {
			continue // already reported as compile diagnostics
		}
		msg := "package failed without a failing test"
		if pkgBuildFailed[p] {
			msg = "package failed to build"
		}
		ds = append(ds, tools.Diagnostic{Severity: tools.Error, Code: p, Message: msg,
			Fingerprint: tools.Fingerprint("gotest", p, "<package>")})
	}
	return ds, metrics, nil
}

// - pytest-junit -

type junitSuites struct {
	Suites []junitSuite `xml:"testsuite"`
}
type junitSuite struct {
	Cases  []junitCase  `xml:"testcase"`
	Suites []junitSuite `xml:"testsuite"`
}
type junitCase struct {
	Classname string        `xml:"classname,attr"`
	Name      string        `xml:"name,attr"`
	File      string        `xml:"file,attr"`
	Line      int           `xml:"line,attr"`
	Failure   *junitProblem `xml:"failure"`
	Error     *junitProblem `xml:"error"`
	Skipped   *junitProblem `xml:"skipped"`
}
type junitProblem struct {
	Message string `xml:"message,attr"`
	Text    string `xml:",chardata"`
}

func parsePytestJUnit(in ParseInput) ([]tools.Diagnostic, map[string]float64, error) {
	if len(bytes.TrimSpace(in.Report)) == 0 {
		return nil, nil, errFormat
	}
	var root junitSuites
	if err := xml.Unmarshal(in.Report, &root); err != nil || len(root.Suites) == 0 {
		var one junitSuite
		if err2 := xml.Unmarshal(in.Report, &one); err2 != nil {
			return nil, nil, fmt.Errorf("%w: %v", errFormat, err)
		}
		root.Suites = []junitSuite{one}
	}
	metrics := map[string]float64{"tests_passed": 0, "tests_failed": 0, "tests_errored": 0, "tests_skipped": 0}
	var ds []tools.Diagnostic
	var walk func(s junitSuite)
	walk = func(s junitSuite) {
		for _, c := range s.Cases {
			id := c.Name
			if c.Classname != "" {
				id = c.Classname + "::" + c.Name
			}
			p := c.Failure
			kind := "failure"
			if p == nil && c.Error != nil {
				p, kind = c.Error, "error"
			}
			switch {
			case p != nil:
				if kind == "failure" {
					metrics["tests_failed"]++
				} else {
					metrics["tests_errored"]++
				}
				msg := strings.TrimSpace(p.Message)
				if t := strings.TrimSpace(p.Text); t != "" {
					msg = strings.TrimSpace(msg + "\n" + t)
				}
				ds = append(ds, tools.Diagnostic{Severity: tools.Error, Code: id, File: in.rel(c.File), Line: c.Line,
					Message: msg, Fingerprint: tools.Fingerprint("pytest", id, kind)})
			case c.Skipped != nil:
				metrics["tests_skipped"]++
			default:
				metrics["tests_passed"]++
			}
		}
		for _, sub := range s.Suites {
			walk(sub)
		}
	}
	for _, s := range root.Suites {
		walk(s)
	}
	return ds, metrics, nil
}

// - eslint-json -

type eslintFile struct {
	FilePath string `json:"filePath"`
	Messages []struct {
		RuleID   *string `json:"ruleId"`
		Severity int     `json:"severity"`
		Message  string  `json:"message"`
		Line     int     `json:"line"`
		Column   int     `json:"column"`
		Fatal    bool    `json:"fatal"`
	} `json:"messages"`
}

func parseESLintJSON(in ParseInput) ([]tools.Diagnostic, map[string]float64, error) {
	var files []eslintFile
	if err := json.Unmarshal(bytes.TrimSpace(in.Stdout), &files); err != nil {
		return nil, nil, fmt.Errorf("%w: %v", errFormat, err)
	}
	var ds []tools.Diagnostic
	m := map[string]float64{"errors": 0, "warnings": 0}
	for _, f := range files {
		file := in.rel(f.FilePath)
		for _, msg := range f.Messages {
			code := "parse"
			if msg.RuleID != nil {
				code = *msg.RuleID
			}
			sev := tools.Warning
			if msg.Severity >= 2 || msg.Fatal {
				sev = tools.Error
				m["errors"]++
			} else {
				m["warnings"]++
			}
			ds = append(ds, tools.Diagnostic{Severity: sev, Code: code, File: file, Line: msg.Line, Col: msg.Column,
				Message: msg.Message, Fingerprint: tools.Fingerprint("eslint", file, code, in.norm(msg.Message))})
		}
	}
	return ds, m, nil
}

// - tsc -

var (
	reTSCPos  = regexp.MustCompile(`^(.+?)\((\d+),(\d+)\): (error|warning) (TS\d+): (.*)$`)
	reTSCBare = regexp.MustCompile(`^(error|warning) (TS\d+): (.*)$`)
)

func parseTSC(in ParseInput) ([]tools.Diagnostic, map[string]float64, error) {
	var ds []tools.Diagnostic
	out := append(append([]byte{}, in.Stdout...), in.Stderr...)
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		line := sc.Text()
		if m := reTSCPos.FindStringSubmatch(line); m != nil {
			file := in.rel(m[1])
			ln, _ := strconv.Atoi(m[2])
			col, _ := strconv.Atoi(m[3])
			ds = append(ds, tools.Diagnostic{Severity: sev(m[4]), Code: m[5], File: file, Line: ln, Col: col, Message: m[6]})
			continue
		}
		if m := reTSCBare.FindStringSubmatch(line); m != nil {
			ds = append(ds, tools.Diagnostic{Severity: sev(m[1]), Code: m[2], Message: m[3]})
			continue
		}
		if len(ds) > 0 && strings.HasPrefix(line, "  ") { // continuation of the previous message
			ds[len(ds)-1].Message += "\n" + strings.TrimSpace(line)
		}
	}
	for i := range ds {
		ds[i].Fingerprint = tools.Fingerprint("tsc", ds[i].File, ds[i].Code, in.norm(ds[i].Message))
	}
	return ds, map[string]float64{"errors": float64(countSev(ds, tools.Error))}, nil
}

func sev(s string) tools.Severity {
	switch strings.ToLower(s) {
	case "warning", "warn":
		return tools.Warning
	case "info", "note":
		return tools.Info
	default:
		return tools.Error
	}
}

func countSev(ds []tools.Diagnostic, s tools.Severity) int {
	n := 0
	for _, d := range ds {
		if d.Severity == s {
			n++
		}
	}
	return n
}

// - ruff-json -

type ruffDiag struct {
	Code     *string `json:"code"`
	Message  string  `json:"message"`
	Filename string  `json:"filename"`
	Severity string  `json:"severity"`
	Location struct {
		Row    int `json:"row"`
		Column int `json:"column"`
	} `json:"location"`
}

func parseRuffJSON(in ParseInput) ([]tools.Diagnostic, map[string]float64, error) {
	var rs []ruffDiag
	if err := json.Unmarshal(bytes.TrimSpace(in.Stdout), &rs); err != nil {
		return nil, nil, fmt.Errorf("%w: %v", errFormat, err)
	}
	var ds []tools.Diagnostic
	for _, r := range rs {
		code := "syntax"
		if r.Code != nil {
			code = *r.Code
		}
		s := tools.Error
		if r.Severity != "" {
			s = sev(r.Severity)
		}
		file := in.rel(r.Filename)
		ds = append(ds, tools.Diagnostic{Severity: s, Code: code, File: file, Line: r.Location.Row, Col: r.Location.Column,
			Message: r.Message, Fingerprint: tools.Fingerprint("ruff", file, code, in.norm(r.Message))})
	}
	return ds, map[string]float64{"errors": float64(countSev(ds, tools.Error))}, nil
}

// - php-lint (php -l) -

var rePHP = regexp.MustCompile(`(?:PHP )?(Parse|Fatal) error:\s+(.*?) in (.+?) on line (\d+)`)

func parsePHPLint(in ParseInput) ([]tools.Diagnostic, map[string]float64, error) {
	var ds []tools.Diagnostic
	seen := map[string]bool{}
	out := append(append([]byte{}, in.Stdout...), in.Stderr...)
	for _, line := range strings.Split(string(out), "\n") {
		m := rePHP.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		file := in.rel(m[3])
		ln, _ := strconv.Atoi(m[4])
		code := "php." + strings.ToLower(m[1])
		fp := tools.Fingerprint("php", file, code, in.norm(m[2]))
		if seen[fp] {
			continue
		}
		seen[fp] = true
		ds = append(ds, tools.Diagnostic{Severity: tools.Error, Code: code, File: file, Line: ln, Message: m[2], Fingerprint: fp})
	}
	return ds, map[string]float64{"errors": float64(len(ds))}, nil
}

// - regex -

// RegexParser matches one diagnostic per line. Named groups: file, line,
// col, code, severity, message.
type RegexParser struct {
	Pattern  string `json:"pattern"`
	Severity string `json:"severity,omitempty"`
	Code     string `json:"code,omitempty"`
}

func parseRegex(in ParseInput) ([]tools.Diagnostic, map[string]float64, error) {
	if in.Regex == nil {
		return nil, nil, fmt.Errorf("%w: regex parser without a pattern", errFormat)
	}
	re, err := regexp.Compile(in.Regex.Pattern)
	if err != nil {
		return nil, nil, err
	}
	idx := map[string]int{}
	for i, n := range re.SubexpNames() {
		if n != "" {
			idx[n] = i
		}
	}
	get := func(m []string, k string) string {
		if i, ok := idx[k]; ok {
			return m[i]
		}
		return ""
	}
	var ds []tools.Diagnostic
	out := append(append([]byte{}, in.Stdout...), in.Stderr...)
	for _, line := range strings.Split(string(out), "\n") {
		m := re.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		d := tools.Diagnostic{Severity: tools.Error, Code: in.Regex.Code, File: in.rel(get(m, "file")), Message: get(m, "message")}
		if d.Message == "" {
			d.Message = strings.TrimSpace(line)
		}
		if s := get(m, "severity"); s != "" {
			d.Severity = sev(s)
		} else if in.Regex.Severity != "" {
			d.Severity = sev(in.Regex.Severity)
		}
		if c := get(m, "code"); c != "" {
			d.Code = c
		}
		d.Line, _ = strconv.Atoi(get(m, "line"))
		d.Col, _ = strconv.Atoi(get(m, "col"))
		d.Fingerprint = tools.Fingerprint("regex", d.File, d.Code, in.norm(d.Message))
		ds = append(ds, d)
	}
	return ds, map[string]float64{"errors": float64(countSev(ds, tools.Error))}, nil
}
