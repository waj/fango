package modules

import (
	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/fixity"
)

// Scope is the unqualified view Prelude.fango's exposing lists produce,
// mapping each surface name to its canonical one. The batch resolver and the
// REPL prompt reach the same view by applying those imports; the focused
// checker tests have no name resolver, so they bind these names directly and
// read the list from here rather than repeating it.
type Scope struct {
	Values, Types, Ctors, Ops map[string]string
}

// PreludeResult is the resolved bundled prelude. Owners identifies the
// complete resolved module set for test projections.
type PreludeResult struct {
	Module   *ast.Module
	Fixities fixity.Table
	Owners   map[string]bool
	Scope    Scope
}

// Prelude resolves the actual bundled sources and their transitive bundled
// dependencies into a fresh, bundled-only graph. The roots and the shape of
// the result are described at Graph.loadPrelude; a REPL session builds the
// same prelude through NewGraph and keeps the graph to import into.
func Prelude() (*PreludeResult, []diag.Error) {
	return newGraph(nil).loadPrelude()
}

// preludeScope projects Prelude.fango's imports through the imported modules'
// public interfaces, which is exactly what the batch resolver merges into
// each module.
func preludeScope(nodes map[string]*node) (Scope, []diag.Error) {
	s := Scope{Values: map[string]string{}, Types: map[string]string{}, Ctors: map[string]string{}, Ops: map[string]string{}}
	prelude := nodes[PreludeModule]
	if prelude == nil {
		return s, []diag.Error{{Title: "INVALID EMBEDDED PRELUDE", Body: "The bundled " + PreludeModule + " module is missing."}}
	}
	var errs []diag.Error
	for _, im := range prelude.mod.Imports {
		dep := nodes[im.Module]
		if dep == nil || dep.iface == nil {
			continue
		}
		sel, es := dep.iface.selection(im.Exposing, im.ModuleSpan)
		errs = append(errs, es...)
		for k, v := range sel.values {
			s.Values[k] = v
		}
		for k, v := range sel.types {
			s.Types[k] = v
		}
		for k, v := range sel.ctors {
			s.Ctors[k] = v
		}
		for k, v := range sel.ops {
			s.Ops[k] = v
		}
	}
	return s, errs
}
