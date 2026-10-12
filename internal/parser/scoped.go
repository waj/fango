package parser

import "github.com/waj/fango/internal/ast"

// scopedRowDecl discovers the explicit row-extension spelling, not arbitrary
// variables local to a callback. Its position and ambient row are checked by
// inference against the complete declaration.
func (p *parser) scopedRowDecl(d *ast.ValueDecl) *ast.ValueDecl {
	if d.Ann == nil {
		return d
	}
	var walk func(ast.TypeExpr)
	row := func(r *ast.EffRow) {
		if r == nil {
			return
		}
		if r.ScopeBase != "" {
			if r.Tail == r.ScopeBase {
				p.errorAt(r.Span(), "SCOPED CALLBACK", "The scoped fragment and ambient row must have different names.")
			}
			if d.ScopedRow != "" && (d.ScopedRow != r.Tail || d.ScopedBase != r.ScopeBase) {
				p.errorAt(r.Span(), "SCOPED CALLBACK", "A runner may bind one scoped fragment, always with the same ambient row.")
			}
			d.ScopedRow, d.ScopedBase = r.Tail, r.ScopeBase
		}
		for _, label := range r.Labels {
			for _, arg := range label.Args {
				walk(arg)
			}
		}
	}
	walk = func(t ast.TypeExpr) {
		switch t := t.(type) {
		case *ast.TFunExpr:
			walk(t.Arg)
			row(t.Eff)
			walk(t.Ret)
		case *ast.TRow:
			row(t.Row)
		case *ast.TApp:
			for _, arg := range t.Args {
				walk(arg)
			}
		}
	}
	walk(d.Ann.Type)
	return d
}
