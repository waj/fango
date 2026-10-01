package codegen

import (
	"bytes"
	"fmt"
	goast "go/ast"
	"go/format"
	"go/token"
)

// A returned literal call has the enclosing function's return continuation.
// Move its body into a lexical block, evaluating all arguments before binding
// parameters. Separate bindings preserve snapshots captured by escaping
// closures, even inside a worker loop. Binding and statement positions use
// typed joins; other expression positions retain an ordinary literal call.
func (g *gen) inlineReturnCalls(file *goast.File) {
	var visit func(goast.Node, *goast.FieldList)
	visit = func(root goast.Node, results *goast.FieldList) {
		goast.Inspect(root, func(node goast.Node) bool {
			switch fn := node.(type) {
			case *goast.FuncDecl:
				if fn.Body != nil {
					visit(fn.Body, fn.Type.Results)
				}
				return false
			case *goast.FuncLit:
				visit(fn.Body, fn.Type.Results)
				return false
			}
			block, ok := node.(*goast.BlockStmt)
			if !ok {
				return true
			}
			for i := 0; i < len(block.List); i++ {
				stmt := block.List[i]
				if expanded, ok := g.inlineAssignedCall(stmt); ok {
					if _, declaration := stmt.(*goast.DeclStmt); declaration {
						expandedBlock := expanded.(*goast.BlockStmt)
						replacement := []goast.Stmt{expandedBlock.List[0], &goast.BlockStmt{List: expandedBlock.List[1:]}}
						tail := append([]goast.Stmt(nil), block.List[i+1:]...)
						block.List = append(append(block.List[:i], replacement...), tail...)
						i++
					} else {
						block.List[i] = expanded
					}
					continue
				}
				ret, ok := stmt.(*goast.ReturnStmt)
				if !ok || len(ret.Results) != 1 {
					continue
				}
				call, ok := ret.Results[0].(*goast.CallExpr)
				if !ok || call.Ellipsis.IsValid() {
					continue
				}
				fn, ok := call.Fun.(*goast.FuncLit)
				if !ok || !inlineableReturnCall(fn) || !sameReturnType(results, fn.Type.Results) {
					continue
				}
				arity := 0
				for _, field := range fn.Type.Params.List {
					arity += len(field.Names)
				}
				if arity != len(call.Args) {
					continue
				}
				if independentInlineArguments(fn, call.Args) {
					var body []goast.Stmt
					index := 0
					for _, field := range fn.Type.Params.List {
						for _, param := range field.Names {
							if param.Name == "_" {
								body = append(body, assignBlank(call.Args[index]))
							} else {
								body = append(body, varDeclStmt(param.Name, field.Type, call.Args[index]), assignBlank(ident(param.Name)))
							}
							index++
						}
					}
					block.List[i] = &goast.BlockStmt{List: append(body, fn.Body.List...)}
					continue
				}
				var arguments, bindings []goast.Stmt
				index := 0
				for _, field := range fn.Type.Params.List {
					for _, param := range field.Names {
						arg := call.Args[index]
						index++
						if param.Name == "_" {
							arguments = append(arguments, assignBlank(arg))
							continue
						}
						name := fmt.Sprintf("t_inlineArg%d", g.tmp)
						g.tmp++
						arguments = append(arguments, varDeclStmt(name, field.Type, arg))
						bindings = append(bindings, varDeclStmt(param.Name, field.Type, ident(name)), assignBlank(ident(param.Name)))
					}
				}
				inner := &goast.BlockStmt{List: append(bindings, fn.Body.List...)}
				block.List[i] = &goast.BlockStmt{List: append(arguments, inner)}
			}
			return true
		})
	}
	visit(file, nil)
}

// Direct parameter initializers need no argument temporaries when earlier
// bindings cannot shadow names in later arguments. A parameter is not in
// scope in its own initializer, so var x T = x reads the outer x correctly.
func independentInlineArguments(fn *goast.FuncLit, args []goast.Expr) bool {
	bound := map[string]bool{}
	index := 0
	for _, field := range fn.Type.Params.List {
		for _, param := range field.Names {
			independent := true
			goast.Inspect(args[index], func(node goast.Node) bool {
				if ref, ok := node.(*goast.Ident); ok && bound[ref.Name] {
					independent = false
				}
				return independent
			})
			if !independent {
				return false
			}
			if param.Name != "_" {
				bound[param.Name] = true
			}
			index++
		}
	}
	return true
}

// Exact result types preserve implicit conversions of untyped literals and
// interface boxing. Assignability alone would change a returned Int literal
// into Go's default int when the enclosing result is any.
func sameReturnType(left, right *goast.FieldList) bool {
	if left == nil || right == nil || len(left.List) != len(right.List) {
		return false
	}
	for i := range left.List {
		var a, b bytes.Buffer
		if format.Node(&a, token.NewFileSet(), left.List[i].Type) != nil || format.Node(&b, token.NewFileSet(), right.List[i].Type) != nil || !bytes.Equal(a.Bytes(), b.Bytes()) {
			return false
		}
	}
	return true
}

func inlineableReturnCall(fn *goast.FuncLit) bool {
	var results []*goast.Field
	if fn.Type.Results != nil {
		results = fn.Type.Results.List
	}
	for _, result := range results {
		if len(result.Names) != 0 {
			return false
		}
	}
	for _, field := range fn.Type.Params.List {
		if len(field.Names) == 0 {
			return false
		}
		if _, variadic := field.Type.(*goast.Ellipsis); variadic {
			return false
		}
	}
	// A defer or recover belongs to its original function boundary. Inspect
	// only this body: nested functions keep their own boundaries unchanged.
	safe := true
	goast.Inspect(fn.Body, func(node goast.Node) bool {
		switch node := node.(type) {
		case *goast.FuncLit:
			return false
		case *goast.DeferStmt:
			safe = false
		case *goast.LabeledStmt:
			// Go labels have function scope, even inside lexical blocks.
			// Repeated expansion must not duplicate a callee's labels.
			safe = false
		case *goast.CallExpr:
			if ref, ok := node.Fun.(*goast.Ident); ok && ref.Name == "recover" {
				safe = false
			}
		}
		return safe
	})
	return safe
}

