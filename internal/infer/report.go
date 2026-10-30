package infer

import (
	"fmt"
	"strings"

	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/types"
)

// A report says what went wrong in the terms of the code the author wrote: the
// callee, operator, or field a type came from, and which effects differ. The
// constraint's Why carries that origin, so the solver stays ignorant of syntax.

// mismatchError tells the story of one failed constraint. The constraint's own
// sides give the top-level story; the mismatch pair m is the leaf that failed.
func mismatchError(c Constraint, m *mismatch, sub Subst) diag.Error {
	left, right := sub.Apply(c.Left), sub.Apply(c.Right)
	if c.Declared != nil {
		left, right = sub.Apply(c.Declared), sub.Apply(c.Inferred)
	}
	p := types.NewPrinter().ForMessage(left, right, c.Signature)
	var e diag.Error
	switch {
	case c.Why.Kind == WhyEffectEscapes:
		e = escapingEffectError(c, left, p)
	case c.Declared != nil && m.effect, c.Why.Kind == WhyEffectMismatch:
		e = annotationEffectError(c, left, right, p)
	case m.effect && (c.Subsume || c.Include):
		e = disallowedEffectError(c, left, right, p, sub)
	default:
		e = typeMismatchError(c, left, right, p)
		// A row note restates the rule in the solver's vocabulary. The story
		// above already names the effects, so only value notes add anything.
		if m.note != "" && !m.effect {
			e.Notes = append(e.Notes, "Note: "+m.note)
		}
	}
	e.Notes = append(e.Notes, p.AliasNotes()...)
	return e
}

func typeMismatchError(c Constraint, left, right types.Type, p *types.Printer) diag.Error {
	l, r := p.Type(left), p.Type(right)
	var e diag.Error
	switch c.Why.Kind {
	case WhyProjection:
		e = diag.Errorf(c.Span, "TYPE MISMATCH", "The field `%s` holds:\n\n    %s\n\nbut it is used here as:\n\n    %s", c.Why.Name, l, r)
	case WhyField:
		e = diag.Errorf(c.Span, "TYPE MISMATCH", "The field `%s` holds:\n\n    %s\n\nbut this value is:\n\n    %s", c.Why.Name, r, l)
	case WhyListElement:
		e = diag.Errorf(c.Span, "TYPE MISMATCH", "All the elements of a list must have the same type. This element is:\n\n    %s\n\nbut the ones before it are:\n\n    %s",
			p.Type(listElement(left)), p.Type(listElement(right)))
	case WhyCall:
		e = callMismatchError(c, left, right, l, r, p)
	case WhyOpRequires:
		e = operatorMismatchError(c, left, l, r)
	case WhyResume:
		e = diag.Errorf(c.Span, "TYPE MISMATCH", "`resume` hands the operation's result back to its caller, so it needs:\n\n    %s\n\nbut this is:\n\n    %s", r, l)
	case WhyNextState:
		e = diag.Errorf(c.Span, "TYPE MISMATCH", "The next state must have the same type as the handler's state, which is:\n\n    %s\n\nbut this is:\n\n    %s", r, l)
	case WhyHandlerClause:
		e = diag.Errorf(c.Span, "TYPE MISMATCH", "The clauses of a handler must produce the same type. This one produces:\n\n    %s\n\nbut the others produce:\n\n    %s", l, r)
	case WhyHandlerSubject:
		e = diag.Errorf(c.Span, "TYPE MISMATCH", "The handled code produces:\n\n    %s\n\nbut the handler's clauses produce:\n\n    %s", l, r)
		e.Notes = append(e.Notes, "A `return value -> ...` clause can convert what the handled code produces.")
	case WhyStatement:
		e = diag.Errorf(c.Span, "TYPE MISMATCH", "The value of this statement would be thrown away, so it must be `()`, but it is:\n\n    %s", l)
		e.Notes = append(e.Notes, "Use it, or discard it on purpose with `ignore (...)`.")
	case WhyIfCondition:
		e = diag.Errorf(c.Span, "TYPE MISMATCH", "An `if` condition must be a Bool, but this one is:\n\n    %s", l)
	case WhyIfBranches:
		e = diag.Errorf(c.Span, "TYPE MISMATCH", "The branches of this `if` do not match. The `else` branch is:\n\n    %s\n\nbut the `then` branch is:\n\n    %s", l, r)
	case WhyBoolOperand:
		e = diag.Errorf(c.Span, "TYPE MISMATCH", "Both sides of (%s) must be a Bool, but this one is:\n\n    %s", c.Why.Op, l)
	case WhyRecursion:
		e = diag.Errorf(c.Span, "TYPE MISMATCH", "The recursive uses of `%s` do not match its definition.\nRecursive uses need:\n\n    %s\n\nbut the definition builds:\n\n    %s", c.Why.Name, l, r)
	case WhyAnnotation:
		e = annotationMismatchError(c, left, right, p)
	case WhyPattern:
		e = diag.Errorf(c.Span, "TYPE MISMATCH", "This pattern matches values of type:\n\n    %s\n\nbut it needs to match:\n\n    %s", l, r)
	case WhyCaseBranches:
		e = diag.Errorf(c.Span, "TYPE MISMATCH", "The branches of this `case` do not match. This branch is:\n\n    %s\n\nbut the earlier branches are:\n\n    %s", l, r)
	case WhySpliceOperand:
		e = diag.Errorf(c.Span, "TYPE MISMATCH", "A splice pastes generated code, so `$(…)` needs an operand that builds\ncode with a backtick quotation, but this one is:\n\n    %s", l)
	default:
		e = diag.Errorf(c.Span, "TYPE MISMATCH", "These types do not match:\n\n    %s\n\nand\n\n    %s", l, r)
	}
	if hint := unappliedHint(left, right); hint != "" {
		e.Notes = append(e.Notes, hint)
	}
	return e
}

