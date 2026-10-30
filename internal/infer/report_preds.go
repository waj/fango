package infer

import (
	"fmt"
	"sort"
	"strings"

	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/types"
)

// literalOp is the callee of a desugared number literal, which is how a failed
// `Num` obligation is recognized as a literal rather than an operator or call.
const literalOp = "Basics.fromInt"

type literalKind int

const (
	literalAnywhere literalKind = iota
	literalArgument
	literalIfBranch
	literalCondition
	literalCaseBranch
	literalListElement
	literalResume
	literalState
	literalClause
	literalField
	literalPattern
)

// literalContext is where a number literal was written, so that one needing a
// type that is no number reports as the mismatch it is.
type literalContext struct {
	kind literalKind
	name string
}

type failedObligation struct {
	obligation predObligation
	resolution InstanceResolution
}

// obligationErrors reports what no instance satisfies. A literal is the same
// number however often it recurs, so `Num String` reports once per kind of
// place, and not at all beside an operator that already explains it.
func (ck *Checker) obligationErrors(failed []failedObligation) []diag.Error {
	if len(failed) == 0 {
		return nil
	}
	isNum := func(o predObligation) bool { return o.pred.Class == "Basics.Num" }
	operatorFailed := map[string]bool{}
	for _, f := range failed {
		if isNum(f.obligation) && f.obligation.op != literalOp {
			operatorFailed[types.Show(ck.Sub.Apply(f.obligation.pred.Ty))] = true
		}
	}
	seen := map[string]bool{}
	var errs []diag.Error
	for _, f := range failed {
		o := f.obligation
		if isNum(o) {
			ty := types.Show(ck.Sub.Apply(o.pred.Ty))
			if o.op == literalOp && operatorFailed[ty] {
				continue
			}
			key := fmt.Sprintf("%s\x00%s\x00%d\x00%s", ty, o.op, o.context.kind, o.context.name)
			if seen[key] {
				continue
			}
			seen[key] = true
			if o.op == literalOp {
				errs = append(errs, ck.literalMismatch(o))
			} else {
				errs = append(errs, ck.numericUseMismatch(o))
			}
			continue
		}
		errs = append(errs, ck.instanceProblem(o, f.resolution))
	}
	return errs
}

func (ck *Checker) literalMismatch(o predObligation) diag.Error {
	ty := ck.Sub.Apply(o.pred.Ty)
	t := types.NewPrinter().ForMessage(ty).Type(ty)
	var body string
	switch o.context.kind {
	case literalArgument:
		callee := "the function"
		if o.context.name != "" {
			callee = "`" + o.context.name + "`"
		}
		body = fmt.Sprintf("This argument is a number, but %s expects:\n\n    %s", callee, t)
	case literalIfBranch:
		body = fmt.Sprintf("The branches of this `if` do not match. This one is a number, but the other is:\n\n    %s", t)
	case literalCondition:
		body = "An `if` condition must be a Bool, but this one is a number."
	case literalCaseBranch:
		body = fmt.Sprintf("The branches of this `case` do not match. This one is a number, but the others are:\n\n    %s", t)
	case literalListElement:
		body = fmt.Sprintf("This list element is a number, but the list has to hold:\n\n    %s", t)
	case literalResume:
		body = fmt.Sprintf("`resume` hands the operation's result back to its caller, so it needs:\n\n    %s\n\nbut this is a number.", t)
	case literalState:
		body = fmt.Sprintf("This handler's state starts as a number, but its clauses use it as:\n\n    %s", t)
	case literalClause:
		body = fmt.Sprintf("The clauses of a handler must produce the same type. This one is a number, but the others produce:\n\n    %s", t)
	case literalField:
		body = fmt.Sprintf("The field `%s` holds:\n\n    %s\n\nbut this is a number.", o.context.name, t)
	case literalPattern:
		body = fmt.Sprintf("This pattern is a number, but it needs to match:\n\n    %s", t)
	default:
		body = fmt.Sprintf("This is a number, but it is needed here as:\n\n    %s", t)
	}
	return diag.Errorf(o.span, "TYPE MISMATCH", "%s", body)
}

