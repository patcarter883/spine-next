// Package builtin assembles the v2.0 tool set (RFC §5.1).
package builtin

import (
	"github.com/patcarter883/spine-next/internal/tools"
	"github.com/patcarter883/spine-next/internal/tools/ast"
	"github.com/patcarter883/spine-next/internal/tools/build"
	"github.com/patcarter883/spine-next/internal/tools/diff"
)

// New returns a registry with every built-in tool registered.
func New(o tools.Options, runner *build.Runner) *tools.Registry {
	if runner == nil {
		runner = &build.Runner{}
	}
	r := tools.New(o)
	r.MustRegister(diff.Tools()...)
	r.MustRegister(runner.Tools()...)
	r.MustRegister(ast.Tools(nil)...)
	return r
}
