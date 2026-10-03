package fixity

import (
	"fmt"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/diag"
)

// Resolve rewrites every ast.OpChain in the module into ast.BinOp trees, in
// place. It always leaves an OpChain-free AST, errors or not, so no later
// phase can meet an unresolved chain — including inside quoted code, which
// resolves here so a quote groups the way it would have written inline.
func (t Table) Resolve(m *ast.Module) []diag.Error {
	return t.ResolveDecls(m.Decls)
}

// ResolveDecls is Resolve over a bare declaration list, which the REPL uses
// for one prompt entry at a time.
func (t Table) ResolveDecls(decls []ast.Decl) []diag.Error {
	r := &resolver{t: t}
	for _, d := range decls {
		r.decl(d)
	}
	return r.errs
}

// ResolveExpr is Resolve for a single expression — the REPL's prompt, which
// is parsed on its own rather than as part of a module.
func (t Table) ResolveExpr(e ast.Expr) (ast.Expr, []diag.Error) {
	r := &resolver{t: t}
	return r.expr(e), r.errs
}

type resolver struct {
	t    Table
	errs []diag.Error
}

// chain groups one flat run using shunting-yard: operands are pushed as they
// come, and an operator reduces the pending ones whose fixity binds tighter
// before it is itself pushed.
//
// A stack rather than precedence climbing, because only a stack holds both
// operators at the moment they compete. Climbing recurses on the right
// operand with the enclosing operator already out of scope, so a same-level
// disagreement like `a ++ b <> c` under `infixr 5 (++)` and `infixl 5 (<>)`
// would silently nest one way instead of being reported.
func (r *resolver) chain(c *ast.OpChain) ast.Expr {
	for i, operand := range c.Operands {
		c.Operands[i] = r.expr(operand)
	}
	operands := []ast.Expr{c.Operands[0]}
	var ops []ast.OpRef
	reduce := func() {
		op := ops[len(ops)-1]
		ops = ops[:len(ops)-1]
		right := operands[len(operands)-1]
		left := operands[len(operands)-2]
		operands = operands[:len(operands)-2]
		operands = append(operands, &ast.BinOp{Op: op.Op, OpSpan: op.Sp, L: left, R: right})
	}
	for i, op := range c.Ops {
		for len(ops) > 0 && r.reduces(ops[len(ops)-1], op) {
			reduce()
		}
		ops = append(ops, op)
		operands = append(operands, c.Operands[i+1])
	}
	for len(ops) > 0 {
		reduce()
	}
	return operands[0]
}

// reduces reports whether the operator already on the stack binds the
// operand between them, rather than the incoming one taking it.
func (r *resolver) reduces(stacked, incoming ast.OpRef) bool {
	f, g := r.t.Lookup(stacked.Op), r.t.Lookup(incoming.Op)
	switch {
	case f.Prec > g.Prec:
		return true
	case f.Prec < g.Prec:
		return false
	case f.Assoc == ast.AssocLeft && g.Assoc == ast.AssocLeft:
		return true
	case f.Assoc == ast.AssocRight && g.Assoc == ast.AssocRight:
		return false
	}
	// Same precedence, and the two do not agree on how to associate — the
	// one ambiguous case, and the only grouping error this pass reports.
	// Recover by grouping to the left so the chain still collapses.
	r.ambiguous(stacked, f, incoming, g)
	return true
}

func (r *resolver) ambiguous(stacked ast.OpRef, f Fixity, incoming ast.OpRef, g Fixity) {
	if stacked.Op == incoming.Op {
		r.errs = append(r.errs, diag.Errorf(incoming.Sp, "OPERATOR GROUPING",
			"(%s) is declared `%s %d`, which does not associate, so I cannot tell\nhow `a %s b %s c` should group. Add parentheses to say what you mean.",
			stacked.Op, f.Assoc, f.Prec, stacked.Op, stacked.Op))
		return
	}
	r.errs = append(r.errs, diag.Errorf(incoming.Sp, "OPERATOR GROUPING",
		"(%s) is `%s %d` and (%s) is `%s %d`. Operators that share a precedence\nlevel must share an associativity to be mixed without parentheses.",
		stacked.Op, f.Assoc, f.Prec, incoming.Op, g.Assoc, g.Prec))
}

