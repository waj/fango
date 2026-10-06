package infer

import (
	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/source"
)

// Canonical names are introduced after source resolution. The nested calls
// sequence each append/render before evaluating the next hole under ordinary
// strict application semantics, without generated local names or Core nodes.
func interpolationExpr(e *ast.StringInterpolation) ast.Expr {
	call := func(name string, span source.Span, args ...ast.Expr) ast.Expr {
		var result ast.Expr = &ast.Var{Name: name, Sp: span}
		for _, arg := range args {
			result = &ast.App{Fn: result, Arg: arg}
		}
		return result
	}
	var built ast.Expr = &ast.Var{Name: "Text.Builder.empty", Sp: e.Sp}
	for i, text := range e.Segments {
		if text != "" {
			span := e.SegmentSpans[i]
			built = call("Text.Builder.append", span, &ast.StringLit{Value: text, Sp: span}, built)
		}
		if i < len(e.Exprs) {
			hole := e.Exprs[i]
			built = call("Basics.displayTo", hole.Span(), built, hole)
		}
	}
	return call("Text.Builder.toString", e.Sp, built)
}
