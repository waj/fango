package format

import (
	"strings"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/source"
)

// raw returns the source text a node occupies. Literal spelling is decoded at
// parse time — `1.50` becomes 1.5, a string's escapes are resolved — so every
// literal is printed through here rather than re-rendered from its value.
func raw(sp source.Span) string {
	if sp.File == nil {
		return ""
	}
	return string(sp.File.Content[sp.Start:sp.End])
}

// exprInline renders an expression on one line, reporting whether it could.
// It fails for the layout constructs, which have no one-line spelling, and for
// anything the printer does not handle; the caller then copies the enclosing
// declaration verbatim.
func exprInline(e ast.Expr) (string, bool) {
	switch e := e.(type) {
	case *ast.IntLit:
		return raw(e.Sp), !e.Raw
	case *ast.FloatLit:
		return raw(e.Sp), true
	case *ast.RegexLit:
		return e.Raw, true
	case *ast.StringLit:
		return raw(e.Sp), true
	case *ast.CharLit:
		return raw(e.Sp), true
	case *ast.UnitLit:
		return "()", true
	case *ast.Var:
		return e.Name, true
	case *ast.Ctor:
		if e.Witness != nil {
			if _, applied := e.Witness.(*ast.TApp); applied {
				return "@(" + typeText(e.Witness) + ")", true
			}
			return "@" + typeText(e.Witness), true
		}
		if e.Sugared && e.Name == "List.Nil" {
			return "[]", true
		}
		return e.Name, true
	case *ast.RecordGet:
		recv, ok := exprAtomInline(e.Record)
		return recv + "." + e.Field, ok
	case *ast.RecordLit:
		fields, ok := recordFieldsInline(e.Fields)
		if e.Name != "" {
			return e.Name + " { " + fields + " }", ok
		}
		return "{ " + fields + " }", ok
	case *ast.RecordUpdate:
		recv, ok1 := exprAtomInline(e.Record)
		fields, ok2 := recordFieldsInline(e.Fields)
		return "{ " + recv + " | " + fields + " }", ok1 && ok2
	case *ast.Neg:
		operand, ok := exprAtomInline(e.Operand)
		return "-" + operand, ok
	case *ast.App:
		if lambda, ok := withLambda(e); ok {
			return withInline(e.Fn, lambda)
		}
		return appInline(e)
	case *ast.OpChain:
		return opChainInline(e)
	case *ast.BinOp:
		l, ok1 := exprOperandInline(e.L)
		r, ok2 := exprOperandInline(e.R)
		return l + " " + e.Op + " " + r, ok1 && ok2
	case *ast.If:
		c, ok1 := exprInline(e.Cond)
		t, ok2 := exprInline(e.Then)
		f, ok3 := exprInline(e.Else)
		return "if " + c + " then " + t + " else " + f, ok1 && ok2 && ok3
	case *ast.Lambda:
		body, ok2 := exprInline(e.Body)
		if len(e.Params) == 1 {
			if _, unit := e.Params[0].(*ast.PUnit); unit {
				return "{ " + body + " }", ok2
			}
		}
		params, ok1 := patternsInline(e.Params)
		return "{ " + params + " -> " + body + " }", ok1 && ok2
	case *ast.Block:
		return semicolonBlockInline(e)
	case *ast.Resume:
		if e.NextState == nil {
			return "resume", true
		}
		next, ok := exprAtomInline(e.NextState)
		return "resume with " + next, ok
	case *ast.Quote:
		body, ok := exprInline(e.Body)
		return "`" + body + "`", ok
	case *ast.Splice:
		operand, ok := exprInline(e.Operand)
		return "$(" + operand + ")", ok
	case *ast.TypeOf:
		return "typeOf " + typeArgText(e.Ty), true
	}
	// Block, Case, Handle and MetaValue have no inline spelling.
	return "", false
}

// semicolonBlockInline renders the explicit one-line spelling of a block.
// Layout blocks deliberately have no inline form; Semicolons is what
// distinguishes source that may safely retain separators when compacted.
func semicolonBlockInline(b *ast.Block) (string, bool) {
	if len(b.Semicolons) == 0 {
		return "", false
	}
	var parts []string
	for _, item := range blockItems(b) {
		if item.Expr != nil {
			s, ok := exprInline(item.Expr)
			if !ok {
				return "", false
			}
			parts = append(parts, s)
			continue
		}
		bindParts, ok := localBindInline(b.Binds[item.BindIndex])
		if !ok {
			return "", false
		}
		parts = append(parts, bindParts...)
	}
	result, ok := exprInline(b.Result)
	if !ok {
		return "", false
	}
	parts = append(parts, result)
	if len(parts) != len(b.Semicolons)+1 {
		return "", false
	}
	return strings.Join(parts, "; "), true
}

// withInline renders a `with` item of a semicolon block and the rest of that
// block after its separator.
func withInline(head ast.Expr, lambda *ast.Lambda) (string, bool) {
	if lambda.With.Semi.File == nil {
		return "", false
	}
	text := "with "
	if lambda.With.Arrow.File != nil {
		params, ok := patternsInline(lambda.Params)
		if !ok {
			return "", false
		}
		text += params + " <- "
	}
	h, ok1 := exprInline(head)
	rest, ok2 := exprInline(lambda.Body)
	return text + h + "; " + rest, ok1 && ok2
}

