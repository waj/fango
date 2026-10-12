package codegen

import (
	"maps"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

// A callback can be emitted outside an enclosing generic worker only if it
// closes over neither term bindings nor type descriptors. Check all runtime
// types, including call instantiations, with lexical term scopes.
func (g *gen) closedMonomorphicCallback(lam *core.Lambda) bool {
	closed := true
	monomorphic := func(t types.Type) {
		if len(types.RigidVarsIn(t)) != 0 {
			closed = false
		}
	}
	var expr func(core.Expr, map[string]bool)
	var tree func(core.Tree, map[string]bool)
	expr = func(e core.Expr, scope map[string]bool) {
		core.InspectPruned(e, func(e core.Expr) bool {
			if !closed {
				return false
			}
			monomorphic(e.Type())
			switch e := e.(type) {
			case *core.VarRef:
				for _, a := range e.TyArgs {
					monomorphic(a)
				}
				if (e.Local || g.defs[e.Name] == nil) && !scope[e.Name] {
					closed = false
				}
			case *core.App:
				for _, a := range e.TyArgs {
					monomorphic(a)
				}
				for _, a := range e.Witnesses {
					monomorphic(a)
				}
				if e.CalleeKind == core.Ctor {
					for _, a := range e.Args {
						expr(a, scope)
					}
					return false
				}
			case *core.Handle, *core.Perform, *core.ControlExit, *core.ResumeTail:
				closed = false
				return false
			case *core.Lambda:
				inner := maps.Clone(scope)
				inner[e.Param] = true
				expr(e.Body, inner)
				return false
			case *core.Let:
				inner := maps.Clone(scope)
				inner[e.Name] = true
				if e.Rec {
					expr(e.Rhs, inner)
				} else {
					expr(e.Rhs, scope)
				}
				expr(e.Body, inner)
				return false
			case *core.Case:
				expr(e.Scrut, scope)
				inner := maps.Clone(scope)
				inner[e.Bind] = true
				tree(e.Tree, inner)
				return false
			}
			return closed
		})
	}
	tree = func(t core.Tree, scope map[string]bool) {
		switch t := t.(type) {
		case nil, *core.Unreachable:
		case *core.Leaf:
			expr(t.Body, scope)
		case *core.Guard:
			expr(t.Cond, scope)
			tree(t.Then, scope)
			tree(t.Else, scope)
		case *core.SwitchLit:
			if !scope[t.Scrut] {
				closed = false
			}
			for _, c := range t.Cases {
				tree(c.Tree, scope)
			}
			tree(t.Default, scope)
		case *core.SwitchCtor:
			if !scope[t.Scrut] {
				closed = false
			}
			for _, c := range t.Cases {
				inner := maps.Clone(scope)
				for _, b := range c.Binds {
					inner[b] = true
				}
				tree(c.Tree, inner)
			}
			tree(t.Default, scope)
		default:
			closed = false
		}
	}
	expr(lam, map[string]bool{})
	return closed
}