// numericUseMismatch reports an operator or numeric function used at a type
// that has no `Num` instance.
func (ck *Checker) numericUseMismatch(o predObligation) diag.Error {
	ty := ck.Sub.Apply(o.pred.Ty)
	t := types.NewPrinter().ForMessage(ty).Type(ty)
	name := ck.calleeName(o.op)
	if isIdentifier(name) && !numMethod(o.op) {
		// A function whose inferred type carries `Num`: it uses a number
		// somewhere that the rest of its body says is something else.
		e := diag.Errorf(o.span, "TYPE MISMATCH", "%s uses numbers, but here it would need a number to be:\n\n    %s", ck.showOp(o.op), t)
		e.Notes = append(e.Notes, fmt.Sprintf("Check the number literals and arithmetic in `%s` and what is passed to it.", name))
		return e
	}
	e := diag.Errorf(o.span, "TYPE MISMATCH", "%s works on numbers, but here it is used on:\n\n    %s", ck.showOp(o.op), t)
	con, isCon := ty.(*types.TCon)
	switch {
	case isCon && con.Unique == ck.B.String.Unique:
		e.Notes = append(e.Notes, "To join text use `++`. To turn a number into text use `show`, or interpolation: `\"#{value}\"`.")
	case isCon && !strings.Contains(con.Name, ".") && con.Unique != ck.B.Bool.Unique && ck.ADTs[con.Unique] != nil:
		e.Notes = append(e.Notes, fmt.Sprintf("If `%s` is meant to be a number, write an `instance Num` for it. Otherwise take the\nnumber out of it first.", types.SurfaceName(con.Name)))
	}
	return e
}

// numMethod reports whether name is a method of the `Num` class itself.
func numMethod(name string) bool {
	switch types.SurfaceName(name) {
	case "negate", "fromInt":
		return strings.HasPrefix(name, "Basics.")
	}
	return false
}

// showOp spells an obligation's callee for prose.
func (ck *Checker) showOp(op string) string {
	if op == "" {
		return "This"
	}
	if op == "Basics.displayTo" {
		return "String interpolation"
	}
	name := ck.calleeName(op)
	if !isIdentifier(name) {
		return "(" + name + ")"
	}
	return "`" + name + "`"
}

// instanceProblem reports a class constraint nothing provides, saying what
// wanted it and how to supply it.
func (ck *Checker) instanceProblem(o predObligation, r InstanceResolution) diag.Error {
	err := *r.Error
	err.Span = o.span
	missing := o.pred
	if r.Missing != nil {
		missing = *r.Missing
	}
	ty := ck.Sub.Apply(missing.Ty)
	class := types.SurfaceName(missing.Class)
	if _, isFun := ty.(*types.TFun); isFun {
		return ck.functionInstanceError(o, class, ty, err.Title)
	}
	// Generated code's callee means nothing to the author, who reads it from
	// the `deriving` it came from.
	if o.op != "" && (o.span.File == nil || !strings.HasPrefix(o.span.File.Name, "<stdlib>")) {
		err.Body += fmt.Sprintf(" %s needs it.", ck.showOp(o.op))
	}
	if con, ok := ty.(*types.TCon); ok && ck.ADTs[con.Unique] != nil && ck.Derivers[missing.Class] != nil {
		err.Notes = append(err.Notes, fmt.Sprintf("Add `deriving (%s)` to the declaration of `%s`, or write an `instance %s` for it.", class, types.SurfaceName(con.Name), class))
	}
	return err
}

// functionsLackInstance is the phrase settleErrors recognizes these reports by.
const functionsLackInstance = "which functions do not have"

func (ck *Checker) functionInstanceError(o predObligation, class string, ty types.Type, title string) diag.Error {
	t := types.NewPrinter().ForMessage(ty).Type(ty)
	who := "This"
	if o.op != "" && o.op != literalOp {
		who = ck.showOp(o.op)
	}
	what := "this is a function"
	if _, direct := ty.(*types.TFun); !direct {
		what = "this contains a function"
	}
	e := diag.Errorf(o.span, title, "%s needs `%s`, %s, but %s:\n\n    %s", who, class, functionsLackInstance, what, t)
	e.Notes = append(e.Notes, "Did you forget to call it, or to pass all its arguments?")
	return e
}

