package codegen

import (
	"fmt"
	goast "go/ast"
	gotoken "go/token"
	"maps"
	"reflect"

	"github.com/waj/fango/internal/core"
)

// Result splitting is a typed lowering: calls are marked at their Core
// emission sites, never guessed from names. Outcome remains the adapter at
// runtime APIs; generated calls and locals carry the two components directly.
func (g *gen) markOutcomeCall(call *goast.CallExpr, value goast.Expr) {
	if g.outcomeCalls == nil {
		g.outcomeCalls = map[*goast.CallExpr]goast.Expr{}
	}
	g.outcomeCalls[call] = value
}

func (g *gen) markWorkerOutcomeCall(call *goast.CallExpr, value goast.Expr, d *core.Def) {
	g.markOutcomeCall(call, value)
	_, result := core.PeelFun(d.Type, len(d.Params))
	if g.isUnit(result) {
		if g.unitOutcomeCalls == nil {
			g.unitOutcomeCalls = map[*goast.CallExpr]bool{}
		}
		g.unitOutcomeCalls[call] = true
	}
}

func outcomeArgument(t goast.Expr) goast.Expr {
	inst, ok := t.(*goast.IndexExpr)
	if !ok || !runtimeSelector(inst.X, "Outcome") {
		return nil
	}
	return inst.Index
}

func runtimeSelector(e goast.Expr, name string) bool {
	sel, ok := e.(*goast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return false
	}
	qual, ok := sel.X.(*goast.Ident)
	return ok && qual.Name == "fangort"
}

func genericRuntimeCall(e goast.Expr, name string) (*goast.CallExpr, bool) {
	call, ok := e.(*goast.CallExpr)
	if !ok {
		return nil, false
	}
	head := call.Fun
	if inst, ok := head.(*goast.IndexExpr); ok {
		head = inst.X
	}
	return call, runtimeSelector(head, name)
}

func unitGoType(t goast.Expr) bool { return runtimeSelector(t, "Unit") }

type outcomeLocal struct {
	value, exit string
	typ         goast.Expr
}
type outcomeLowering struct {
	g               *gen
	results         map[*goast.FuncType]goast.Expr
	done            map[*goast.FuncType]bool
	bridges         map[*goast.FuncType]bool
	singleFunctions map[*goast.FuncType]bool
}

func (g *gen) splitOutcomeABI(file *goast.File) {
	if g.disableOptimizations {
		return
	}
	l := &outcomeLowering{g: g, results: map[*goast.FuncType]goast.Expr{}, done: map[*goast.FuncType]bool{}, bridges: map[*goast.FuncType]bool{}, singleFunctions: map[*goast.FuncType]bool{}}
	goast.Inspect(file, func(n goast.Node) bool {
		if fn, ok := n.(*goast.FuncDecl); ok && fn.Type.Results != nil && len(fn.Type.Results.List) == 1 {
			if value := outcomeArgument(fn.Type.Results.List[0].Type); value != nil && unitGoType(value) {
				l.singleFunctions[fn.Type] = true
			}
		}
		if fn, ok := n.(*goast.FuncType); ok && fn.Results != nil && len(fn.Results.List) == 1 {
			if value := outcomeArgument(fn.Results.List[0].Type); value != nil {
				l.results[fn] = value
			}
		}
		return true
	})
	for _, declaration := range file.Decls {
		if fn, ok := declaration.(*goast.FuncDecl); ok {
			l.function(fn.Type, fn.Body, nil)
		} else {
			l.rewrite(reflect.ValueOf(declaration), nil)
		}
	}
	// Function fields and adapter parameter signatures have no body.
	for fn, value := range l.results {
		l.signature(fn, value)
	}
}

func (l *outcomeLowering) signature(fn *goast.FuncType, value goast.Expr) {
	fields := []*goast.Field{}
	if !l.singleFunctions[fn] {
		fields = append(fields, &goast.Field{Type: value})
	}
	fields = append(fields, &goast.Field{Type: &goast.StarExpr{X: selector("fangort", "ExitRequest")}})
	fn.Results = &goast.FieldList{List: fields}
}

func (l *outcomeLowering) function(fn *goast.FuncType, body *goast.BlockStmt, env map[string]outcomeLocal) {
	if body == nil || l.done[fn] || l.bridges[fn] {
		return
	}
	l.done[fn] = true
	value := l.results[fn]
	if value != nil {
		l.signature(fn, value)
	}
	l.block(body, value, env, l.singleFunctions[fn])
}

