//go:build !cgo

package ast

import (
	"go/parser"
	"go/scanner"
	"go/token"
)

type noCGO struct{}

// Default returns the parser for this build: Go only.
func Default() Parser { return noCGO{} }

// Available reports whether tree-sitter languages can be parsed.
func Available() bool { return false }

func (noCGO) Errors(lang Lang, src []byte) ([]SyntaxError, error) {
	if lang != Go {
		return nil, ErrUnavailable
	}
	_, err := parser.ParseFile(token.NewFileSet(), "x.go", src, parser.SkipObjectResolution)
	if err == nil {
		return nil, nil
	}
	var out []SyntaxError
	if list, ok := err.(scanner.ErrorList); ok {
		for _, e := range list {
			out = append(out, SyntaxError{Line: e.Pos.Line, Col: e.Pos.Column, Text: e.Msg})
		}
	} else {
		out = append(out, SyntaxError{Line: 1, Col: 1, Text: err.Error()})
	}
	return out, nil
}

func (noCGO) Extract(lang Lang, file string, src []byte) ([]Symbol, []Import, error) {
	if lang != Go {
		return nil, nil, ErrUnavailable
	}
	return goExtract(file, src)
}