// callMismatchError reports an argument that does not fit, or a callee that
// cannot take one.
func callMismatchError(c Constraint, left, right types.Type, l, r string, p *types.Printer) diag.Error {
	if c.Subsume {
		callee := "the function"
		if c.Why.Name != "" {
			callee = "`" + c.Why.Name + "`"
		}
		e := diag.Errorf(c.Span, "TYPE MISMATCH", "This argument has type:\n\n    %s\n\nbut %s expects:\n\n    %s", l, callee, r)
		if c.Why.TupleArg {
			e.Notes = append(e.Notes, "Arguments are separated by spaces, as in `f 1 2`. `f(1, 2)` passes one tuple.")
		}
		_, leftFun := left.(*types.TFun)
		_, rightFun := right.(*types.TFun)
		if c.Signature != nil && c.Why.Name != "" && (leftFun || rightFun) {
			e.Notes = append(e.Notes, fmt.Sprintf("`%s` has type:\n\n    %s", c.Why.Name, p.Type(c.Signature)))
		}
		return e
	}
	if _, ok := left.(*types.TFun); ok {
		return diag.Errorf(c.Span, "TYPE MISMATCH", "This function's argument type does not match this application.\nThe function has type:\n\n    %s\n\nbut this application requires:\n\n    %s", l, r)
	}
	var e diag.Error
	if c.Why.Name != "" && c.Why.Args > 1 {
		e = diag.Errorf(c.Span, "TYPE MISMATCH", "`%s` is given %d arguments here, but after %d it has finished, returning:\n\n    %s", c.Why.Name, c.Why.Args, c.Why.Args-1, l)
	} else {
		e = diag.Errorf(c.Span, "TYPE MISMATCH", "This is not a function, so I cannot give it an argument.\nIt has type:\n\n    %s", l)
	}
	if c.Why.UnitArg {
		e.Notes = append(e.Notes, "`print f ()` means `(print f) ()`. To call `f`, attach the parentheses: `print f()`.")
	}
	return e
}

func operatorMismatchError(c Constraint, left types.Type, l, r string) diag.Error {
	if c.Why.Op == "|>" {
		return diag.Errorf(c.Span, "TYPE MISMATCH", "(|>) passes its left side to this function, which has type:\n\n    %s\n\nbut it would need to accept:\n\n    %s", l, r)
	}
	e := diag.Errorf(c.Span, "TYPE MISMATCH", "(%s) needs:\n\n    %s\n\nbut this operand is:\n\n    %s", c.Why.Op, r, l)
	switch {
	case c.Why.Op == "/" && l == "Int":
		e.Notes = append(e.Notes, "(/) works only on `Float`, and an `Int` does not convert by itself. Convert with\n`Basics.fromInt`, or divide integers with `Basics.quotientBy`.")
	case c.Why.Op == "++" && r == "String" && l != "String":
		if _, isFun := left.(*types.TFun); !isFun {
			e.Notes = append(e.Notes, "Turn a value into text with `show`, or use interpolation: `\"#{value}\"`.")
		}
	}
	return e
}

