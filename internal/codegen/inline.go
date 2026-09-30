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
// closures, even inside a worker loop. Other expression positions retain an
// ordinary literal call and let Go decide whether to inline it.
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
			for i, stmt := range block.List {
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
	if left == nil || right == nil || len(left.List) != 1 || len(right.List) != 1 {
		return false
	}
	var a, b bytes.Buffer
	if format.Node(&a, token.NewFileSet(), left.List[0].Type) != nil || format.Node(&b, token.NewFileSet(), right.List[0].Type) != nil {
		return false
	}
	return bytes.Equal(a.Bytes(), b.Bytes())
}

func inlineableReturnCall(fn *goast.FuncLit) bool {
	if fn.Type.Results == nil || len(fn.Type.Results.List) != 1 || len(fn.Type.Results.List[0].Names) != 0 {
		return false
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
		case *goast.CallExpr:
			if ref, ok := node.Fun.(*goast.Ident); ok && ref.Name == "recover" {
				safe = false
			}
		}
		return safe
	})
	return safe
}
