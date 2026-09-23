package machine

import (
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

// inlineWrapper expands only checked, statically resolved calls. Arguments
// become strict lets even when unused. No semantic Core node is mutated and
// no owner/effect binder can be introduced by the template whitelist.
func (b *builder) inlineWrapper(a *core.App) core.Expr {
	if b.inlineDepth >= 4 || b.inlineNodes >= 128 || a.CalleeKind != core.Worker {
		return nil
	}
	ref, ok := a.Callee.(*core.VarRef)
	if !ok {
		return nil
	}
	d := b.templates[ref.Name]
	if d == nil || len(d.Params) != len(a.Args) || len(d.TyParams) != len(a.TyArgs) || len(a.EvidenceArgs) != 0 {
		return nil
	}
	n := core.WrapperSize(d, d.InlineBody)
	if n == 0 || b.inlineNodes+n > 128 {
		return nil
	}
	sub := map[int]types.Type{}
	for i, p := range d.TyParams {
		sub[p.ID] = a.TyArgs[i]
	}
	names := map[string]string{}
	rename := func(name string) string {
		if name == "" {
			return ""
		}
		if s, ok := names[name]; ok {
			return s
		}
		s := b.fresh("inline")
		names[name] = s
		return s
	}
	for _, p := range d.Params {
		rename(p)
	}
	// Declare every lexical name before rewriting references or decision trees.
	var collectTree func(core.Tree)
	collectTree = func(t core.Tree) {
		switch t := t.(type) {
		case *core.SwitchCtor:
			rename(t.Scrut)
			for _, c := range t.Cases {
				for _, v := range c.Binds {
					rename(v)
				}
				collectTree(c.Tree)
			}
			collectTree(t.Default)
		case *core.SwitchLit:
			rename(t.Scrut)
			for _, c := range t.Cases {
				collectTree(c.Tree)
			}
			collectTree(t.Default)
		case *core.Guard:
			collectTree(t.Then)
			collectTree(t.Else)
		}
	}
	core.Inspect(d.InlineBody, func(e core.Expr) {
		switch e := e.(type) {
		case *core.Let:
			rename(e.Name)
		case *core.Case:
			rename(e.Bind)
			collectTree(e.Tree)
		}
	})
	var tree func(core.Tree)
	tree = func(t core.Tree) {
		switch t := t.(type) {
		case *core.SwitchCtor:
			t.Scrut = rename(t.Scrut)
			for i := range t.Cases {
				for j, v := range t.Cases[i].Binds {
					t.Cases[i].Binds[j] = rename(v)
				}
				tree(t.Cases[i].Tree)
			}
			tree(t.Default)
		case *core.SwitchLit:
			t.Scrut = rename(t.Scrut)
			for _, c := range t.Cases {
				tree(c.Tree)
			}
			tree(t.Default)
		case *core.Guard:
			tree(t.Then)
			tree(t.Else)
		}
	}
	valid := true
	body := core.Rewrite(d.InlineBody, func(t types.Type) types.Type { return types.SubstRigid(t, sub) }, func(e core.Expr) core.Expr {
		switch e := e.(type) {
		case *core.VarRef:
			if name, bound := names[e.Name]; bound {
				e.Name, e.Local = name, true
			}
		case *core.Let:
			e.Name = rename(e.Name)
		case *core.Case:
			e.Bind = rename(e.Bind)
			tree(e.Tree)
		}
		if row := core.ExpressionRow(e); row != nil && row.From != 0 {
			if row.From != d.RowParam || len(row.Effects) != 0 || a.Row == nil {
				valid = false
			} else {
				row.From = a.Row.From
				row.Effects = append([]core.EffectInstance(nil), a.Row.Effects...)
			}
		}
		return e
	})
	if !valid {
		return nil
	}
	for i := len(a.Args) - 1; i >= 0; i-- {
		body = &core.Let{Name: names[d.Params[i]], Rhs: a.Args[i], Body: body, Ty: a.Ty}
	}
	b.inlineNodes += n
	return body
}