// containsFunction reports whether a value of type t can hold a function.
func containsFunction(t types.Type) bool {
	switch t := t.(type) {
	case *types.TFun:
		return true
	case *types.TCon:
		for _, a := range t.Args {
			if containsFunction(a) {
				return true
			}
		}
	}
	return false
}

// undeterminedError reports a constraint on a type inference never settled. A
// function type there is not really undetermined: something that wanted a
// number or a printable value got a function, with only its result unknown.
func (ck *Checker) undeterminedError(o predObligation) diag.Error {
	ty := ck.Sub.Apply(o.pred.Ty)
	class := types.SurfaceName(o.pred.Class)
	t := types.NewPrinter().ForMessage(ty).Type(ty)
	if containsFunction(ty) && o.pred.Class != "Basics.Num" {
		return ck.functionInstanceError(o, class, ty, "MISSING INSTANCE")
	}
	if _, isFun := ty.(*types.TFun); isFun {
		switch {
		case o.op == literalOp && o.context.kind == literalArgument:
			return ck.literalMismatch(o)
		case o.op == literalOp:
			return diag.Errorf(o.span, "TYPE MISMATCH", "This is a number, so it cannot be called, but it is being given an argument.")
		}
		return diag.Errorf(o.span, "TYPE MISMATCH", "%s is given too many arguments here. It works with numbers, so its result cannot be a function like:\n\n    %s", ck.showOp(o.op), t)
	}
	who := "This"
	if o.op != "" && o.op != literalOp {
		who = ck.showOp(o.op)
	}
	e := diag.Errorf(o.span, "AMBIGUOUS CONSTRAINT", "I cannot tell which type this is, and %s needs to know, for `%s`.", strings.ToLower(who[:1])+who[1:], types.NewPrinter().ForMessage(ty).Pred(o.pred.Class, ty))
	e.Notes = append(e.Notes, "Add a type annotation that says which type it is.")
	return e
}

// missingConstraint reports a class constraint the body needs and the
// annotation does not list, at the expression needing it.
func (ck *Checker) missingConstraint(o predObligation, sp source.Span, given []types.Pred) diag.Error {
	if o.span.File != nil {
		sp = o.span
	}
	ty := ck.Sub.Apply(o.pred.Ty)
	shown := types.NewPrinter().ForMessage(ty).Pred(o.pred.Class, ty)
	e := diag.Errorf(sp, "MISSING CONSTRAINT", "This needs `%s`, which the annotation does not list.", shown)
	e.Notes = append(e.Notes, fmt.Sprintf("Add `%s =>` to the annotation.", shown))
	return ck.withBlanketNote(e, o.pred, given)
}

func unknownField(sp source.Span, adt *types.ADTInfo, name string) diag.Error {
	e := diag.Errorf(sp, "UNKNOWN FIELD", "Record `%s` has no field named `%s`.", types.SurfaceName(adt.Con.Name), name)
	names := make([]string, len(adt.RecordFields))
	for i, f := range adt.RecordFields {
		names[i] = f.Name
	}
	if hint := diag.DidYouMean(name, names); hint != "" {
		e.Notes = append(e.Notes, hint)
	}
	return e
}

// surfaceKeys lists a name table's unqualified keys, which are the spellings a
// reader of this module could use.
func surfaceKeys[V any](table map[string]V) []string {
	var out []string
	for name := range table {
		if !strings.Contains(name, ".") {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func (g *generator) visibleValueNames() []string {
	var names []string
	for s := g.locals; s != nil; s = s.parent {
		for name := range s.names {
			names = append(names, name)
		}
	}
	for _, name := range g.ck.Env.Names() {
		if !strings.Contains(name, ".") {
			names = append(names, name)
		}
	}
	return names
}