func (l *outcomeLowering) fresh(stem string) string {
	name := fmt.Sprintf("t_%s%d", stem, l.g.tmp)
	l.g.tmp++
	return name
}

func (l *outcomeLowering) pair(e goast.Expr) (goast.Expr, bool) {
	call, ok := e.(*goast.CallExpr)
	if !ok {
		return nil, false
	}
	if value, ok := l.g.outcomeCalls[call]; ok {
		return value, true
	}
	if fn, ok := call.Fun.(*goast.FuncLit); ok {
		value, ok := l.results[fn.Type]
		return value, ok
	}
	return nil, false
}

func (l *outcomeLowering) rawCall(e goast.Expr, env map[string]outcomeLocal) goast.Expr {
	call := e.(*goast.CallExpr)
	call.Fun = l.expr(call.Fun, env)
	for i, arg := range call.Args {
		call.Args[i] = l.expr(arg, env)
	}
	return call
}

func (l *outcomeLowering) expr(e goast.Expr, env map[string]outcomeLocal) goast.Expr {
	if e == nil {
		return nil
	}
	if fn, ok := e.(*goast.FuncLit); ok {
		l.function(fn.Type, fn.Body, env)
		return fn
	}
	if id, ok := e.(*goast.Ident); ok {
		if local, ok := env[id.Name]; ok {
			return &goast.CompositeLit{Type: indexExpr(selector("fangort", "Outcome"), []goast.Expr{local.typ}), Elts: []goast.Expr{
				&goast.KeyValueExpr{Key: ident("Value"), Value: ident(local.value)},
				&goast.KeyValueExpr{Key: ident("Exit"), Value: ident(local.exit)},
			}}
		}
		return e
	}
	if sel, ok := e.(*goast.SelectorExpr); ok {
		if id, ok := sel.X.(*goast.Ident); ok {
			if local, ok := env[id.Name]; ok {
				switch sel.Sel.Name {
				case "Value":
					return ident(local.value)
				case "Exit":
					return ident(local.exit)
				}
			}
		}
	}
	if value, ok := l.pair(e); ok {
		call := l.rawCall(e, env)
		v, exit := l.fresh("bridgeValue"), l.fresh("bridgeExit")
		var body []goast.Stmt
		if l.singleCall(e) {
			body = append(body, varDeclStmt(exit, &goast.StarExpr{X: selector("fangort", "ExitRequest")}, call))
		} else {
			body = append(body, varDeclNoValue(v, value), varDeclNoValue(exit, &goast.StarExpr{X: selector("fangort", "ExitRequest")}), &goast.AssignStmt{Lhs: []goast.Expr{ident(v), ident(exit)}, Tok: gotoken.ASSIGN, Rhs: []goast.Expr{call}})
		}
		val := goast.Expr(ident(v))
		if l.singleCall(e) {
			val = selector("fangort", "UnitValue")
		}
		typ := indexExpr(selector("fangort", "Outcome"), []goast.Expr{value})
		body = append(body, returnStmt(&goast.CompositeLit{Type: typ, Elts: []goast.Expr{&goast.KeyValueExpr{Key: ident("Value"), Value: val}, &goast.KeyValueExpr{Key: ident("Exit"), Value: ident(exit)}}}))
		bridge := funcLit(typ, body).(*goast.FuncLit)
		l.bridges[bridge.Type] = true
		return callExpr(bridge)
	}
	l.rewrite(reflect.ValueOf(e), env)
	return e
}

// Rewrite child expression slots, stopping at function bodies. Reflection
// supplies parent slots without relying on textual Go or ast.Ident.Obj.
func (l *outcomeLowering) rewrite(v reflect.Value, env map[string]outcomeLocal) {
	if !v.IsValid() {
		return
	}
	if v.Kind() == reflect.Interface {
		if v.IsNil() {
			return
		}
		if e, ok := v.Interface().(goast.Expr); ok && v.CanSet() {
			v.Set(reflect.ValueOf(l.expr(e, env)))
			return
		}
		l.rewrite(v.Elem(), env)
		return
	}
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return
		}
		if fn, ok := v.Interface().(*goast.FuncLit); ok {
			l.function(fn.Type, fn.Body, env)
			return
		}
		l.rewrite(v.Elem(), env)
		return
	}
	switch v.Kind() {
	case reflect.Struct:
		for i := range v.NumField() {
			l.rewrite(v.Field(i), env)
		}
	case reflect.Slice:
		for i := range v.Len() {
			l.rewrite(v.Index(i), env)
		}
	}
}

