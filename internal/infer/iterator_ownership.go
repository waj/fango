package infer

import (
	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/types"
)

// iteratorOwnership checks the source-facing half of the initial E8 cursor
// discipline. The Core linter repeats the proof after elaboration; doing it
// here lets an invalid source use point at the exact cursor occurrence.
func (g *generator) iteratorOwnership(e *ast.App) []diag.Error {
	head, ok := appHead(e).(*ast.Var)
	if !ok || g.canonicalValueName(head.Name) != types.GeneratorWithIteratorName {
		return nil
	}
	// Canonical spelling is not sufficient identity by itself: activate the
	// rule only when the bundled declaration installed the compiler intrinsic.
	if _, declared := g.ck.Intrinsics[types.GeneratorWithIteratorName]; !declared {
		return nil
	}
	args := appArgs(e)
	if len(args) != 2 {
		return nil
	}
	consumer, ok := args[1].(*ast.Lambda)
	if !ok || len(consumer.Params) != 1 {
		return []diag.Error{diag.Errorf(args[1].Span(), "ITERATOR CONSUMER",
			"`Generator.withIterator` requires a lexical one-parameter lambda as its consumer.")}
	}
	cursor, ok := consumer.Params[0].(*ast.PVar)
	if !ok {
		return []diag.Error{diag.Errorf(consumer.Params[0].Span(), "ITERATOR CONSUMER",
			"The iterator consumer parameter must be a name used by one consuming combinator.")}
	}

	uses, consumed := g.iteratorCursorUses(consumer.Body, cursor.Name)
	allowed := make(map[*ast.Var]bool, len(consumed))
	for _, use := range consumed {
		allowed[use] = true
	}
	var errs []diag.Error
	for _, use := range uses {
		if !allowed[use] {
			errs = append(errs, diag.Errorf(use.Sp, "ITERATOR CURSOR ESCAPES",
				"Iterator cursor `%s` may only be passed directly as the final argument of a consuming `Iterator` combinator.", cursor.Name))
			break
		}
	}
	if len(consumed) > 1 {
		errs = append(errs, diag.Errorf(consumed[1].Sp, "ITERATOR CURSOR REUSED",
			"Iterator cursor `%s` is consumed more than once; a cursor has exactly one owner.", cursor.Name))
	}
	return errs
}

func (g *generator) canonicalValueName(name string) string {
	if canonical := g.ck.Aliases[name]; canonical != "" {
		return canonical
	}
	return name
}

// iteratorCursorUses returns every occurrence of cursor and the subset that
// is a direct final argument to a recognized terminal consumer. Fango forbids
// shadowing, so the resolved spelling identifies this lambda parameter
// throughout its body.
func (g *generator) iteratorCursorUses(e ast.Expr, cursor string) (uses, consumed []*ast.Var) {
	var walk func(ast.Expr)
	walk = func(e ast.Expr) {
		if e == nil {
			return
		}
		if app, ok := e.(*ast.App); ok {
			if head, ok := appHead(app).(*ast.Var); ok && types.IteratorConsumer(g.canonicalValueName(head.Name)) {
				args := appArgs(app)
				if len(args) > 0 {
					if use, ok := args[len(args)-1].(*ast.Var); ok && use.Name == cursor {
						consumed = append(consumed, use)
					}
				}
			}
		}
		switch x := e.(type) {
		case *ast.Var:
			if x.Name == cursor {
				uses = append(uses, x)
			}
		case *ast.RecordLit:
			for _, field := range x.Fields {
				walk(field.Value)
			}
		case *ast.RecordGet:
			walk(x.Record)
		case *ast.RecordUpdate:
			walk(x.Record)
			for _, field := range x.Fields {
				walk(field.Value)
			}
		case *ast.App:
			walk(x.Fn)
			walk(x.Arg)
		case *ast.Neg:
			walk(x.Operand)
		case *ast.If:
			walk(x.Cond)
			walk(x.Then)
			walk(x.Else)
		case *ast.BinOp:
			walk(x.L)
			walk(x.R)
		case *ast.OpChain:
			for _, operand := range x.Operands {
				walk(operand)
			}
		case *ast.Block:
			for i := range x.Binds {
				bind := &x.Binds[i]
				if len(bind.Equations) == 0 {
					walk(bind.Body)
				} else {
					for _, eq := range bind.Equations {
						walk(eq.Body)
					}
				}
			}
			for _, item := range x.Items {
				walk(item.Expr)
			}
			walk(x.Result)
		case *ast.Lambda:
			walk(x.Body)
		case *ast.Case:
			walk(x.Scrutinee)
			for _, branch := range x.Branches {
				walk(branch.Body)
			}
		case *ast.Handle:
			walk(x.Body)
			if x.State != nil {
				walk(x.State.Initial)
			}
			for i := range x.Clauses {
				clause := &x.Clauses[i]
				if len(clause.Equations) == 0 {
					walk(clause.Body)
				} else {
					for _, eq := range clause.Equations {
						walk(eq.Body)
					}
				}
			}
			if x.Return != nil {
				if len(x.Return.Equations) == 0 {
					walk(x.Return.Body)
				} else {
					for _, eq := range x.Return.Equations {
						walk(eq.Body)
					}
				}
			}
		case *ast.Resume:
			walk(x.NextState)
		case *ast.Quote:
			walk(x.Body)
		case *ast.Splice:
			walk(x.Operand)
		case *ast.IntLit, *ast.FloatLit, *ast.StringLit, *ast.CharLit,
			*ast.UnitLit, *ast.Ctor, *ast.TypeOf, *ast.MetaValue:
			// Leaves.
		}
	}
	walk(e)
	return uses, consumed
}
