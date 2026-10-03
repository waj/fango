package infer

import (
	"fmt"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/types"
)

// operationSignature checks a clause group's `op : Type` line against the
// operation's declaration and returns the effect arguments it selects, one
// per parameter of the declaring effect. A signature is the declared type
// specialized: it may fix the effect's parameters, and nothing else. The
// operation's own type variables stay variables, under any name, and every
// effect parameter must come out as a closed type, so the signature names
// one application. The effect row is optional; when written it must be the
// declaring effect's closed row on the innermost arrow, which is how a
// parameter absent from the inputs and result is selected. A nil result
// means a diagnostic was reported.
func (g *generator) operationSignature(cl *ast.HandleClause, op *types.EffectOp) []types.Type {
	ck := g.ck
	scope := ck.NewAnnScope()
	sigTy, errs := ck.ResolveTypeExpr(cl.Signature.Type, scope)
	if len(errs) > 0 {
		g.errs = append(g.errs, errs...)
		return nil
	}
	// One printer names the declaration, its variables, and the signature's
	// types consistently across a diagnostic.
	pr := types.NewPrinter()
	declared := pr.Scheme(op.Scheme)

	// The declared operation type with effect parameters as metas to solve,
	// its local variables as fresh skolems, and its row variables as metas
	// that the match never binds.
	m := make(map[int]types.Type, len(op.Scheme.Vars))
	params := make([]*types.TVar, len(op.Owner.Params))
	for i, p := range op.Owner.Params {
		params[i] = ck.Sup.FreshVar(types.General)
		m[p.ID] = params[i]
	}
	match := &signatureMatch{pr: pr, sub: Subst{}, locals: map[int]string{}, pairs: map[int]int{}, paired: map[int]bool{}}
	for _, v := range op.LocalVars {
		fresh := ck.Sup.FreshRigid(v.Kind)
		m[v.ID] = fresh
		match.locals[fresh.ID] = fmt.Sprintf("the operation's own type variable `%s`", pr.Type(v))
	}
	for _, v := range op.Scheme.Vars {
		if _, ok := m[v.ID]; !ok {
			m[v.ID] = ck.Sup.FreshVar(v.Kind)
		}
	}
	decl := types.SubstRigid(op.Scheme.Body, m)
	if reason := match.match(sigTy, decl); reason != "" {
		g.errs = append(g.errs, diag.Errorf(cl.SigSpan, "OPERATION SIGNATURE MISMATCH",
			"This signature does not specialize `%s`, declared as\n\n    %s : %s\n\n%s", op.Name, op.Name, declared, reason))
		return nil
	}

	// Rows: only the innermost arrow may carry one, and it must be exactly
	// the declaring effect's application.
	arrow, _ := cl.Signature.Type.(*ast.TFunExpr)
	for i := 0; arrow != nil && i < op.Arity; i++ {
		if arrow.Eff != nil {
			if i != op.Arity-1 {
				g.errs = append(g.errs, diag.Errorf(arrow.Eff.Sp, "OPERATION SIGNATURE MISMATCH",
					"Only the innermost arrow of `%s` carries its effect row.", op.Name))
				return nil
			}
			if arrow.Eff.Tail != "" || len(arrow.Eff.Labels) != 1 || ck.Effects[arrow.Eff.Labels[0].Name] != op.Owner {
				g.errs = append(g.errs, diag.Errorf(arrow.Eff.Sp, "OPERATION SIGNATURE MISMATCH",
					"The row of an operation signature names only the declaring effect: `{%s …}` for `%s`.\nIt identifies the handled application, not the clause's effects.", types.SurfaceName(op.Owner.Name), op.Name))
				return nil
			}
			row, rowErrs := ck.resolveEffRow(arrow.Eff, scope)
			if len(rowErrs) > 0 {
				g.errs = append(g.errs, rowErrs...)
				return nil
			}
			for j, a := range row.Labels[0].Args {
				if j >= len(params) {
					break
				}
				if bound, ok := match.sub[params[j].ID]; ok {
					if !types.Equal(match.sub.Apply(bound), a) {
						g.errs = append(g.errs, diag.Errorf(arrow.Eff.Sp, "OPERATION SIGNATURE MISMATCH",
							"The row says `%s` is `%s`, but the rest of the signature says `%s`.", pr.Type(op.Owner.Params[j]), pr.Type(a), pr.Type(match.sub.Apply(bound))))
						return nil
					}
				} else {
					match.sub[params[j].ID] = a
				}
			}
		}
		arrow, _ = arrow.Ret.(*ast.TFunExpr)
	}

	args := make([]types.Type, len(params))
	for i, p := range params {
		bound, ok := match.sub[p.ID]
		if !ok || !closedType(match.sub.Apply(bound)) {
			hint := "Name a closed type for it"
			if !ok {
				hint += ", in the signature's row if the inputs and result never mention it"
			}
			g.errs = append(g.errs, diag.Errorf(cl.SigSpan, "INCOMPLETE OPERATION SIGNATURE",
				"This signature leaves the effect parameter `%s` of `%s` open, so it does not select one application.\n`%s` is declared as\n\n    %s : %s\n\n%s.", pr.Type(op.Owner.Params[i]), types.SurfaceName(op.Owner.Name), op.Name, op.Name, declared, hint))
			return nil
		}
		args[i] = match.sub.Apply(bound)
	}
	return args
}