// An immediate call in a binding has a destination, so it can be lowered to
// statements too. Typed result slots preserve implicit conversions. A jump
// out of the private parameter/body block keeps all exits at the same join
// and preserves snapshots retained by escaping closures.
func (g *gen) inlineAssignedCall(stmt goast.Stmt) (goast.Stmt, bool) {
	var call *goast.CallExpr
	var lhs []goast.Expr
	var declaration goast.Stmt
	switch s := stmt.(type) {
	case *goast.ExprStmt:
		call, _ = s.X.(*goast.CallExpr)
	case *goast.AssignStmt:
		if s.Tok != token.ASSIGN || len(s.Rhs) != 1 {
			return nil, false
		}
		call, _ = s.Rhs[0].(*goast.CallExpr)
		lhs = s.Lhs
	case *goast.DeclStmt:
		d, ok := s.Decl.(*goast.GenDecl)
		if !ok || d.Tok != token.VAR || len(d.Specs) != 1 {
			return nil, false
		}
		v, ok := d.Specs[0].(*goast.ValueSpec)
		if !ok || len(v.Names) != 1 || len(v.Values) != 1 || v.Type == nil {
			return nil, false
		}
		call, _ = v.Values[0].(*goast.CallExpr)
		if call == nil {
			return nil, false
		}
		lhs = []goast.Expr{v.Names[0]}
		// Hoisting the declaration must not change a reference in its
		// initializer from an outer binding to the newly declared one.
		shadowed := false
		goast.Inspect(call, func(n goast.Node) bool {
			if id, ok := n.(*goast.Ident); ok && id.Name == v.Names[0].Name {
				shadowed = true
			}
			return !shadowed
		})
		if shadowed {
			return nil, false
		}
		declaration = varDeclNoValue(v.Names[0].Name, v.Type)
	default:
		return nil, false
	}
	if call == nil || call.Ellipsis.IsValid() {
		return nil, false
	}
	for _, destination := range lhs {
		if _, ok := destination.(*goast.Ident); !ok {
			// Index and selector operands evaluate before the RHS in Go.
			// Keep their call boundary rather than delay that evaluation.
			return nil, false
		}
	}
	fn, ok := call.Fun.(*goast.FuncLit)
	if !ok || !inlineableReturnCall(fn) {
		return nil, false
	}
	var fields []*goast.Field
	if fn.Type.Results != nil {
		fields = fn.Type.Results.List
	}
	if len(fields) != len(lhs) {
		return nil, false
	}
	var arguments, bindings []goast.Stmt
	index := 0
	for _, field := range fn.Type.Params.List {
		for _, param := range field.Names {
			if index >= len(call.Args) {
				return nil, false
			}
			arg := call.Args[index]
			index++
			if param.Name == "_" {
				arguments = append(arguments, assignBlank(arg))
				continue
			}
			name := fmt.Sprintf("t_inlineArg%d", g.tmp)
			g.tmp++
			arguments = append(arguments, varDeclStmt(name, field.Type, arg))
			bindings = append(bindings, varDeclStmt(param.Name, field.Type, ident(name)), assignBlank(ident(param.Name)))
		}
	}
	if index != len(call.Args) {
		return nil, false
	}
	label := fmt.Sprintf("t_join%d", g.tmp)
	g.tmp++
	var results []goast.Expr
	for _, result := range fields {
		name := fmt.Sprintf("t_inlineResult%d", g.tmp)
		g.tmp++
		arguments = append(arguments, varDeclNoValue(name, result.Type))
		results = append(results, ident(name))
	}
	returns := 0
	var replace func(goast.Node)
	replace = func(root goast.Node) {
		goast.Inspect(root, func(n goast.Node) bool {
			if _, nested := n.(*goast.FuncLit); nested {
				return false
			}
			if block, ok := n.(*goast.BlockStmt); ok {
				for i, statement := range block.List {
					if ret, ok := statement.(*goast.ReturnStmt); ok {
						returns++
						statements := []goast.Stmt{}
						if len(results) > 0 {
							statements = append(statements, &goast.AssignStmt{Lhs: results, Tok: token.ASSIGN, Rhs: ret.Results})
						}
						statements = append(statements, &goast.BranchStmt{Tok: token.GOTO, Label: ident(label)})
						block.List[i] = &goast.BlockStmt{List: statements}
					}
				}
			}
			if clause, ok := n.(*goast.CaseClause); ok {
				block := &goast.BlockStmt{List: clause.Body}
				replace(block)
				clause.Body = block.List
				return false
			}
			return true
		})
	}
	replace(fn.Body)
	if returns == 0 && len(results) != 0 {
		return nil, false
	}
	inner := &goast.BlockStmt{List: append(bindings, fn.Body.List...)}
	body := append(arguments, inner)
	if returns != 0 {
		body = append(body, &goast.LabeledStmt{Label: ident(label), Stmt: &goast.EmptyStmt{}})
	}
	if len(lhs) > 0 {
		body = append(body, &goast.AssignStmt{Lhs: lhs, Tok: token.ASSIGN, Rhs: results})
	}
	if declaration != nil {
		return &goast.BlockStmt{List: append([]goast.Stmt{declaration}, body...)}, true
	}
	return &goast.BlockStmt{List: body}, true
}
