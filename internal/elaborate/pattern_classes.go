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
		t := el.ck.Sub.Apply(el.ck.PatTypes[p])
		return el.unique(t) != el.ck.B.Int.Unique && el.unique(t) != el.ck.B.Float.Unique
	case *ast.PCtor:
		for _, a := range p.Args {
			if el.overloadedPattern(a) {
				return true
			}
		}
	}
	return false
}

func (m *matcher) ordered(e *ast.Case, occ occurrence, i int) core.Tree {
	if i == len(e.Branches) {
		return &core.Unreachable{}
	}
	m.used[i] = true
	p := e.Branches[i].Pattern
	for j := 0; j < i; j++ {
		if patternSubsumes(e.Branches[j].Pattern, p) {
			m.used[i] = false
			return m.ordered(e, occ, i+1)
		}
	}
	if irrefutable(p) {
		return m.orderedPattern(p, occ, &core.Leaf{Body: m.bodies[i]}, &core.Unreachable{})
	}
	failure := m.ordered(e, occ, i+1)
	return m.orderedPattern(p, occ, &core.Leaf{Body: m.bodies[i]}, failure)
}

func (m *matcher) orderedPattern(p ast.Pattern, occ occurrence, success, failure core.Tree) core.Tree {
	el := m.el
	ref := &core.VarRef{Name: occ.name, Ty: occ.ty, Local: true}
	switch p := p.(type) {
	case *ast.PWildcard:
		return success
	case *ast.PVar:
		if !core.TreeMentions(success, p.Name) { return success }
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
		if len(adt.Ctors) == 1 { fallback = nil }
		return &core.SwitchCtor{Scrut: occ.name, ADT: adt, Cases: []core.CtorCase{{Ctor: ctor, Binds: binds, Tree: success}}, Default: fallback}
	case *ast.PInt:
		fromTy := &types.TFun{Arg: el.ck.B.Int, Ret: occ.ty}
		lit := el.valueApp(el.methodValue(el.ck.Methods["Basics.fromInt"], fromTy), &core.IntLit{Val: p.Value, Ty: el.ck.B.Int})
		eqTy := &types.TFun{Arg: occ.ty, Ret: &types.TFun{Arg: occ.ty, Ret: el.ck.B.Bool}}
		cond := el.valueApp(el.valueApp(el.methodValue(el.ck.Methods["Basics.eq"], eqTy), ref), lit)
		return &core.Guard{Cond: cond, Then: success, Else: failure}
	case *ast.PFloat, *ast.PString:
		return &core.SwitchLit{Scrut: occ.name, Cases: []core.LitCase{{Lit: m.litExpr(p, occ.ty), Tree: success}}, Default: failure}
	}
	panic("unknown ordered pattern")
}

func patternSubsumes(a, b ast.Pattern) bool {
	if irrefutable(a) { return true }
	switch a := a.(type) {
	case *ast.PInt:
		b, ok := b.(*ast.PInt); return ok && a.Value == b.Value
	case *ast.PFloat:
		b, ok := b.(*ast.PFloat); return ok && a.Value == b.Value
	case *ast.PString:
		b, ok := b.(*ast.PString); return ok && a.Value == b.Value
	case *ast.PCtor:
		b, ok := b.(*ast.PCtor)
		if !ok || a.Name != b.Name || len(a.Args) != len(b.Args) { return false }
		for i := range a.Args { if !patternSubsumes(a.Args[i], b.Args[i]) { return false } }
		return true
	}
	return false
}
