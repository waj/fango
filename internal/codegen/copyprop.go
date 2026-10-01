package codegen

import (
	goast "go/ast"
	gotoken "go/token"
	gotypes "go/types"
)

// elideCopies removes a temporary that only renames a local: `var X T = Y`
// becomes uses of Y when Y has the same declared type, is never addressed,
// is not reassigned after the declaration (a tail-loop jump excepted when no
// closure in X's scope captures X), and is not shadowed within X's scope.
// Go copies large structs through memory on each such declaration, and
// lowering produces one per field projection, pattern binder, and call
// result, so decoders paid several copies of their state per operation.
func (g *gen) elideCopies(file *goast.File) {
	for _, decl := range file.Decls {
		if fn, ok := decl.(*goast.FuncDecl); ok && fn.Body != nil {
			newCopyEliding(fn).run()
		}
	}
}

type localDecl struct {
	order int
	typ   string // "" when unknown (short variable declaration)
}

type localAssign struct {
	order    int
	tailJump bool
}

type copyEliding struct {
	fn        *goast.FuncDecl
	order     map[goast.Node]int
	decls     map[string][]localDecl
	assigns   map[string][]localAssign
	addressed map[string]bool
}

func newCopyEliding(fn *goast.FuncDecl) *copyEliding {
	c := &copyEliding{fn: fn, order: map[goast.Node]int{}, decls: map[string][]localDecl{}, assigns: map[string][]localAssign{}, addressed: map[string]bool{}}
	c.index()
	return c
}

func typeString(expr goast.Expr) string {
	if expr == nil {
		return ""
	}
	return gotypes.ExprString(expr)
}

// index numbers every node in syntactic order (generated nodes carry no
// positions) and records declarations, assignments, and address-of uses.
func (c *copyEliding) index() {
	n := 0
	var params func(*goast.FieldList)
	params = func(list *goast.FieldList) {
		if list == nil {
			return
		}
		for _, field := range list.List {
			for _, name := range field.Names {
				c.decls[name.Name] = append(c.decls[name.Name], localDecl{order: c.order[name], typ: typeString(field.Type)})
			}
		}
	}
	goast.Inspect(c.fn, func(node goast.Node) bool {
		if node == nil {
			return false
		}
		n++
		c.order[node] = n
		return true
	})
	params(c.fn.Type.Params)
	params(c.fn.Type.Results)
	c.walkLists(c.fn.Body, func(list []goast.Stmt, i int) {
		switch stmt := list[i].(type) {
		case *goast.DeclStmt:
			if decl, ok := stmt.Decl.(*goast.GenDecl); ok && decl.Tok == gotoken.VAR {
				for _, spec := range decl.Specs {
					if value, ok := spec.(*goast.ValueSpec); ok {
						for _, name := range value.Names {
							c.decls[name.Name] = append(c.decls[name.Name], localDecl{order: c.order[name], typ: typeString(value.Type)})
						}
					}
				}
			}
		case *goast.AssignStmt:
			tail := false
			if i+1 < len(list) {
				branch, ok := list[i+1].(*goast.BranchStmt)
				tail = ok && branch.Tok == gotoken.CONTINUE
			}
			for _, lhs := range stmt.Lhs {
				id, ok := lhs.(*goast.Ident)
				if !ok {
					continue
				}
				if stmt.Tok == gotoken.DEFINE {
					c.decls[id.Name] = append(c.decls[id.Name], localDecl{order: c.order[id]})
				} else {
					c.assigns[id.Name] = append(c.assigns[id.Name], localAssign{order: c.order[id], tailJump: tail})
				}
			}
		case *goast.IncDecStmt:
			if id, ok := stmt.X.(*goast.Ident); ok {
				c.assigns[id.Name] = append(c.assigns[id.Name], localAssign{order: c.order[id]})
			}
		case *goast.RangeStmt:
			for _, expr := range []goast.Expr{stmt.Key, stmt.Value} {
				if id, ok := expr.(*goast.Ident); ok {
					if stmt.Tok == gotoken.DEFINE {
						c.decls[id.Name] = append(c.decls[id.Name], localDecl{order: c.order[id]})
					} else {
						c.assigns[id.Name] = append(c.assigns[id.Name], localAssign{order: c.order[id]})
					}
				}
			}
		}
	})
	goast.Inspect(c.fn.Body, func(node goast.Node) bool {
		switch node := node.(type) {
		case *goast.FuncLit:
			params(node.Type.Params)
			params(node.Type.Results)
		case *goast.UnaryExpr:
			if id, ok := node.X.(*goast.Ident); ok && node.Op == gotoken.AND {
				c.addressed[id.Name] = true
			}
		}
		return true
	})
}

