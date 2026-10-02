package codegen

import (
	goast "go/ast"
	gotoken "go/token"
	"reflect"
	"strings"

	"github.com/waj/fango/internal/core"
)

// A bound operation callback sometimes passes its fixed evidence through an
// intermediate callable whose whole body forwards to that operation. Call the
// slot directly in the bound callback; the evidence still comes from the
// definition site, so later handler shadowing cannot change its target.
func (g *gen) inlineBoundOperations(file *goast.File) {
	if g.disableOptimizations {
		return
	}
	goast.Inspect(file, func(node goast.Node) bool {
		block, ok := node.(*goast.BlockStmt)
		if !ok {
			return true
		}
		for i := 0; i < len(block.List); i++ {
			name, members := operationForwarder(block.List[i])
			if name == "" || !boundForwarderStable(block.List[i+1:], name) {
				continue
			}
			changed := false
			for _, following := range block.List[i+1:] {
				changed = g.rewriteBoundOperation(following, name, members) || changed
			}
			if changed {
				// The declaration is pure, and Go still needs it marked as used.
				block.List = append(block.List[:i+1], append([]goast.Stmt{assignBlank(ident(name))}, block.List[i+1:]...)...)
				i++
			}
		}
		return true
	})
}

// A later assignment, address-taking use, or shadowing declaration would
// invalidate replacement of the local callable by its original operation.
func boundForwarderStable(statements []goast.Stmt, name string) bool {
	stable := true
	for _, stmt := range statements {
		goast.Inspect(stmt, func(node goast.Node) bool {
			if !stable {
				return false
			}
			switch node := node.(type) {
			case *goast.ValueSpec:
				for _, declared := range node.Names {
					stable = stable && declared.Name != name
				}
			case *goast.AssignStmt:
				for _, lhs := range node.Lhs {
					if boundForwarderTarget(lhs, name) {
						stable = false
					}
				}
			case *goast.IncDecStmt:
				if boundForwarderTarget(node.X, name) {
					stable = false
				}
			case *goast.RangeStmt:
				for _, lhs := range []goast.Expr{node.Key, node.Value} {
					if id, ok := lhs.(*goast.Ident); ok && id.Name == name {
						stable = false
					}
				}
			case *goast.FuncLit:
				for _, fields := range []*goast.FieldList{node.Type.Params, node.Type.Results} {
					if fields == nil {
						continue
					}
					for _, param := range fields.List {
						for _, declared := range param.Names {
							stable = stable && declared.Name != name
						}
					}
				}
			case *goast.UnaryExpr:
				if node.Op == gotoken.AND && boundForwarderTarget(node.X, name) {
					stable = false
				}
			}
			return stable
		})
		if !stable {
			return false
		}
	}
	return true
}

func boundForwarderTarget(expr goast.Expr, name string) bool {
	for {
		switch target := expr.(type) {
		case *goast.Ident:
			return target.Name == name
		case *goast.SelectorExpr:
			expr = target.X
		default:
			return false
		}
	}
}

type operationForward struct {
	op       string
	argParam []int
	params   int
	results  int
	voidUnit bool
}

func operationForwarder(stmt goast.Stmt) (string, map[string]operationForward) {
	decl, ok := stmt.(*goast.DeclStmt)
	if !ok {
		return "", nil
	}
	gen, ok := decl.Decl.(*goast.GenDecl)
	if !ok || len(gen.Specs) != 1 {
		return "", nil
	}
	spec, ok := gen.Specs[0].(*goast.ValueSpec)
	if !ok || len(spec.Names) != 1 || len(spec.Values) != 1 {
		return "", nil
	}
	lit, ok := spec.Values[0].(*goast.CompositeLit)
	if !ok {
		return "", nil
	}
	members := map[string]operationForward{}
	for _, elt := range lit.Elts {
		field, ok := elt.(*goast.KeyValueExpr)
		if !ok {
			return "", nil
		}
		key, ok := field.Key.(*goast.Ident)
		if !ok || key.Name != "Direct" && key.Name != "Exit" {
			return "", nil
		}
		fn, ok := field.Value.(*goast.FuncLit)
		if !ok {
			return "", nil
		}
		forward, ok := forwardedOperation(fn)
		if !ok {
			return "", nil
		}
		members[key.Name] = forward
	}
	if len(members) != 2 || members["Direct"].op != members["Exit"].op {
		return "", nil
	}
	return spec.Names[0].Name, members
}

