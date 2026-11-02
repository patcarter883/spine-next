package ast

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"strconv"
	"strings"

	"github.com/patcarter883/spine-next/internal/tools"
)

// goExtract returns the exported API of one Go file using go/parser. Test
// files and package main export nothing. Signatures are syntactic: parameter
// names are dropped, unexported struct fields are ignored, and constant
// values are not part of the contract.
func goExtract(file string, src []byte) ([]Symbol, []Import, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrSyntax, err)
	}
	var imports []Import
	for _, im := range f.Imports {
		p, _ := strconv.Unquote(im.Path.Value)
		imports = append(imports, Import{Path: p, Line: fset.Position(im.Pos()).Line})
	}
	if strings.HasSuffix(file, "_test.go") || f.Name.Name == "main" {
		return nil, imports, nil
	}
	mod := moduleID(Go, file)
	pr := func(n ast.Node) string {
		var b bytes.Buffer
		printer.Fprint(&b, fset, n)
		return normalizeSig(b.String())
	}
	var syms []Symbol
	add := func(name, kind, sig string, pos token.Pos) {
		sig = normalizeSig(kind + " " + sig)
		syms = append(syms, Symbol{ID: mod + ":" + name, Name: name, Kind: kind, Signature: sig,
			SigHash: tools.Fingerprint(sig), File: file, Line: fset.Position(pos).Line})
	}
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			if !d.Name.IsExported() {
				continue
			}
			sig := funcSig(pr, d.Type)
			if d.Recv == nil || len(d.Recv.List) == 0 {
				add(d.Name.Name, "function", sig, d.Pos())
				continue
			}
			recv := recvName(d.Recv.List[0].Type)
			if recv == "" || !ast.IsExported(recv) {
				continue
			}
			ptr := ""
			if _, ok := d.Recv.List[0].Type.(*ast.StarExpr); ok {
				ptr = "*"
			}
			add(recv+"."+d.Name.Name, "method", "("+ptr+recv+") "+sig, d.Pos())
		case *ast.GenDecl:
			for _, s := range d.Specs {
				switch s := s.(type) {
				case *ast.TypeSpec:
					if !s.Name.IsExported() {
						continue
					}
					sig := ""
					if s.TypeParams != nil {
						sig = "[" + fieldTypes(pr, s.TypeParams, true) + "] "
					}
					if s.Assign.IsValid() {
						sig += "= "
					}
					sig += typeSig(pr, s.Type)
					add(s.Name.Name, "type", sig, s.Pos())
				case *ast.ValueSpec:
					kind := "var"
					if d.Tok == token.CONST {
						kind = "const"
					}
					for _, n := range s.Names {
						if !n.IsExported() {
							continue
						}
						sig := ""
						if s.Type != nil {
							sig = pr(s.Type)
						}
						add(n.Name, kind, sig, n.Pos())
					}
				}
			}
		}
	}
	return syms, imports, nil
}

func recvName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.StarExpr:
		return recvName(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.IndexExpr:
		return recvName(t.X)
	case *ast.IndexListExpr:
		return recvName(t.X)
	}
	return ""
}

func funcSig(pr func(ast.Node) string, ft *ast.FuncType) string {
	s := "func"
	if ft.TypeParams != nil {
		s += "[" + fieldTypes(pr, ft.TypeParams, true) + "]"
	}
	s += "(" + fieldTypes(pr, ft.Params, false) + ")"
	if ft.Results != nil && len(ft.Results.List) > 0 {
		s += " (" + fieldTypes(pr, ft.Results, false) + ")"
	}
	return s
}

// fieldTypes prints a field list's types, one per name. Type parameter
// lists keep names, since constraints refer to them positionally.
func fieldTypes(pr func(ast.Node) string, fl *ast.FieldList, keepNames bool) string {
	if fl == nil {
		return ""
	}
	var parts []string
	for _, f := range fl.List {
		t := pr(f.Type)
		n := len(f.Names)
		if n == 0 {
			n = 1
		}
		for i := 0; i < n; i++ {
			if keepNames && len(f.Names) > 0 {
				parts = append(parts, f.Names[i].Name+" "+t)
			} else {
				parts = append(parts, t)
			}
		}
	}
	return strings.Join(parts, ", ")
}

func typeSig(pr func(ast.Node) string, e ast.Expr) string {
	switch t := e.(type) {
	case *ast.StructType:
		var parts []string
		for _, f := range t.Fields.List {
			if len(f.Names) == 0 { // embedded
				if n := recvName(f.Type); n != "" && ast.IsExported(n) {
					parts = append(parts, pr(f.Type))
				}
				continue
			}
			for _, n := range f.Names {
				if n.IsExported() {
					parts = append(parts, n.Name+" "+pr(f.Type))
				}
			}
		}
		return "struct{" + strings.Join(parts, "; ") + "}"
	case *ast.InterfaceType:
		var parts []string
		for _, m := range t.Methods.List {
			if len(m.Names) == 0 {
				parts = append(parts, pr(m.Type))
				continue
			}
			for _, n := range m.Names {
				if ft, ok := m.Type.(*ast.FuncType); ok {
					parts = append(parts, n.Name+strings.TrimPrefix(funcSig(pr, ft), "func"))
				}
			}
		}
		return "interface{" + strings.Join(parts, "; ") + "}"
	}
	return pr(e)
}