func (l *outcomeLowering) assign(local outcomeLocal, e goast.Expr, env map[string]outcomeLocal) []goast.Stmt {
	if _, ok := l.pair(e); ok {
		call := l.rawCall(e, env)
		if l.singleCall(e) {
			return []goast.Stmt{assignStmt(local.value, selector("fangort", "UnitValue")), assignStmt(local.exit, call)}
		}
		return []goast.Stmt{&goast.AssignStmt{Lhs: []goast.Expr{ident(local.value), ident(local.exit)}, Tok: gotoken.ASSIGN, Rhs: []goast.Expr{call}}}
	}
	if call, ok := genericRuntimeCall(e, "Normal"); ok {
		return []goast.Stmt{assignStmt(local.value, l.expr(call.Args[0], env)), assignStmt(local.exit, ident("nil"))}
	}
	if call, ok := genericRuntimeCall(e, "Propagate"); ok {
		zero := l.fresh("zero")
		return []goast.Stmt{varDeclNoValue(zero, local.typ), assignStmt(local.value, ident(zero)), assignStmt(local.exit, l.expr(call.Args[0], env))}
	}
	if id, ok := e.(*goast.Ident); ok {
		if source, ok := env[id.Name]; ok {
			return []goast.Stmt{&goast.AssignStmt{Lhs: []goast.Expr{ident(local.value), ident(local.exit)}, Tok: gotoken.ASSIGN, Rhs: []goast.Expr{ident(source.value), ident(source.exit)}}}
		}
	}
	if literal, ok := e.(*goast.CompositeLit); ok && outcomeArgument(literal.Type) != nil {
		var value, exit goast.Expr = nil, ident("nil")
		for _, field := range literal.Elts {
			kv := field.(*goast.KeyValueExpr)
			switch kv.Key.(*goast.Ident).Name {
			case "Value":
				value = l.expr(kv.Value, env)
			case "Exit":
				exit = l.expr(kv.Value, env)
			}
		}
		var out []goast.Stmt
		if value == nil {
			zero := l.fresh("zero")
			out = append(out, varDeclNoValue(zero, local.typ))
			value = ident(zero)
		}
		return append(out, assignStmt(local.value, value), assignStmt(local.exit, exit))
	}
	boundary := l.fresh("boundary")
	return []goast.Stmt{varDeclStmt(boundary, indexExpr(selector("fangort", "Outcome"), []goast.Expr{local.typ}), l.expr(e, env)), assignStmt(local.value, selector(boundary, "Value")), assignStmt(local.exit, selector(boundary, "Exit"))}
}