// signatureMatch matches a resolved signature against the declared operation
// type. Effect-parameter metas in the declaration bind to signature types;
// the operation's local skolems pair one-to-one with signature variables.
// Arrow rows are ignored here and checked from the surface syntax instead,
// since a plain `->` cannot be told from an empty row once resolved.
type signatureMatch struct {
	pr     *types.Printer
	sub    Subst
	locals map[int]string // declaration skolem → how to name it in a diagnostic
	pairs  map[int]int    // signature variable → declaration skolem
	paired map[int]bool   // declaration skolems already taken
}

// match returns "" on success or the reason the signature does not fit.
func (sm *signatureMatch) match(sig, decl types.Type) string {
	decl = sm.sub.walk(decl)
	switch d := decl.(type) {
	case *types.TVar:
		if !d.Rigid {
			sm.sub[d.ID] = sig
			return ""
		}
		v, ok := sig.(*types.TVar)
		if !ok {
			return fmt.Sprintf("It instantiates %s with `%s`; only the effect's parameters may be fixed.", sm.locals[d.ID], sm.pr.Type(sig))
		}
		if prev, seen := sm.pairs[v.ID]; seen {
			if prev != d.ID {
				return fmt.Sprintf("It uses `%s` for two different type variables of the operation.", sm.pr.Type(v))
			}
			return ""
		}
		if sm.paired[d.ID] {
			return fmt.Sprintf("It names %s twice, under different variables.", sm.locals[d.ID])
		}
		sm.pairs[v.ID], sm.paired[d.ID] = d.ID, true
		return ""
	case *types.TCon:
		c, ok := sig.(*types.TCon)
		if !ok || c.Unique != d.Unique || len(c.Args) != len(d.Args) {
			return fmt.Sprintf("Where the declaration has `%s`, the signature has `%s`.", sm.pr.Type(sm.sub.Apply(d)), sm.pr.Type(sig))
		}
		for i := range c.Args {
			if reason := sm.match(c.Args[i], d.Args[i]); reason != "" {
				return reason
			}
		}
		return ""
	case *types.TFun:
		f, ok := sig.(*types.TFun)
		if !ok {
			return fmt.Sprintf("Where the declaration has `%s`, the signature has `%s`.", sm.pr.Type(sm.sub.Apply(d)), sm.pr.Type(sig))
		}
		if reason := sm.match(f.Arg, d.Arg); reason != "" {
			return reason
		}
		return sm.match(f.Ret, d.Ret)
	}
	return fmt.Sprintf("Where the declaration has `%s`, the signature has `%s`.", sm.pr.Type(decl), sm.pr.Type(sig))
}

// closedType reports whether t mentions no type or row variable at all.
func closedType(t types.Type) bool {
	switch t := t.(type) {
	case *types.TVar:
		return false
	case *types.TCon:
		for _, a := range t.Args {
			if !closedType(a) {
				return false
			}
		}
		return true
	case *types.TFun:
		if t.Eff.Tail != nil {
			return false
		}
		for _, l := range t.Eff.Labels {
			for _, a := range l.Args {
				if !closedType(a) {
					return false
				}
			}
		}
		return closedType(t.Arg) && closedType(t.Ret)
	}
	return true
}