func forwardedOperation(fn *goast.FuncLit) (operationForward, bool) {
	var names []string
	for _, field := range fn.Type.Params.List {
		if len(field.Names) == 0 {
			return operationForward{}, false
		}
		for _, name := range field.Names {
			names = append(names, name.Name)
		}
	}
	if len(names) < 2 {
		return operationForward{}, false
	}
	body := fn.Body.List
	for len(body) == 1 {
		inner, ok := body[0].(*goast.BlockStmt)
		if !ok {
			break
		}
		body = inner.List
	}
	var call *goast.CallExpr
	voidUnit := false
	switch {
	case len(body) == 1:
		ret, ok := body[0].(*goast.ReturnStmt)
		if !ok || len(ret.Results) != 1 {
			return operationForward{}, false
		}
		call, ok = ret.Results[0].(*goast.CallExpr)
		if !ok {
			return operationForward{}, false
		}
	case len(body) == 2:
		eval, ok := body[0].(*goast.ExprStmt)
		if !ok {
			return operationForward{}, false
		}
		call, ok = eval.X.(*goast.CallExpr)
		if !ok {
			return operationForward{}, false
		}
		ret, ok := body[1].(*goast.ReturnStmt)
		if !ok || len(ret.Results) != 1 || !unitValue(ret.Results[0]) {
			return operationForward{}, false
		}
		voidUnit = true
	default:
		return operationForward{}, false
	}
	selector, ok := call.Fun.(*goast.SelectorExpr)
	if !ok || !strings.HasPrefix(selector.Sel.Name, "Op_") {
		return operationForward{}, false
	}
	evidence, ok := selector.X.(*goast.Ident)
	if !ok || evidence.Name != names[0] {
		return operationForward{}, false
	}
	forward := operationForward{op: selector.Sel.Name, params: len(names), voidUnit: voidUnit}
	if fn.Type.Results != nil {
		forward.results = fn.Type.Results.NumFields()
	}
	for _, arg := range call.Args {
		ref, ok := arg.(*goast.Ident)
		if !ok {
			return operationForward{}, false
		}
		index := -1
		for i := 2; i < len(names); i++ {
			if names[i] == ref.Name {
				index = i
				break
			}
		}
		if index < 0 {
			return operationForward{}, false
		}
		forward.argParam = append(forward.argParam, index)
	}
	return forward, true
}

func unitValue(expr goast.Expr) bool {
	sel, ok := expr.(*goast.SelectorExpr)
	if !ok || sel.Sel.Name != "UnitValue" {
		return false
	}
	pkg, ok := sel.X.(*goast.Ident)
	return ok && pkg.Name == "fangort"
}

func (g *gen) rewriteBoundOperation(root goast.Node, name string, members map[string]operationForward) bool {
	changed := false
	goast.Inspect(root, func(node goast.Node) bool {
		block, ok := node.(*goast.BlockStmt)
		if !ok {
			return true
		}
		for i, stmt := range block.List {
			ret, ok := stmt.(*goast.ReturnStmt)
			if !ok || len(ret.Results) != 1 {
				continue
			}
			call, ok := ret.Results[0].(*goast.CallExpr)
			if !ok || call.Ellipsis.IsValid() {
				continue
			}
			selector, ok := call.Fun.(*goast.SelectorExpr)
			if !ok {
				continue
			}
			ref, ok := selector.X.(*goast.Ident)
			if !ok || ref.Name != name {
				continue
			}
			forward, ok := members[selector.Sel.Name]
			if !ok || len(call.Args) != forward.params || !boundOperationArgsSafe(call.Args) {
				continue
			}
			args := make([]goast.Expr, 0, len(forward.argParam))
			for _, index := range forward.argParam {
				args = append(args, call.Args[index])
			}
			if stmts := g.directBoundOperation(call.Args[0], forward, args); stmts != nil {
				block.List[i] = &goast.BlockStmt{List: stmts}
				changed = true
				continue
			}
			direct := callExpr(&goast.SelectorExpr{X: call.Args[0], Sel: ident(forward.op)}, args...).(*goast.CallExpr)
			if forward.voidUnit {
				block.List[i] = &goast.BlockStmt{List: []goast.Stmt{exprStmt(direct), returnStmt(&goast.SelectorExpr{X: ident("fangort"), Sel: ident("UnitValue")})}}
			} else {
				ret.Results[0] = direct
			}
			changed = true
		}
		return true
	})
	return changed
}

