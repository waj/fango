package elaborate

import (
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

// Hidden package factories and consumers may forward an invocation row. They
// cannot install handlers or manufacture evidence: copying them substitutes the
// caller's checked row, rather than resolving effects at the factory site.
func hiddenInlineBody(e core.Expr, row types.CaptureVar) bool {
	all := func(es []core.Expr) bool {
		for _, e := range es {
			if !hiddenInlineBody(e, row) {
				return false
			}
		}
		return true
	}
	switch e := e.(type) {
	case *core.IntLit, *core.FloatLit, *core.RegexLit, *core.StringLit, *core.CharLit, *core.BoolLit, *core.UnitLit, *core.VarRef:
		return true
	case *core.Neg:
		return hiddenInlineBody(e.Operand, row)
	case *core.NativeCall:
		return all(e.Args)
	case *core.If:
		return all([]core.Expr{e.Cond, e.Then, e.Else})
	case *core.Seq:
		return all([]core.Expr{e.First, e.Then})
	case *core.Let:
		return !e.Rec && all([]core.Expr{e.Rhs, e.Body})
	case *core.Case:
		return hiddenInlineBody(e.Scrut, row) && hiddenInlineTree(e.Tree, row)
	case *core.Lambda:
		if len(e.EffectParams) != 0 || len(e.RowEffects) != 0 {
			return false
		}
		if core.ArrowOpenRow(e.Ty, 1) {
			row = e.RowParam
			if row == 0 {
				row = pendingRow
			}
		}
		return hiddenInlineBody(e.Body, row)
	case *core.App:
		if (e.CalleeKind != core.Worker && e.CalleeKind != core.Ctor && e.CalleeKind != core.Value) ||
			len(e.EvidenceArgs) != 0 || e.Control.Transport != types.Direct {
			return false
		}
		if e.Row != nil && (len(e.Row.Effects) != 0 || e.Row.From != 0 && e.Row.From != row) {
			return false
		}
		return hiddenInlineBody(e.Callee, row) && all(e.Args)
	}
	return false
}

func hiddenInlineTree(t core.Tree, row types.CaptureVar) bool {
	switch t := t.(type) {
	case nil, *core.Unreachable:
		return true
	case *core.Leaf:
		return hiddenInlineBody(t.Body, row)
	case *core.Guard:
		return hiddenInlineBody(t.Cond, row) && hiddenInlineTree(t.Then, row) && hiddenInlineTree(t.Else, row)
	case *core.SwitchCtor:
		for _, c := range t.Cases {
			if !hiddenInlineTree(c.Tree, row) {
				return false
			}
		}
		return hiddenInlineTree(t.Default, row)
	case *core.SwitchLit:
		for _, c := range t.Cases {
			if !hiddenInlineTree(c.Tree, row) {
				return false
			}
		}
		return hiddenInlineTree(t.Default, row)
	}
	return false
}

func (c *copier) row(row *core.RowArgument) *core.RowArgument {
	if row == nil {
		return nil
	}
	n := &core.RowArgument{From: row.From}
	if actual, ok := c.rows[row.From]; ok {
		n.From = actual.From
		// Actual arguments are already in the caller's type scope.
		n.Effects = append(n.Effects, actual.Effects...)
	}
	for _, effect := range row.Effects {
		effect.Args = c.types(effect.Args)
		n.Effects = append(n.Effects, effect)
	}
	return n
}

func hiddenBranchBody(e core.Expr) bool {
	rows := core.FreeRows(e)
	if len(rows) > 1 {
		return false
	}
	var row types.CaptureVar
	for v := range rows {
		row = v
	}
	return hiddenInlineBody(e, row)
}
