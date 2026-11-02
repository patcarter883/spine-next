// Package ast implements the syntax tools ts.parse, ts.symbols and
// ts.api_contract (RFC §5.1, §5.2).
//
// Parsing is tree-sitter (CGO, vendored grammars for Go, Python,
// TypeScript/TSX/JavaScript and PHP). Exported-symbol extraction is driven by
// per-language query files embedded from queries/*.scm, so adding a language
// is a grammar plus one query file. Go symbols use go/parser instead (stdlib,
// no CGO), as the RFC prefers for Go. Without CGO the tree-sitter languages
// report ErrUnavailable, which the registry surfaces as an infrastructure
// error rather than a verdict.
package ast

import (
	"embed"
	"errors"
	"path"
	"strings"
)

//go:embed queries/*.scm
var queryFS embed.FS

// Lang names a supported language.
type Lang string

const (
	Go         Lang = "go"
	Python     Lang = "python"
	TypeScript Lang = "typescript"
	TSX        Lang = "tsx" // also JavaScript/JSX
	PHP        Lang = "php"
)

// Family groups languages that share a query file and a --languages name.
func (l Lang) Family() string {
	if l == TSX {
		return string(TypeScript)
	}
	return string(l)
}

// LangFor maps a path to its language by extension.
func LangFor(p string) (Lang, bool) {
	switch strings.ToLower(path.Ext(p)) {
	case ".go":
		return Go, true
	case ".py", ".pyi":
		return Python, true
	case ".ts", ".mts", ".cts":
		return TypeScript, true
	case ".tsx", ".js", ".jsx", ".mjs", ".cjs":
		return TSX, true
	case ".php":
		return PHP, true
	}
	return "", false
}

// Symbol is one exported API element.
type Symbol struct {
	ID        string `json:"id"`   // stable identity across files where the language allows it
	Name      string `json:"name"` // qualified within the module: Type.Method, Class::method
	Kind      string `json:"kind"` // function, method, class, interface, type, enum, trait, const, var
	Signature string `json:"signature"`
	SigHash   string `json:"sig_hash"`
	File      string `json:"file"`
	Line      int    `json:"line"`
}

// Import is one import/require/use in a file.
type Import struct {
	Path string `json:"path"`
	Line int    `json:"line"`
}

// SyntaxError is an ERROR or MISSING node.
type SyntaxError struct {
	Line, Col int
	Missing   bool
	Text      string
}

// Parser is the parsing backend. Implementations are safe for concurrent use.
type Parser interface {
	Errors(lang Lang, src []byte) ([]SyntaxError, error)
	Extract(lang Lang, file string, src []byte) ([]Symbol, []Import, error)
}

// ErrUnavailable is returned for tree-sitter languages in a CGO-less build.
var ErrUnavailable = errors.New("ast: tree-sitter is not available in this build (CGO disabled)")

// ErrSyntax is returned by Extract when a Go file does not parse.
var ErrSyntax = errors.New("ast: syntax error")

// moduleID returns the module part of a symbol ID for file-scoped languages.
func moduleID(lang Lang, file string) string {
	switch lang {
	case Python:
		m := strings.TrimSuffix(file, path.Ext(file))
		m = strings.TrimSuffix(m, "/__init__")
		return strings.ReplaceAll(m, "/", ".")
	case Go:
		return path.Dir(file)
	default:
		return strings.TrimSuffix(file, path.Ext(file))
	}
}

func normalizeSig(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