func boundOperationArgsSafe(args []goast.Expr) bool {
	for i, arg := range args {
		if _, ok := arg.(*goast.Ident); ok {
			continue
		}
		value, ok := arg.(*goast.SelectorExpr)
		if !ok {
			return false
		}
		if i == 0 {
			// The evidence receiver is retained and evaluated once.
			if _, ok := value.X.(*goast.Ident); ok {
				continue
			}
		}
		// A dropped selector could otherwise lose a nil-pointer panic.
		if i == 2 && unitValue(arg) {
			continue
		}
		return false
	}
	return true
}

// directOperation describes one monomorphic clause of a Direct activation:
// whether its Direct slot returns nothing, and its body when the activation
// is fixed and the clause small enough to expand.
type directOperation struct {
	void  bool
	fixed *goast.FuncLit
}

func (g *gen) recordDirectActivation(name string, e *core.Handle) {
	operations := map[string]directOperation{}
	for _, c := range e.Clauses {
		if len(c.LocalVars) > 0 || c.Op.Abort || c.Op.Native != nil {
			continue
		}
		slot := "Op_" + linkName(c.Op.Name)
		operations[slot] = directOperation{void: g.isUnit(c.Op.ResultType), fixed: g.fixedOperations[e][slot]}
	}
	if g.directActivations == nil {
		g.directActivations = map[string]map[string]directOperation{}
	}
	g.directActivations[name] = operations
}

// A bound callback's evidence is often a Direct activation installed in the
// same function. Its Exit view only wraps each Direct slot as a normal result,
// so an Exit member calls the Direct slot and supplies the nil exit itself.
// A fixed activation's small clause is expanded in place of the slot, as a
// Perform against the same lexical evidence would be. Activation names are
// unique and assigned once, so the receiver still denotes that activation.
func (g *gen) directBoundOperation(receiver goast.Expr, forward operationForward, args []goast.Expr) []goast.Stmt {
	evidence, _ := receiver.(*goast.Ident)
	exitView := false
	if view, ok := receiver.(*goast.SelectorExpr); ok && view.Sel.Name == "Exit" {
		evidence, _ = view.X.(*goast.Ident)
		exitView = true
	}
	if evidence == nil {
		return nil
	}
	op, ok := g.directActivations[evidence.Name][forward.op]
	if !ok || !exitView && op.fixed == nil {
		return nil
	}
	var callee goast.Expr = &goast.SelectorExpr{X: ident(evidence.Name), Sel: ident(forward.op)}
	if op.fixed != nil {
		callee = g.cloneOperation(reflect.ValueOf(op.fixed)).Interface().(*goast.FuncLit)
	}
	call := callExpr(callee, args...)
	unit := &goast.SelectorExpr{X: ident("fangort"), Sel: ident("UnitValue")}
	switch {
	case !exitView && forward.voidUnit && op.void:
		return []goast.Stmt{exprStmt(call), returnStmt(unit)}
	case !exitView && !forward.voidUnit && !op.void:
		return []goast.Stmt{returnStmt(call)}
	case exitView && op.void && forward.results == 1:
		return []goast.Stmt{exprStmt(call), returnStmt(ident("nil"))}
	case exitView && op.void && forward.results == 2:
		return []goast.Stmt{exprStmt(call), &goast.ReturnStmt{Results: []goast.Expr{unit, ident("nil")}}}
	case exitView && !op.void && forward.results == 2:
		return []goast.Stmt{&goast.ReturnStmt{Results: []goast.Expr{call, ident("nil")}}}
	}
	return nil
}
