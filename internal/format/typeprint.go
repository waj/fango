package format

import (
	"strings"

	"github.com/waj/fango/internal/ast"
)

// Types are rendered inline. The surface type grammar has no layout of its own
// — an arrow chain is a single expression however long it grows — so there is
// no author break to preserve inside one.

// typeText renders a type expression at the loosest precedence, where an arrow
// needs no parentheses.
func typeText(t ast.TypeExpr) string {
	switch t := t.(type) {
	case *ast.TName:
		return t.Name
	case *ast.TVarName:
		return t.Name
	case *ast.TRow:
		return effRowText(t.Row)
	case *ast.TFunExpr:
		arrow := "->"
		if t.Eff != nil {
			arrow += effRowText(t.Eff)
		}
		// Arrows are right-associative, so only the argument side can need
		// parentheses; the result side chains without them.
		return typeAtomInArrow(t.Arg) + " " + arrow + " " + typeText(t.Ret)
	case *ast.TApp:
		if s, ok := tupleTypeText(t); ok {
			return s
		}
		parts := make([]string, 0, len(t.Args)+1)
		parts = append(parts, t.Name)
		for _, a := range t.Args {
			parts = append(parts, typeArgText(a))
		}
		return strings.Join(parts, " ")
	}
	return ""
}

// typeArgText renders a type in argument position, where an application or an
// arrow has to be parenthesized: `Maybe (List a)`, `List (a -> b)`.
func typeArgText(t ast.TypeExpr) string {
	switch t := t.(type) {
	case *ast.TApp:
		if _, ok := tupleTypeText(t); ok {
			return typeText(t)
		}
		return "(" + typeText(t) + ")"
	case *ast.TFunExpr:
		return "(" + typeText(t) + ")"
	}
	return typeText(t)
}

// typeAtomInArrow renders the argument side of an arrow, where only a nested
// arrow needs parentheses.
func typeAtomInArrow(t ast.TypeExpr) string {
	if _, ok := t.(*ast.TFunExpr); ok {
		return "(" + typeText(t) + ")"
	}
	return typeText(t)
}

// tupleTypeText recovers `(A, B)` from the bundled tuple type the parser
// lowers it to. The Sugared flag is the only signal: a hand-written
// `Tuple.Pair A B` shares the shape and must keep its application spelling.
func tupleTypeText(t *ast.TApp) (string, bool) {
	if !t.Sugared {
		return "", false
	}
	switch t.Name {
	case "Tuple.Pair", "Tuple.Triple":
	default:
		return "", false
	}
	parts := make([]string, len(t.Args))
	for i, a := range t.Args {
		parts[i] = typeText(a)
	}
	return "(" + strings.Join(parts, ", ") + ")", true
}

// effRowText renders a row wherever it stands — on an arrow or as a type
// argument: `{Console}`, `{Db, Fail String}`, `{Console | e}`.
func effRowText(r *ast.EffRow) string {
	labels := make([]string, len(r.Labels))
	for i, l := range r.Labels {
		parts := append([]string{l.Name}, nil...)
		for _, a := range l.Args {
			parts = append(parts, typeArgText(a))
		}
		labels[i] = strings.Join(parts, " ")
	}
	body := strings.Join(labels, ", ")
	if r.Tail != "" {
		if body == "" {
			return "{" + r.Tail + "}"
		}
		return "{" + body + " | " + r.Tail + "}"
	}
	return "{" + body + "}"
}

// annotationText renders `Preds => Type`, using the parenthesized form only
// when there is more than one constraint.
func annotationText(a *ast.TypeAnn) string {
	if len(a.Preds) == 0 {
		return typeText(a.Type)
	}
	return predsText(a.Preds) + " => " + typeText(a.Type)
}
