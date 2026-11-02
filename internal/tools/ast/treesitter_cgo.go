//go:build cgo

package ast

import (
	"fmt"
	"strings"
	"sync"

	sitter "github.com/tree-sitter/go-tree-sitter"
	tsgo "github.com/tree-sitter/tree-sitter-go/bindings/go"
	tsphp "github.com/tree-sitter/tree-sitter-php/bindings/go"
	tspy "github.com/tree-sitter/tree-sitter-python/bindings/go"
	tsts "github.com/tree-sitter/tree-sitter-typescript/bindings/go"

	"github.com/patcarter883/spine-next/internal/tools"
)

var grammars = map[Lang]*sitter.Language{
	Go:         sitter.NewLanguage(tsgo.Language()),
	Python:     sitter.NewLanguage(tspy.Language()),
	TypeScript: sitter.NewLanguage(tsts.LanguageTypescript()),
	TSX:        sitter.NewLanguage(tsts.LanguageTSX()),
	PHP:        sitter.NewLanguage(tsphp.LanguagePHP()),
}

type compiled struct {
	once sync.Once
	q    *sitter.Query
	err  error
}

// treeSitter is the CGO backend. Queries are compiled once per language and
// shared (a TSQuery is immutable); parsers and cursors are per call.
type treeSitter struct {
	mu      sync.Mutex
	queries map[Lang]*compiled
}

// Default returns the parser for this build.
func Default() Parser { return defaultParser }

var defaultParser = &treeSitter{queries: map[Lang]*compiled{}}

// Available reports whether tree-sitter languages can be parsed.
func Available() bool { return true }

func (t *treeSitter) query(lang Lang) (*sitter.Query, error) {
	t.mu.Lock()
	c, ok := t.queries[lang]
	if !ok {
		c = &compiled{}
		t.queries[lang] = c
	}
	t.mu.Unlock()
	c.once.Do(func() {
		src, err := queryFS.ReadFile("queries/" + lang.Family() + ".scm")
		if err != nil {
			c.err = fmt.Errorf("ast: no query file for %s: %w", lang, err)
			return
		}
		q, qerr := sitter.NewQuery(grammars[lang], string(src))
		if qerr != nil {
			c.err = fmt.Errorf("ast: %s query: %s", lang, qerr.Error())
			return
		}
		c.q = q
	})
	return c.q, c.err
}

func parse(lang Lang, src []byte) (*sitter.Tree, error) {
	g, ok := grammars[lang]
	if !ok {
		return nil, fmt.Errorf("ast: unsupported language %q", lang)
	}
	p := sitter.NewParser()
	defer p.Close()
	if err := p.SetLanguage(g); err != nil {
		return nil, err
	}
	tr := p.Parse(src, nil)
	if tr == nil {
		return nil, fmt.Errorf("ast: %s parse failed", lang)
	}
	return tr, nil
}

// Errors returns ERROR and MISSING nodes, at most 20.
func (t *treeSitter) Errors(lang Lang, src []byte) ([]SyntaxError, error) {
	tr, err := parse(lang, src)
	if err != nil {
		return nil, err
	}
	defer tr.Close()
	root := tr.RootNode()
	if !root.HasError() {
		return nil, nil
	}
	var out []SyntaxError
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if len(out) >= 20 {
			return
		}
		if n.IsError() || n.IsMissing() {
			pos := n.StartPosition()
			text := n.Utf8Text(src)
			if len(text) > 80 {
				text = text[:80]
			}
			if n.IsMissing() {
				text = n.Kind()
			}
			out = append(out, SyntaxError{Line: int(pos.Row) + 1, Col: int(pos.Column) + 1, Missing: n.IsMissing(), Text: text})
			if n.IsError() {
				return // do not report nested errors of an error node
			}
		}
		for i := uint(0); i < n.ChildCount(); i++ {
			c := n.Child(i)
			if c != nil && (c.HasError() || c.IsMissing()) {
				walk(c)
			}
		}
	}
	walk(root)
	if len(out) == 0 { // HasError without a locatable node
		out = append(out, SyntaxError{Line: 1, Col: 1, Text: "syntax error"})
	}
	return out, nil
}

// Extract returns exported symbols and imports.
func (t *treeSitter) Extract(lang Lang, file string, src []byte) ([]Symbol, []Import, error) {
	if lang == Go {
		return goExtract(file, src)
	}
	q, err := t.query(lang)
	if err != nil {
		return nil, nil, err
	}
	tr, err := parse(lang, src)
	if err != nil {
		return nil, nil, err
	}
	defer tr.Close()
	names := q.CaptureNames()
	qc := sitter.NewQueryCursor()
	defer qc.Close()

	type nsMark struct {
		start uint
		name  string
	}
	var namespaces []nsMark
	type rawSym struct {
		start      uint
		kind, name string
		scope      string
		sig        []string
		line       int
		private    bool
	}
	var raws []rawSym
	var imports []Import
	seen := map[string]bool{}

	ms := qc.Matches(q, tr.RootNode(), src)
	for m := ms.Next(); m != nil; m = ms.Next() {
		var r rawSym
		isDef := false
		for _, c := range m.Captures {
			cn := names[c.Index]
			node := c.Node
			switch {
			case strings.HasPrefix(cn, "def."):
				isDef = true
				r.kind = strings.TrimPrefix(cn, "def.")
				r.start = node.StartByte()
				r.line = int(node.StartPosition().Row) + 1
				r.private = hasPrivateModifier(&node, src)
			case cn == "name":
				r.name = node.Utf8Text(src)
			case cn == "scope":
				r.scope = node.Utf8Text(src)
			case cn == "sig":
				r.sig = append(r.sig, node.Utf8Text(src))
			case cn == "import":
				imports = append(imports, Import{Path: node.Utf8Text(src), Line: int(node.StartPosition().Row) + 1})
			case cn == "namespace":
				namespaces = append(namespaces, nsMark{start: node.StartByte(), name: node.Utf8Text(src)})
			}
		}
		if isDef && r.name != "" && !r.private {
			raws = append(raws, r)
		}
	}

	var syms []Symbol
	for _, r := range raws {
		qual := r.name
		sep := "."
		if lang == PHP {
			sep = "::"
		}
		if r.scope != "" {
			qual = r.scope + sep + r.name
		}
		var id string
		if lang == PHP {
			ns := ""
			for _, n := range namespaces {
				if n.start <= r.start {
					ns = n.name
				}
			}
			id = qual
			if ns != "" {
				id = ns + `\` + qual
			}
		} else {
			id = moduleID(lang, file) + ":" + qual
		}
		if seen[id+"|"+r.kind] {
			continue // e.g. a decorated def matched by two patterns
		}
		seen[id+"|"+r.kind] = true
		sig := normalizeSig(r.kind + " " + strings.Join(r.sig, " "))
		syms = append(syms, Symbol{ID: id, Name: qual, Kind: r.kind, Signature: sig,
			SigHash: tools.Fingerprint(sig), File: file, Line: r.line})
	}
	return syms, imports, nil
}

// hasPrivateModifier reports a TS accessibility_modifier or PHP
// visibility_modifier of private/protected on a definition node.
func hasPrivateModifier(n *sitter.Node, src []byte) bool {
	for i := uint(0); i < n.ChildCount(); i++ {
		c := n.Child(i)
		if c == nil {
			continue
		}
		if k := c.Kind(); k == "accessibility_modifier" || k == "visibility_modifier" {
			switch c.Utf8Text(src) {
			case "private", "protected":
				return true
			}
		}
	}
	return false
}