func (l *outcomeLowering) block(block *goast.BlockStmt, result goast.Expr, inherited map[string]outcomeLocal, single bool) {
	env := maps.Clone(inherited)
	if env == nil {
		env = map[string]outcomeLocal{}
	}
	var out []goast.Stmt
	for _, stmt := range block.List {
		switch s := stmt.(type) {
		case *goast.DeclStmt:
			decl, ok := s.Decl.(*goast.GenDecl)
			if ok && decl.Tok == gotoken.VAR && len(decl.Specs) == 1 {
				spec := decl.Specs[0].(*goast.ValueSpec)
				if typ := outcomeArgument(spec.Type); typ != nil && len(spec.Names) == 1 {
					local := outcomeLocal{l.fresh("value"), l.fresh("exit"), typ}
					out = append(out, varDeclNoValue(local.value, typ), varDeclNoValue(local.exit, &goast.StarExpr{X: selector("fangort", "ExitRequest")}))
					if len(spec.Values) != 0 {
						out = append(out, l.assign(local, spec.Values[0], env)...)
					}
					env[spec.Names[0].Name] = local
					out = append(out, assignBlank(ident(local.value)), assignBlank(ident(local.exit)))
					continue
				}
			}
			l.rewrite(reflect.ValueOf(s), env)
		case *goast.AssignStmt:
			if len(s.Lhs) == 1 && len(s.Rhs) == 1 {
				if id, ok := s.Lhs[0].(*goast.Ident); ok {
					if local, ok := env[id.Name]; ok {
						out = append(out, l.assign(local, s.Rhs[0], env)...)
						continue
					}
				}
			}
			l.rewrite(reflect.ValueOf(s), env)
		case *goast.ReturnStmt:
			if result != nil && len(s.Results) == 1 {
				if call, ok := genericRuntimeCall(s.Results[0], "Normal"); ok {
					value := l.expr(call.Args[0], env)
					if single {
						if !runtimeSelector(value, "UnitValue") {
							out = append(out, assignBlank(value))
						}
						out = append(out, returnStmt(ident("nil")))
					} else {
						out = append(out, &goast.ReturnStmt{Results: []goast.Expr{value, ident("nil")}})
					}
					continue
				}
				if call, ok := genericRuntimeCall(s.Results[0], "Propagate"); ok {
					exit := l.expr(call.Args[0], env)
					if single {
						out = append(out, returnStmt(exit))
					} else {
						zero := l.fresh("zero")
						out = append(out, varDeclNoValue(zero, result), &goast.ReturnStmt{Results: []goast.Expr{ident(zero), exit}})
					}
					continue
				}
				if id, ok := s.Results[0].(*goast.Ident); ok {
					if local, ok := env[id.Name]; ok {
						if single {
							out = append(out, returnStmt(ident(local.exit)))
						} else {
							out = append(out, &goast.ReturnStmt{Results: []goast.Expr{ident(local.value), ident(local.exit)}})
						}
						continue
					}
				}
				if _, ok := l.pair(s.Results[0]); ok && l.singleCall(s.Results[0]) == single {
					out = append(out, returnStmt(l.rawCall(s.Results[0], env)))
					continue
				}
				local := outcomeLocal{l.fresh("returnValue"), l.fresh("returnExit"), result}
				out = append(out, varDeclNoValue(local.value, result), varDeclNoValue(local.exit, &goast.StarExpr{X: selector("fangort", "ExitRequest")}))
				out = append(out, l.assign(local, s.Results[0], env)...)
				if single {
					out = append(out, assignBlank(ident(local.value)), returnStmt(ident(local.exit)))
				} else {
					out = append(out, &goast.ReturnStmt{Results: []goast.Expr{ident(local.value), ident(local.exit)}})
				}
				continue
			}
			l.rewrite(reflect.ValueOf(s), env)
		case *goast.IfStmt:
			l.rewrite(reflect.ValueOf(&s.Init).Elem(), env)
			s.Cond = l.expr(s.Cond, env)
			l.block(s.Body, result, env, single)
			if other, ok := s.Else.(*goast.BlockStmt); ok {
				l.block(other, result, env, single)
			} else if s.Else != nil {
				wrapper := &goast.BlockStmt{List: []goast.Stmt{s.Else}}
				l.block(wrapper, result, env, single)
				s.Else = wrapper.List[0]
			}
		case *goast.BlockStmt:
			l.block(s, result, env, single)
		case *goast.ForStmt:
			l.rewrite(reflect.ValueOf(&s.Init).Elem(), env)
			s.Cond = l.expr(s.Cond, env)
			l.rewrite(reflect.ValueOf(&s.Post).Elem(), env)
			l.block(s.Body, result, env, single)
		case *goast.SwitchStmt:
			l.rewrite(reflect.ValueOf(&s.Init).Elem(), env)
			s.Tag = l.expr(s.Tag, env)
			l.cases(s.Body, result, env, single)
		case *goast.TypeSwitchStmt:
			l.rewrite(reflect.ValueOf(&s.Init).Elem(), env)
			l.rewrite(reflect.ValueOf(s.Assign), env)
			l.cases(s.Body, result, env, single)
		default:
			l.rewrite(reflect.ValueOf(s), env)
		}
		out = append(out, stmt)
	}
	block.List = out
}

func (l *outcomeLowering) cases(body *goast.BlockStmt, result goast.Expr, env map[string]outcomeLocal, single bool) {
	for _, stmt := range body.List {
		clause := stmt.(*goast.CaseClause)
		for i, e := range clause.List {
			clause.List[i] = l.expr(e, env)
		}
		block := &goast.BlockStmt{List: clause.Body}
		l.block(block, result, env, single)
		clause.Body = block.List
	}
}

func (l *outcomeLowering) singleCall(e goast.Expr) bool {
	call, ok := e.(*goast.CallExpr)
	if !ok {
		return false
	}
	if fn, ok := call.Fun.(*goast.FuncLit); ok {
		return l.singleFunctions[fn.Type]
	}
	return l.g.unitOutcomeCalls[call]
}
