package parser

import (
	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/source"
)

// scopedRowDecl records the declaration's scoped row variable from its `#s`
// occurrences. A runner binds at most one, and no other variable of its
// annotation may share the name; where the variable may occur is checked by
// inference against the complete declaration.
func (p *parser) scopedRowDecl(d *ast.ValueDecl) *ast.ValueDecl {
	if d.Ann == nil {
		return d
	}
	type plain struct {
		name, repair string
		sp           source.Span
	}
	var first source.Span
	var plains []plain
	var walk func(ast.TypeExpr)
	row := func(r *ast.EffRow) {
		if r == nil {
			return
		}
		for _, label := range r.Labels {
			for _, arg := range label.Args {
				walk(arg)
			}
		}
		switch {
		case r.Scoped && d.ScopedRow == "":
			d.ScopedRow, first = r.Tail, r.TailSp
		case r.Scoped && r.Tail != d.ScopedRow:
			p.errorAt(r.TailSp, "SCOPED CALLBACK", "A runner binds one scoped row variable, but this annotation writes both `#"+d.ScopedRow+"` and `#"+r.Tail+"`.")
		case !r.Scoped && r.Tail != "":
			labels := string(r.Sp.File.Content[r.Sp.Start:r.TailSp.Start])
			plains = append(plains, plain{r.Tail, labels + "#" + r.Tail + "}", r.TailSp})
		}
	}
	walk = func(t ast.TypeExpr) {
		switch t := t.(type) {
		case *ast.TVarName:
			plains = append(plains, plain{t.Name, "{#" + t.Name + "}", t.Sp})
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
	for _, pred := range d.Ann.Preds {
		walk(pred.Ty)
	}
	if d.ScopedRow == "" {
		return d
	}
	for _, use := range plains {
		if use.name == d.ScopedRow {
			p.errorAt(use.sp, "SCOPED CALLBACK", "`"+d.ScopedRow+"` is also the scoped row variable `#"+d.ScopedRow+"` of this runner, written at "+first.StartPos().String()+". Write the scoped variable `#"+d.ScopedRow+"` everywhere, as `"+use.repair+"` here, or give an ordinary variable a distinct name.")
		}
	}
	return d
}