// walkLists visits every statement list in the body, nested lists included,
// calling visit with the list and the statement's index.
func (c *copyEliding) walkLists(body goast.Node, visit func([]goast.Stmt, int)) {
	goast.Inspect(body, func(node goast.Node) bool {
		var list []goast.Stmt
		switch node := node.(type) {
		case *goast.BlockStmt:
			list = node.List
		case *goast.CaseClause:
			list = node.Body
		case *goast.CommClause:
			list = node.Body
		default:
			return true
		}
		for i := range list {
			visit(list, i)
		}
		return true
	})
}

func (c *copyEliding) run() {
	var visitBlock func(set func([]goast.Stmt), list []goast.Stmt)
	visitBlock = func(set func([]goast.Stmt), list []goast.Stmt) {
		for i := 0; i < len(list); i++ {
			if x, y, ok := c.copyDecl(list[i]); ok && c.elidable(x, y, list[i+1:]) {
				rename(list[i+1:], x.Name, y.Name)
				list = append(list[:i], list[i+1:]...)
				set(list)
				i--
				continue
			}
			c.visitNested(list[i], visitBlock)
		}
	}
	visitBlock(func(l []goast.Stmt) { c.fn.Body.List = l }, c.fn.Body.List)
}

// visitNested descends into the statement lists owned by one statement,
// including those of function literals in its expressions.
func (c *copyEliding) visitNested(stmt goast.Stmt, visitBlock func(func([]goast.Stmt), []goast.Stmt)) {
	goast.Inspect(stmt, func(node goast.Node) bool {
		switch node := node.(type) {
		case *goast.BlockStmt:
			visitBlock(func(l []goast.Stmt) { node.List = l }, node.List)
			return false
		case *goast.CaseClause:
			visitBlock(func(l []goast.Stmt) { node.Body = l }, node.Body)
			return false
		case *goast.CommClause:
			visitBlock(func(l []goast.Stmt) { node.Body = l }, node.Body)
			return false
		}
		return true
	})
}

// copyDecl matches `var X T = Y` with a single name and a plain identifier.
func (c *copyEliding) copyDecl(stmt goast.Stmt) (*goast.Ident, *goast.Ident, bool) {
	decl, ok := stmt.(*goast.DeclStmt)
	if !ok {
		return nil, nil, false
	}
	gen, ok := decl.Decl.(*goast.GenDecl)
	if !ok || gen.Tok != gotoken.VAR || len(gen.Specs) != 1 {
		return nil, nil, false
	}
	spec, ok := gen.Specs[0].(*goast.ValueSpec)
	if !ok || len(spec.Names) != 1 || len(spec.Values) != 1 || spec.Type == nil {
		return nil, nil, false
	}
	y, ok := spec.Values[0].(*goast.Ident)
	if !ok || y.Name == "_" || y.Name == "nil" || y.Name == "true" || y.Name == "false" {
		return nil, nil, false
	}
	x := spec.Names[0]
	if x.Name == "_" || typeString(spec.Type) == "" {
		return nil, nil, false
	}
	if decls := c.decls[x.Name]; len(decls) != 1 || decls[0].typ != typeString(spec.Type) {
		return nil, nil, false
	}
	return x, y, true
}

func (c *copyEliding) elidable(x, y *goast.Ident, scope []goast.Stmt) bool {
	if c.addressed[x.Name] || c.addressed[y.Name] || len(c.assigns[x.Name]) != 0 {
		return false
	}
	at := c.order[x]
	typ := c.decls[x.Name][0].typ
	visible := 0
	for _, decl := range c.decls[y.Name] {
		if decl.order < at {
			if decl.typ != typ {
				return false
			}
			visible++
		}
	}
	if visible == 0 {
		return false
	}
	tailJumps := false
	for _, assign := range c.assigns[y.Name] {
		if assign.order > at {
			if !assign.tailJump {
				return false
			}
			tailJumps = true
		}
	}
	shadowed, captured := false, false
	for _, stmt := range scope {
		goast.Inspect(stmt, func(node goast.Node) bool {
			switch node := node.(type) {
			case *goast.Ident:
				if node.Name == y.Name && c.order[node] > at {
					for _, decl := range c.decls[y.Name] {
						if decl.order == c.order[node] {
							shadowed = true
						}
					}
				}
			case *goast.FuncLit:
				for _, name := range node.Type.Params.List {
					for _, id := range name.Names {
						if id.Name == y.Name {
							shadowed = true
						}
					}
				}
				if tailJumps && references(node, x.Name) {
					captured = true
				}
			}
			return true
		})
	}
	return !shadowed && !captured
}

func references(node goast.Node, name string) bool {
	found := false
	goast.Inspect(node, func(n goast.Node) bool {
		if id, ok := n.(*goast.Ident); ok && id.Name == name {
			found = true
		}
		return !found
	})
	return found
}

func rename(scope []goast.Stmt, from, to string) {
	for _, stmt := range scope {
		goast.Inspect(stmt, func(n goast.Node) bool {
			if id, ok := n.(*goast.Ident); ok && id.Name == from {
				id.Name = to
			}
			return true
		})
	}
}