func (r *resolver) decl(d ast.Decl) {
	switch d := d.(type) {
	case *ast.ValueDecl:
		r.value(d)
	case *ast.PatternDecl:
		d.Body = r.expr(d.Body)
	case *ast.InstanceDecl:
		for _, m := range d.Methods {
			r.value(m)
		}
	case *ast.DeriverDecl:
		for _, m := range d.Methods {
			r.value(m)
		}
	case *ast.ClassDecl:
		for _, m := range d.Defaults {
			r.value(m)
		}
	case *ast.TypeDecl:
		d.VisitAttributes(func(group *ast.AttributeGroup) {
			for i, e := range group.Exprs {
				group.Exprs[i] = r.expr(e)
			}
		})
	case *ast.EffectDecl, *ast.FixityDecl:
		// Signatures and type declarations hold no expressions.
	default:
		panic(fmt.Sprintf("fixity: unhandled declaration %T", d))
	}
}

func (r *resolver) value(d *ast.ValueDecl) {
	if d.Body != nil {
		d.Body = r.expr(d.Body)
	}
	for i := range d.Equations {
		d.Equations[i].Body = r.expr(d.Equations[i].Body)
	}
}

// expr rewrites e and returns its replacement. Every case owning child
// expressions must reassign them: a chain child is replaced by a different
// node rather than mutated in place.
//
// The default panics so that adding an ast.Expr node without teaching this
// pass fails a test, rather than leaving a chain to surface in inference.
func (r *resolver) expr(e ast.Expr) ast.Expr {
	switch e := e.(type) {
	case nil:
		return nil
	case *ast.OpChain:
		return r.chain(e)
	case *ast.BinOp:
		e.L, e.R = r.expr(e.L), r.expr(e.R)
	case *ast.App:
		e.Fn, e.Arg = r.expr(e.Fn), r.expr(e.Arg)
	case *ast.Neg:
		e.Operand = r.expr(e.Operand)
	case *ast.If:
		e.Cond, e.Then, e.Else = r.expr(e.Cond), r.expr(e.Then), r.expr(e.Else)
	case *ast.Lambda:
		e.Body = r.expr(e.Body)
	case *ast.Block:
		for i := range e.Binds {
			e.Binds[i].Body = r.expr(e.Binds[i].Body)
			for j := range e.Binds[i].Equations {
				e.Binds[i].Equations[j].Body = r.expr(e.Binds[i].Equations[j].Body)
			}
		}
		for i := range e.Items {
			e.Items[i].Expr = r.expr(e.Items[i].Expr)
		}
		e.Result = r.expr(e.Result)
	case *ast.Case:
		e.Scrutinee = r.expr(e.Scrutinee)
		for i := range e.Branches {
			e.Branches[i].Body = r.expr(e.Branches[i].Body)
		}
	case *ast.Handle:
		e.Body = r.expr(e.Body)
		if e.State != nil {
			e.State.Initial = r.expr(e.State.Initial)
		}
		for i := range e.Clauses {
			e.Clauses[i].Body = r.expr(e.Clauses[i].Body)
			for j := range e.Clauses[i].Equations {
				e.Clauses[i].Equations[j].Body = r.expr(e.Clauses[i].Equations[j].Body)
			}
		}
		if e.Return != nil {
			e.Return.Body = r.expr(e.Return.Body)
			for i := range e.Return.Equations {
				e.Return.Equations[i].Body = r.expr(e.Return.Equations[i].Body)
			}
		}
	case *ast.RecordLit:
		for i := range e.Fields {
			e.Fields[i].Value = r.expr(e.Fields[i].Value)
		}
	case *ast.RecordGet:
		e.Record = r.expr(e.Record)
	case *ast.RecordUpdate:
		e.Record = r.expr(e.Record)
		for i := range e.Fields {
			e.Fields[i].Value = r.expr(e.Fields[i].Value)
		}
	case *ast.Quote:
		e.Body = r.expr(e.Body)
	case *ast.Splice:
		e.Operand = r.expr(e.Operand)
	case *ast.Resume:
		if e.NextState != nil {
			e.NextState = r.expr(e.NextState)
		}
	case *ast.Var, *ast.Ctor, *ast.IntLit, *ast.FloatLit, *ast.RegexLit, *ast.StringLit,
		*ast.CharLit, *ast.UnitLit, *ast.TypeOf:
		// Leaves, or heads whose arguments arrive as App wrappers.
	default:
		panic(fmt.Sprintf("fixity: unhandled expression %T", e))
	}
	return e
}
