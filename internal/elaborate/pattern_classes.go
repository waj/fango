package elaborate

import (
	"fmt"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

func (el *elab) overloadedPattern(p ast.Pattern) bool {
	switch p := p.(type) {
	case *ast.PInt:
		t := el.apply(el.ck.PatTypes[p])
		return el.unique(t) != el.ck.B.Int.Unique && el.unique(t) != el.ck.B.Float.Unique
	case *ast.PPin:
		return true
	case *ast.PCtor:
		for _, a := range p.Args {
			if el.overloadedPattern(a) {
				return true
			}
		}
	case *ast.PRecord:
		for _, f := range p.Fields {
			if el.overloadedPattern(f.Pattern) {
				return true
			}
		}
	}
	return false
}

func (m *matcher) ordered(patterns [][]ast.Pattern, occs []occurrence, i int) core.Tree {
	if i == len(patterns) {
		return &core.Unreachable{}
	}
	tys := make([]types.Type, len(occs))
	for j := range occs {
		tys[j] = occs[j].ty
	}
	if !m.useful(tys, patterns[:i], patterns[i]) {
		m.used[i] = false
		return m.ordered(patterns, occs, i+1)
	}
	m.used[i] = true
	success := core.Tree(&core.Leaf{Body: m.bodies[i]})
	failure := core.Tree(&core.Unreachable{})
	allIrrefutable := true
	for _, p := range patterns[i] {
		allIrrefutable = allIrrefutable && irrefutable(p)
	}
	if !allIrrefutable {
		failure = m.ordered(patterns, occs, i+1)
	}
	for j := len(patterns[i]) - 1; j >= 0; j-- {
		success = m.orderedPattern(patterns[i][j], occs[j], success, failure)
	}
	return success
}

func (m *matcher) orderedPattern(p ast.Pattern, occ occurrence, success, failure core.Tree) core.Tree {
	el := m.el
	ref := &core.VarRef{Name: occ.name, Ty: occ.ty, Local: true}
	switch p := p.(type) {
	case *ast.PWildcard, *ast.PUnit:
		return success
	case *ast.PVar:
		if !core.TreeMentions(success, p.Name) {
			return success
		}
		// A one-field irrefutable binding is represented as a Case with a
		// leaf, so the success tree can retain its constructor occurrences.
		ty := m.bodies[0].Type()
		body := &core.Case{Scrut: ref, Bind: p.Name, Tree: success, Ty: ty}
		return &core.Leaf{Body: body}
	case *ast.PCtor:
		ctor := el.ck.Ctors[p.Name]
		adt := el.ck.ADTs[ctor.Result.Unique]
		fts := instFields(adt, ctor, occ.ty)
		binds := make([]string, len(p.Args))
		for i := range binds {
			binds[i] = fmt.Sprintf("_pattern%d", el.tmp)
			el.tmp++
		}
		for i := len(p.Args) - 1; i >= 0; i-- {
			success = m.orderedPattern(p.Args[i], occurrence{name: binds[i], ty: fts[i]}, success, failure)
		}
		for i, n := range binds {
			if !core.TreeMentions(success, n) {
				binds[i] = ""
			}
		}
		var fallback core.Tree = failure
		if len(adt.Ctors) == 1 {
			fallback = nil
		}
		return &core.SwitchCtor{Scrut: occ.name, ADT: adt, Cases: []core.CtorCase{{Ctor: ctor, Binds: binds, Tree: success}}, Default: fallback}
	case *ast.PInt:
		fromTy := &types.TFun{Arg: el.ck.B.Int, Ret: occ.ty}
		lit := el.valueApp(el.methodValue(el.ck.Methods["Basics.fromInt"], fromTy), &core.IntLit{Val: p.Value, Ty: el.ck.B.Int})
		eqTy := &types.TFun{Arg: occ.ty, Ret: &types.TFun{Arg: occ.ty, Ret: el.ck.B.Bool}}
		cond := el.valueApp(el.valueApp(el.methodValue(el.ck.Methods["Basics.=="], eqTy), ref), lit)
		return &core.Guard{Cond: cond, Then: success, Else: failure}
	case *ast.PPin:
		pinned := el.expr(el.ck.PinExprs[p])
		eqTy := &types.TFun{Arg: occ.ty, Ret: &types.TFun{Arg: occ.ty, Ret: el.ck.B.Bool}}
		cond := el.valueApp(el.valueApp(el.methodValue(el.ck.Methods["Basics.=="], eqTy), ref), pinned)
		return &core.Guard{Cond: cond, Then: success, Else: failure}
	case *ast.PFloat, *ast.PString, *ast.PChar:
		return &core.SwitchLit{Scrut: occ.name, Cases: []core.LitCase{{Lit: m.litExpr(p, occ.ty), Tree: success}}, Default: failure}
	}
	panic("unknown ordered pattern")
}

// useful tests coverage by the entire preceding matrix, not by individual
// rows. Numeric equality is opaque: distinct overloaded literals may overlap,
// but only identical literal tests can prove redundancy statically.
func (m *matcher) useful(tys []types.Type, matrix [][]ast.Pattern, q []ast.Pattern) bool {
	if len(matrix) == 0 {
		return true
	}
	if len(q) == 0 {
		return false
	}
	if p, ok := q[0].(*ast.PCtor); ok {
		ctor := m.el.ck.Ctors[p.Name]
		adt := m.el.ck.ADTs[ctor.Result.Unique]
		fts := instFields(adt, ctor, tys[0])
		return m.useful(append(fts, tys[1:]...), specializeWitness(ctor, matrix), splicePats(q, 0, p.Args))
	}
	if !irrefutable(q[0]) {
		var selected [][]ast.Pattern
		for _, row := range matrix {
			if irrefutable(row[0]) || ast.DumpPattern(row[0]) == ast.DumpPattern(q[0]) {
				selected = append(selected, row[1:])
			}
		}
		return m.useful(tys[1:], selected, q[1:])
	}
	heads := map[string]bool{}
	for _, row := range matrix {
		if p, ok := row[0].(*ast.PCtor); ok {
			heads[p.Name] = true
		}
	}
	// Expand only a complete constructor signature. Expanding a wildcard
	// column unconditionally would recurse forever on recursive ADTs.
	if adt := m.adtOf(tys[0]); adt != nil && len(heads) > 0 && len(heads) == len(adt.Ctors) {
		for _, ctor := range adt.Ctors {
			fts := instFields(adt, ctor, tys[0])
			if m.useful(append(fts, tys[1:]...), specializeWitness(ctor, matrix), splicePats(q, 0, wildcards(len(fts)))) {
				return true
			}
		}
		return false
	}
	var defaults [][]ast.Pattern
	for _, row := range matrix {
		if irrefutable(row[0]) {
			defaults = append(defaults, row[1:])
		}
	}
	return m.useful(tys[1:], defaults, q[1:])
}