func localBindInline(b ast.LocalBind) ([]string, bool) {
	var parts []string
	if b.Ann != nil {
		parts = append(parts, declName(b.Name)+" : "+annotationText(b.Ann))
	}
	if b.Pattern != nil {
		pat, ok1 := patternInline(b.Pattern)
		body, ok2 := exprInline(b.Body)
		if !ok1 || !ok2 {
			return nil, false
		}
		return append(parts, pat+" = "+body), true
	}
	for _, eq := range localEquations(b) {
		params, ok1 := patternsInline(eq.Params)
		body, ok2 := exprInline(eq.Body)
		if !ok1 || !ok2 {
			return nil, false
		}
		head := declName(b.Name)
		if params != "" {
			head += " " + params
		}
		parts = append(parts, head+" = "+body)
	}
	return parts, true
}

// atomic reports whether an expression needs no parentheses in argument
// position, either because it is a single token or because it brackets itself.
func atomic(e ast.Expr) bool {
	switch e := e.(type) {
	case *ast.IntLit, *ast.FloatLit, *ast.RegexLit, *ast.StringLit, *ast.CharLit, *ast.UnitLit,
		*ast.Var, *ast.RecordGet, *ast.Splice, *ast.Quote:
		return true
	case *ast.RecordLit:
		// `{ x = 1 }` brackets itself; `Named { x = 1 }` is two tokens and is
		// parenthesized in argument position, the way a named record pattern
		// is, so the two sides of the language agree.
		return e.Name == ""
	case *ast.RecordUpdate:
		// Brackets itself, but see recordAfter: a brace directly after an
		// uppercase name binds to that name instead.
		return true
	case *ast.Lambda:
		return true
	case *ast.Ctor:
		return true
	case *ast.Resume:
		return e.NextState == nil
	case *ast.App:
		// A list or tuple literal brackets itself; an ordinary application
		// does not.
		if _, _, ok := asList(e); ok {
			return true
		}
		if _, ok := asTuple(e); ok {
			return true
		}
		// A postfix unit call binds tighter than application, so it is safe as
		// an argument: `foo bar()` stays distinct from `foo bar ()`.
		_, args := spine(e)
		if len(args) > 0 {
			if _, isUnit := args[len(args)-1].(*ast.UnitLit); isUnit && adjacent(args[len(args)-1]) {
				return true
			}
		}
		// Other applications in argument position need parentheses: without
		// them, `g (h x) y` would read as three arguments.
		return false
	}
	return false
}

// exprAtomInline renders an expression in a position that binds tightly,
// parenthesizing anything that is not already an atom.
func exprAtomInline(e ast.Expr) (string, bool) {
	s, ok := exprInline(e)
	if !ok {
		return s, false
	}
	if atomic(e) {
		return s, true
	}
	return "(" + s + ")", true
}

// exprOperandInline renders an operand of an operator run. A nested run can
// only have come from explicit parentheses, since runs parse flat, so
// parenthesizing exactly those reproduces the author's grouping.
func exprOperandInline(e ast.Expr) (string, bool) {
	s, ok := exprInline(e)
	if !ok {
		return s, false
	}
	switch e.(type) {
	case *ast.OpChain, *ast.BinOp, *ast.If:
		return "(" + s + ")", true
	}
	return s, true
}

