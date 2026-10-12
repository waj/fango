package codegen

import (
	"fmt"
	goast "go/ast"
	gotoken "go/token"
)

// splitCallbackLocals retains closure creation at its original position, but
// exposes selected members of an immutable callback bundle as function locals.
// Go can devirtualize those locals; a struct-field projection blocks that even
// after Core has eliminated the record that originally required the bundle.
func (g *gen) splitCallbackLocals(file *goast.File) {
	aliases := map[string]bool{}
	for _, name := range g.callableNames {
		aliases[name] = true
	}
	if len(aliases) == 0 {
		return
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*goast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		candidate := false
		goast.Inspect(fn.Body, func(node goast.Node) bool {
			if literal, ok := node.(*goast.CompositeLit); ok && aliases[callbackAliasName(literal.Type)] {
				candidate = true
			}
			return !candidate
		})
		if !candidate {
			continue
		}
		c := newCopyEliding(fn)
		changed := false
		var visit func(func([]goast.Stmt), []goast.Stmt)
		visit = func(set func([]goast.Stmt), list []goast.Stmt) {
			for i := 0; i < len(list); i++ {
				c.visitNested(list[i], visit)
				declaration, ok := list[i].(*goast.DeclStmt)
				if !ok {
					continue
				}
				gen, ok := declaration.Decl.(*goast.GenDecl)
				if !ok || gen.Tok != gotoken.VAR || len(gen.Specs) != 1 {
					continue
				}
				spec, ok := gen.Specs[0].(*goast.ValueSpec)
				if !ok || len(spec.Names) != 1 || len(spec.Values) != 1 {
					continue
				}
				literal, ok := spec.Values[0].(*goast.CompositeLit)
				if !ok || !aliases[callbackAliasName(literal.Type)] {
					continue
				}
				name := spec.Names[0].Name
				if len(c.decls[name]) != 1 || len(c.assigns[name]) != 0 || c.addressed[name] {
					continue
				}
				members := map[string]*goast.FuncLit{}
				valid := true
				for _, elt := range literal.Elts {
					kv, ok := elt.(*goast.KeyValueExpr)
					if !ok {
						valid = false
						break
					}
					key, ok := kv.Key.(*goast.Ident)
					if !ok {
						valid = false
						break
					}
					value, ok := kv.Value.(*goast.FuncLit)
					if !ok {
						valid = false
						break
					}
					members[key.Name] = value
				}
				if !valid {
					continue
				}
				type use struct {
					member  string
					replace func(goast.Expr)
				}
				var uses []use
				references := 0
				match := func(expr goast.Expr, replace func(goast.Expr)) {
					selector, ok := expr.(*goast.SelectorExpr)
					if !ok {
						return
					}
					ref, ok := selector.X.(*goast.Ident)
					if !ok || ref.Name != name || members[selector.Sel.Name] == nil {
						return
					}
					uses = append(uses, use{selector.Sel.Name, replace})
				}
				for _, stmt := range list[i+1:] {
					goast.Inspect(stmt, func(node goast.Node) bool {
						if id, ok := node.(*goast.Ident); ok && id.Name == name {
							references++
						}
						if call, ok := node.(*goast.CallExpr); ok {
							match(call.Fun, func(x goast.Expr) { call.Fun = x })
							for j, arg := range call.Args {
								match(arg, func(x goast.Expr) { call.Args[j] = x })
							}
						}
						return true
					})
				}
				// Any whole-value use, assignment to a member, address, or shadowing use
				// keeps the bundle. We change only direct calls and function arguments.
				if len(uses) == 0 || references != len(uses) {
					continue
				}
				names := map[string]string{}
				var replacement []goast.Stmt
				for _, use := range uses {
					local := names[use.member]
					if local == "" {
						local = fmt.Sprintf("t_callablePart%d", g.tmp)
						g.tmp++
						names[use.member] = local
						value := members[use.member]
						replacement = append(replacement, varDeclStmt(local, value.Type, value), assignStmt("_", ident(local)))
					}
					use.replace(ident(local))
				}
				list = append(append(append([]goast.Stmt(nil), list[:i]...), replacement...), list[i+1:]...)
				set(list)
				changed = true
				i += len(replacement) - 1
			}
		}
		visit(func(list []goast.Stmt) { fn.Body.List = list }, fn.Body.List)
		if changed {
			newCopyEliding(fn).run()
		}
	}
}

func callbackAliasName(t goast.Expr) string {
	switch t := t.(type) {
	case *goast.Ident:
		return t.Name
	case *goast.IndexExpr:
		return callbackAliasName(t.X)
	case *goast.IndexListExpr:
		return callbackAliasName(t.X)
	}
	return ""
}