// unappliedHint recognizes a function used where its result was wanted.
func unappliedHint(left, right types.Type) string {
	fn, ok := left.(*types.TFun)
	if !ok {
		return ""
	}
	switch right.(type) {
	case *types.TFun, *types.TVar:
		return ""
	}
	if con, ok := fn.Arg.(*types.TCon); ok && len(con.Args) == 0 && types.SurfaceName(con.Name) == "()" {
		return "This is a function that has not been called. Did you mean to call it with `()`?"
	}
	return "This is a function still waiting for an argument. Did you leave one out?"
}

func listElement(t types.Type) types.Type {
	if con, ok := t.(*types.TCon); ok && len(con.Args) == 1 {
		return con.Args[0]
	}
	return t
}

// annotationMismatchError narrows a whole-function comparison to the result
// when that is the only difference, which is where the eye would end up.
func annotationMismatchError(c Constraint, left, right types.Type, p *types.Printer) diag.Error {
	ann, body := left, right
	peeled := false
	for {
		af, aok := ann.(*types.TFun)
		bf, bok := body.(*types.TFun)
		if !aok || !bok || !types.Equal(af.Arg, bf.Arg) {
			break
		}
		ann, body, peeled = af.Ret, bf.Ret, true
	}
	_, annFun := ann.(*types.TFun)
	_, bodyFun := body.(*types.TFun)
	if peeled && !annFun && !bodyFun {
		return diag.Errorf(c.Span, "TYPE MISMATCH", "The annotation for `%s` says it returns:\n\n    %s\n\nbut its body returns:\n\n    %s", c.Why.Name, p.Type(ann), p.Type(body))
	}
	return diag.Errorf(c.Span, "TYPE MISMATCH", "The type annotation for `%s` says it is:\n\n    %s\n\nbut the body I found is:\n\n    %s", c.Why.Name, p.Type(left), p.Type(right))
}

// effectDifference is what one row has that another lacks.
type effectDifference struct {
	labels []types.EffLabel
	tail   *types.TVar
}

func (d effectDifference) empty() bool { return len(d.labels) == 0 && d.tail == nil }