// appInline renders an application spine, recovering list and tuple literals
// from the constructor applications the parser lowered them to.
func appInline(e *ast.App) (string, bool) {
	if elems, tail, ok := asList(e); ok {
		return listInline(elems, tail)
	}
	if elems, ok := asTuple(e); ok {
		return tupleInline(elems)
	}

	fn, args := spine(e)

	// `resume` heads an application, so `resume value with next` is the head,
	// its argument, and then the state clause — which therefore has to be
	// rendered after the arguments rather than on the head.
	if r, isResume := fn.(*ast.Resume); isResume && r.NextState != nil {
		parts := []string{"resume"}
		for _, a := range args {
			s, argOK := exprAtomInline(a)
			if !argOK {
				return "", false
			}
			parts = append(parts, s)
		}
		next, nextOK := exprInline(r.NextState)
		if !nextOK {
			return "", false
		}
		return strings.Join(parts, " ") + " with " + next, true
	}

	head, ok := exprAtomInline(fn)
	if !ok {
		return "", false
	}
	parts := []string{head}
	for i, a := range args {
		// `f()` and `f ()` differ in tree depth, so the spacing the author
		// used is what distinguishes them and is reproduced here.
		if _, isUnit := a.(*ast.UnitLit); isUnit && i == len(args)-1 && adjacent(a) {
			return strings.Join(parts, " ") + "()", true
		}
		s, argOK := exprAtomInline(a)
		if !argOK {
			return "", false
		}
		if literal, isRecord := a.(*ast.RecordLit); isRecord && literal.Name == "" {
			parts = append(parts, recordAfter(parts[len(parts)-1], s))
		} else {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, " "), true
}

// adjacent reports whether the argument was written with no space before it,
// which is what makes `f()` bind tighter than `f ()`.
func adjacent(arg ast.Expr) bool {
	sp := arg.Span()
	return writtenAgainst(sp.File, sp.Start)
}

// spine flattens curried application into a head and its arguments.
func spine(e ast.Expr) (ast.Expr, []ast.Expr) {
	var args []ast.Expr
	for {
		app, ok := e.(*ast.App)
		if !ok {
			return e, args
		}
		args = append([]ast.Expr{app.Arg}, args...)
		e = app.Fn
	}
}

// opChainInline renders a run of infix operators exactly as written. Fixity is
// not known here — it needs the whole module graph — and is not needed: the
// run is printed flat, never regrouped.
func opChainInline(e *ast.OpChain) (string, bool) {
	var b strings.Builder
	for i, operand := range e.Operands {
		if i > 0 {
			b.WriteString(" " + e.Ops[i-1].Op + " ")
		}
		s, ok := exprOperandInline(operand)
		if !ok {
			return "", false
		}
		b.WriteString(s)
	}
	return b.String(), true
}

func recordFieldsInline(fields []ast.RecordExprField) (string, bool) {
	parts := make([]string, len(fields))
	for i, f := range fields {
		v, ok := exprInline(f.Value)
		if !ok {
			return "", false
		}
		parts[i] = f.Name + " = " + v
	}
	return strings.Join(parts, ", "), true
}

// asList peels the right-nested constructor spine a bracket list lowers to.
// The Sugared flag is the only signal that distinguishes it from a written
// `List.Cons a b`: every synthetic constructor shares the whole bracket span,
// so spans cannot tell them apart.
func asList(e ast.Expr) (elems []ast.Expr, tail ast.Expr, ok bool) {
	cur := e
	for {
		if c, isCtor := cur.(*ast.Ctor); isCtor && c.Sugared && c.Name == "List.Nil" {
			return elems, nil, len(elems) > 0
		}
		head, args, isCons := sugaredCtorApp(cur, "List.Cons", 2)
		if !isCons {
			if len(elems) == 0 {
				return nil, nil, false
			}
			return elems, cur, true
		}
		_ = head
		elems = append(elems, args[0])
		cur = args[1]
	}
}

// asTuple recovers `(a, b)` and `(a, b, c)`.
func asTuple(e ast.Expr) ([]ast.Expr, bool) {
	if _, args, ok := sugaredCtorApp(e, "Tuple.Pair", 2); ok {
		return args, true
	}
	if _, args, ok := sugaredCtorApp(e, "Tuple.Triple", 3); ok {
		return args, true
	}
	return nil, false
}

// sugaredCtorApp matches a parser-generated constructor applied to exactly
// arity arguments.
func sugaredCtorApp(e ast.Expr, name string, arity int) (*ast.Ctor, []ast.Expr, bool) {
	head, args := spine(e)
	c, ok := head.(*ast.Ctor)
	if !ok || !c.Sugared || c.Name != name || len(args) != arity {
		return nil, nil, false
	}
	return c, args, true
}

func listInline(elems []ast.Expr, tail ast.Expr) (string, bool) {
	parts := make([]string, len(elems))
	for i, el := range elems {
		s, ok := exprInline(el)
		if !ok {
			return "", false
		}
		parts[i] = s
	}
	body := strings.Join(parts, ", ")
	if tail == nil {
		return "[" + body + "]", true
	}
	t, ok := exprInline(tail)
	if !ok {
		return "", false
	}
	return "[" + body + " | " + t + "]", true
}

func tupleInline(elems []ast.Expr) (string, bool) {
	parts := make([]string, len(elems))
	for i, el := range elems {
		s, ok := exprInline(el)
		if !ok {
			return "", false
		}
		parts[i] = s
	}
	return "(" + strings.Join(parts, ", ") + ")", true
}

// recordAfter wraps an inferred record literal in parentheses when it would
// follow an uppercase name, because a field list there opens a named record:
// `Just { x = 1 }` is a literal of nominal type Just rather than Just applied
// to an inferred record.
// After anything else — a lowercase name, a closing bracket, an operator — the
// braces delimit the record on their own and the parentheses are noise.
func recordAfter(prev, s string) string {
	if !strings.HasPrefix(s, "{") {
		return s
	}
	if !endsWithUpperName(prev) {
		return s
	}
	return "(" + s + ")"
}

// endsWithUpperName reports whether text ends in an identifier whose last
// segment starts with an uppercase letter, which is what a brace binds to.
func endsWithUpperName(text string) bool {
	i := len(text)
	for i > 0 && isNameByte(text[i-1]) {
		i--
	}
	if i == len(text) {
		return false
	}
	name := text[i:]
	if dot := strings.LastIndexByte(name, '.'); dot >= 0 {
		name = name[dot+1:]
	}
	return name != "" && name[0] >= 'A' && name[0] <= 'Z'
}

func isNameByte(b byte) bool {
	return b == '_' || b == '.' ||
		(b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}