// text lists the effects for prose: "`Console`", "`Console` and `Log`", and for
// a row variable "the effects of `e`".
func (d effectDifference) text(p *types.Printer) string {
	var parts []string
	for _, l := range d.labels {
		if !l.Scoped {
			parts = append(parts, "`"+strings.Trim(p.Type(types.Row{Labels: []types.EffLabel{l}}), "{}")+"`")
		}
	}
	if d.tail != nil {
		parts = append(parts, "the effects of `"+p.Type(d.tail)+"`")
	}
	switch len(parts) {
	case 0:
		return "more effects"
	case 1:
		return parts[0]
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
}

// rowDifference is what sub has beyond super. A flexible tail is inference's
// own and is not a difference.
func rowDifference(sub, super types.Row) effectDifference {
	var d effectDifference
	for _, l := range sub.Labels {
		if !rowHasApplication(super, l) {
			d.labels = append(d.labels, l)
		}
	}
	if v, ok := sub.Tail.(*types.TVar); ok && v.Rigid && sub.Tail != super.Tail {
		d.tail = v
	}
	return d
}

// showAllowed lists a row for prose, with permissions and inference tails hidden.
func showAllowed(row types.Row, p *types.Printer) string {
	return rowDifference(row, types.Row{}).text(p)
}

func escapingEffectError(c Constraint, left types.Type, p *types.Printer) diag.Error {
	performed := "an effect"
	if row, ok := left.(types.Row); ok && len(row.Labels) > 0 {
		performed = rowDifference(row, types.Row{}).text(p)
	}
	e := diag.Errorf(c.Span, "UNHANDLED EFFECT", "Top-level values must be pure, but this one performs %s.", performed)
	e.Notes = append(e.Notes, "Make it a function, like `name() = ...`, and call it from `main`.")
	return e
}

// disallowedEffectError reports a row that does not fit where it goes: the
// callback or field position it was passed to, or the annotated function
// around it.
func disallowedEffectError(c Constraint, left, right types.Type, p *types.Printer, sub Subst) diag.Error {
	span := c.Span
	lr, lok := left.(types.Row)
	rr, rok := right.(types.Row)
	if !lok || !rok {
		return diag.Errorf(span, "EFFECT MISMATCH", "These effects do not match:\n\n    %s\n\nand\n\n    %s", p.Type(left), p.Type(right))
	}
	diff := rowDifference(lr, rr)
	if diff.empty() {
		return diag.Errorf(span, "EFFECT MISMATCH", "This performs:\n\n    %s\n\nbut only these effects are available here:\n\n    %s", p.Type(left), p.Type(right))
	}
	performed := diff.text(p)
	pure := len(rr.Labels) == 0 && rr.Tail == nil
	// A long callback's whole span would underline only its opening line, so
	// point at what performs the effect instead.
	if span.File != nil && span.StartPos().Line != span.EndPos().Line {
		if site := c.siteOf(diff.labels, span, sub); site != nil {
			span = site.span
		}
	}
	allowed := showAllowed(rr, p)
	switch {
	case c.Why.Resume:
		e := diag.Errorf(span, "EFFECT MISMATCH", "The handled code still performs %s, which this handler does not handle.", performed)
		if c.Why.Name != "" {
			e.Body += fmt.Sprintf("\n`%s` does not allow it either.", c.Why.Name)
		}
		e.Notes = append(e.Notes, "Add a clause for each of its operations, or let it through by listing it in the annotation.")
		return e
	case c.Why.Kind == WhyField, c.Why.Arg:
		who := "This position"
		switch {
		case c.Why.Kind == WhyField:
			who = "The field `" + c.Why.Name + "`"
		case c.Why.Name != "":
			who = "`" + c.Why.Name + "`"
		}
		var e diag.Error
		if pure {
			e = diag.Errorf(span, "EFFECT MISMATCH", "%s expects a function that performs no effects, but this one performs %s.", who, performed)
		} else {
			e = diag.Errorf(span, "EFFECT MISMATCH", "%s allows only %s in this function, but it also performs %s.", who, allowed, performed)
		}
		switch {
		case c.Why.Kind == WhyField:
			e.Notes = append(e.Notes, "To allow it, add the effect to the field's arrow in its type declaration.")
		case pure && c.Why.Name != "" && !strings.Contains(c.Why.Name, "."):
			e.Notes = append(e.Notes, fmt.Sprintf("To accept effects, make `%s` generic over them: give this parameter's arrow a\nrow variable, as in `->{e}`, and add `{e}` to the final arrow of `%s`.", c.Why.Name, c.Why.Name))
		}
		return e
	case c.Why.Name != "" && pure:
		return diag.Errorf(span, "EFFECT MISMATCH", "This performs %s, but the annotation for `%s` allows no effects here.", performed, c.Why.Name)
	case c.Why.Name != "":
		return diag.Errorf(span, "EFFECT MISMATCH", "This performs %s, but the annotation for `%s` allows only %s here.", performed, c.Why.Name, allowed)
	}
	return diag.Errorf(span, "EFFECT MISMATCH", "This performs %s, but only these effects are available here:\n\n    %s", performed, allowed)
}

// annotationEffectError reports an annotation that disagrees with its body
// about effects, in the arrows' row variables or a label's arguments. The two
// whole types still print, because the row that differs may be anywhere.
func annotationEffectError(c Constraint, declared, inferred types.Type, p *types.Printer) diag.Error {
	span := c.Span
	if c.AnnSpan.File != nil {
		span = c.AnnSpan
	}
	name := c.Why.Name
	var missing, extra effectDifference
	for ann, body := declared, inferred; ; {
		af, aok := ann.(*types.TFun)
		bf, bok := body.(*types.TFun)
		if !aok || !bok {
			break
		}
		if d := rowDifference(bf.Eff, af.Eff); missing.empty() {
			missing = d
		}
		if d := rowDifference(af.Eff, bf.Eff); extra.empty() {
			extra = d
		}
		ann, body = af.Ret, bf.Ret
	}
	var lead string
	switch {
	case name == "":
		lead = "This annotation's effects do not match its body."
	case !missing.empty():
		lead = fmt.Sprintf("`%s` performs %s, which its annotation does not list.", name, missing.text(p))
	case !extra.empty():
		lead = fmt.Sprintf("The annotation for `%s` lists %s, which its body does not perform.", name, extra.text(p))
	default:
		lead = fmt.Sprintf("The effects in the annotation for `%s` do not match its body.", name)
	}
	e := diag.Errorf(span, "EFFECT MISMATCH", "%s", lead)
	e.Notes = append(e.Notes, fmt.Sprintf("The annotation says:\n\n    %s\n\nbut the body needs:\n\n    %s", p.Type(declared), p.Type(inferred)))
	return e
}

// performSite is one place a body performs effects, kept so a report about a
// row can show the call responsible.
type performSite struct {
	row     types.Type
	span    source.Span
	handled []int // uniques of the effects enclosing handlers discharge here
}

// firstSite returns the first recorded call performing one of labels that no
// enclosing handler discharges, and inside within when within has a file.
func firstSite(sites []performSite, labels []types.EffLabel, within source.Span, sub Subst) *performSite {
	for i := range sites {
		site := &sites[i]
		if within.File != nil && (site.span.File != within.File || site.span.Start < within.Start || site.span.End > within.End) {
			continue
		}
		row, ok := sub.Apply(site.row).(types.Row)
		if !ok {
			continue
		}
		for _, l := range labels {
			if rowHasApplication(row, l) && !containsInt(site.handled, l.Unique) {
				return site
			}
		}
	}
	return nil
}

func containsInt(xs []int, x int) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func (c Constraint) siteOf(labels []types.EffLabel, within source.Span, sub Subst) *performSite {
	if c.Sites == nil {
		return nil
	}
	return firstSite(*c.Sites, labels, within, sub)
}

// settleErrors finishes one declaration's reports: what depends on an earlier
// failure goes, and the rest read in source order. A broken annotation has no
// type to compare against, and an unsolved constraint usually only means
// inference stopped at the real error, so they would each send the reader after
// a problem that disappears with the fix.
func settleErrors(errs []diag.Error, annotationFailed bool) []diag.Error {
	if len(errs) == 0 {
		return errs
	}
	dependsOnAnnotation := map[string]bool{"TYPE MISMATCH": true, "EFFECT MISMATCH": true, "MISSING CONSTRAINT": true, "AMBIGUOUS CONSTRAINT": true, "BAD MAIN": true, "MISSING INSTANCE": true}
	primary, bad := false, false
	for _, e := range errs {
		switch e.Title {
		case "AMBIGUOUS CONSTRAINT":
		case "BAD MAIN":
			bad = true
		default:
			primary = true
		}
	}
	// Two reports of one annotation's effects say the same thing; keep the one
	// that shows the call responsible.
	effects := map[source.Span]int{}
	for i, e := range errs {
		if e.Title != "EFFECT MISMATCH" {
			continue
		}
		if at, seen := effects[e.Span]; !seen || len(errs[at].Related) == 0 && len(e.Related) > 0 {
			effects[e.Span] = i
		}
	}
	mismatch := false
	for _, e := range errs {
		mismatch = mismatch || e.Title == "TYPE MISMATCH"
	}
	operators := map[string]bool{}
	out := errs[:0:0]
	for i, e := range errs {
		switch {
		case annotationFailed && dependsOnAnnotation[e.Title]:
		case primary && (e.Title == "AMBIGUOUS CONSTRAINT" || e.Title == "BAD MAIN"):
		case bad && e.Title == "AMBIGUOUS CONSTRAINT":
		case e.Title == "EFFECT MISMATCH" && effects[e.Span] != i:
		// A function reached `print` or `==` because an earlier argument did
		// not fit, and that report is the one to read.
		case mismatch && e.Title == "MISSING INSTANCE" && strings.Contains(e.Body, functionsLackInstance):
		// One operator complaining about both its operands is one complaint.
		case e.Title == "TYPE MISMATCH" && strings.HasPrefix(e.Body, "(") && operators[e.Body]:
		default:
			if e.Title == "TYPE MISMATCH" && strings.HasPrefix(e.Body, "(") {
				operators[e.Body] = true
			}
			out = append(out, e)
		}
	}
	diag.SortBySource(out)
	return out
}

// withoutEffectMismatches drops the annotation bound's row reports when the
// effect comparison has already named the difference.
func withoutEffectMismatches(errs []diag.Error) []diag.Error {
	out := errs[:0:0]
	for _, e := range errs {
		if e.Title != "EFFECT MISMATCH" {
			out = append(out, e)
		}
	}
	return out
}

func hasEffectMismatch(errs []diag.Error) bool {
	for _, e := range errs {
		if e.Title == "EFFECT MISMATCH" {
			return true
		}
	}
	return false
}
