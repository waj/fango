package infer

// The compile-time stage. `quote` goes up a stage and `$(…)` comes back
// down, and the two are the entire staging surface (doc/design.md,
// "Compile-time metaprogramming").
//
// Two positions are tracked independently rather than as one signed depth,
// because they answer different questions. `quoted` counts enclosing quote
// bodies: inside one, a `$(…)` is a hole. `stage` counts enclosing splice
// operands: at 0 the code runs when the program runs, at 1 it runs while the
// compiler is running. Each is permitted to reach exactly 1 — a nested quote
// and a second compile-time stage are both rejected.
//
// Staging runs before a declaration is checked, so by the time inference sees
// a body every splice has already been replaced by the code it produced.

import (
	"errors"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/eval"
	"github.com/waj/fango/internal/meta"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/types"
)

// CodeTypeName is the canonical symbol of the bundled abstract code type.
const CodeTypeName = "Meta.Code"

// The reflection surface: an opaque nominal identity, the schema record a
// deriver walks, and the compiler-only projection from one to the other.
const (
	TypeReprName = "Meta.TypeRepr"
	TypeInfoName = "Meta.TypeInfo"
	InfoOfName   = "Meta.infoOf"
)

// Reflect builds the compile-time value of a reflected type. visible names
// the schemas the reflection site may read, as module resolution recorded
// them; nil means every schema is readable, which is the REPL and a
// headerless file, neither of which has an export boundary to respect.
func (ck *Checker) Reflect(t types.Type, visible map[string]bool) *meta.TypeRepr {
	repr := &meta.TypeRepr{Type: t, Schema: ck}
	if visible == nil {
		return repr
	}
	repr.Visible = map[int]bool{}
	for name := range visible {
		if con, ok := ck.TypeNames[name].(*types.TCon); ok {
			repr.Visible[con.Unique] = true
		}
	}
	return repr
}

// codeType returns the compile-time code type. A quote or splice can only
// appear in a module that pulled `Meta` in, which the module loader arranges
// for any file that uses the syntax, so a missing type is an internal error
// rather than a user mistake.
func (ck *Checker) codeType(sp source.Span) (types.Type, []diag.Error) {
	if t := ck.TypeNames[CodeTypeName]; t != nil {
		return t, nil
	}
	return ck.Sup.FreshVar(types.General), []diag.Error{diag.Errorf(sp, "STAGE ERROR",
		"I cannot find the bundled `Meta` module, so I cannot type this quote.")}
}

// IsCompileTimeOnly reports whether t mentions the code type. A definition
// whose type is compile-time-only is not emitted, and no runtime-reachable
// expression may have such a type — one rule, checked purely on types, that
// keeps code values out of generated Go without any reachability analysis.
func (ck *Checker) IsCompileTimeOnly(t types.Type) bool {
	if t == nil {
		return false
	}
	roots := map[int]bool{}
	for _, name := range []string{CodeTypeName, TypeReprName} {
		if con, ok := ck.TypeNames[name].(*types.TCon); ok {
			roots[con.Unique] = true
		}
	}
	var visit func(types.Type, map[int]bool) bool
	visit = func(t types.Type, seen map[int]bool) bool {
		switch t := t.(type) {
		case *types.TCon:
			if roots[t.Unique] {
				return true
			}
			for _, a := range t.Args {
				if visit(a, seen) {
					return true
				}
			}
			if seen[t.Unique] {
				return false
			}
			seen[t.Unique] = true
			if adt := ck.ADTs[t.Unique]; adt != nil {
				for _, c := range adt.Ctors {
					for _, f := range c.Fields {
						if visit(f, seen) {
							return true
						}
					}
				}
			}
		case *types.TFun:
			return visit(t.Arg, seen) || visit(t.Ret, seen)
		}
		return false
	}
	return visit(t, map[int]bool{})
}

func (ck *Checker) reflectionClosed(t types.Type) bool {
	switch t := t.(type) {
	case *types.TVar:
		return false
	case *types.TCon:
		if adt := ck.ADTs[t.Unique]; adt != nil && len(t.Args) != len(adt.Params) {
			return false
		}
		for _, arg := range t.Args {
			if !ck.reflectionClosed(arg) {
				return false
			}
		}
		return true
	case *types.TFun:
		if !ck.reflectionClosed(t.Arg) || !ck.reflectionClosed(t.Ret) || t.Eff.Tail != nil {
			return false
		}
		for _, label := range t.Eff.Labels {
			for _, arg := range label.Args {
				if !ck.reflectionClosed(arg) {
					return false
				}
			}
		}
		return true
	default:
		return false
	}
}

// checkStageLeaks enforces the other half of the compile-time-only rule. A
// definition whose own type mentions the code type is never emitted, so it
// may hold code freely. Any other definition may not: a code value in
// runtime-reachable code would have to survive into generated Go, where it
// has no meaning.
func (ck *Checker) checkStageLeaks(d *ast.ValueDecl, declType types.Type, name string) []diag.Error {
	if d.Body == nil {
		return nil
	}
	// An instance method has no type of its own to exempt it: its signature
	// comes from the class, so it is always runtime code.
	if declType != nil && ck.IsCompileTimeOnly(ck.Sub.Apply(declType)) {
		return nil
	}
	// One mistake, one message: report the first offending expression and
	// stop. A quote is named directly, because it is the cause rather than
	// a consequence of one.
	var quoted ast.Expr
	visitExpr(d.Body, func(n ast.Expr) bool {
		if _, ok := n.(*ast.Quote); ok && quoted == nil {
			quoted = n
		}
		return quoted == nil
	})
	if quoted != nil {
		return []diag.Error{diag.Errorf(quoted.Span(), "STAGE ERROR",
			"This quote builds compile-time code, but `%s` is an ordinary definition.\nOnly a definition whose own type is `Code` may hold a quote.", name)}
	}
	var leak ast.Expr
	var leakTy types.Type
	visitExpr(d.Body, func(n ast.Expr) bool {
		if leak != nil {
			return false
		}
		if ty := ck.ExprTypes[n]; ty != nil {
			if solved := ck.Sub.Apply(ty); ck.IsCompileTimeOnly(solved) {
				leak, leakTy = n, solved
				return false
			}
		}
		return true
	})
	if leak == nil {
		return nil
	}
	return []diag.Error{diag.Errorf(leak.Span(), "STAGE ERROR",
		"`%s` runs at run time, but this has the compile-time-only type:\n\n    %s\n\nCode exists only while the compiler is running.", name, types.Show(leakTy))}
}

// StageDecl validates and expands one declaration in place. Callers run it
// before checking the declaration; after it returns without errors the body
// contains no splices.
func (ck *Checker) StageDecl(d *ast.ValueDecl) []diag.Error {
	var errs []diag.Error
	if len(d.Equations) > 0 {
		for i := range d.Equations {
			eq := &d.Equations[i]
			if !usesStaging(eq.Body) {
				continue
			}
			if es := stageCheck(eq.Params, eq.Body); len(es) > 0 {
				errs = append(errs, es...)
				continue
			}
			eq.Body, _ = ck.stageExpand(eq.Body)
		}
		return errs
	}
	if d.Body == nil || !usesStaging(d.Body) {
		return nil
	}
	if errs := stageCheck(d.Params, d.Body); len(errs) > 0 {
		return errs
	}
	body, errs := ck.stageExpand(d.Body)
	d.Body = body
	return errs
}

// StageExpr validates and expands a bare expression — the REPL's prompt.
func (ck *Checker) StageExpr(e ast.Expr) (ast.Expr, []diag.Error) {
	if !usesStaging(e) {
		return e, nil
	}
	if errs := stageCheck(nil, e); len(errs) > 0 {
		return e, errs
	}
	return ck.stageExpand(e)
}

// usesStaging reports whether an expression mentions the staging syntax at
// all. It is the gate on the whole pass, so a program that does no
// metaprogramming pays one tree walk per declaration and nothing else.
func usesStaging(e ast.Expr) bool {
	found := false
	visitExpr(e, func(n ast.Expr) bool {
		switch n.(type) {
		case *ast.Quote, *ast.Splice:
			found = true
		}
		return !found
	})
	return found
}

// --- validation -------------------------------------------------------

type binderStage struct{ stage, quoted int }

type stageChecker struct {
	errs          []diag.Error
	binders       map[string]binderStage
	stage, quoted int
}

func stageCheck(params []ast.Pattern, e ast.Expr) []diag.Error {
	s := &stageChecker{binders: map[string]binderStage{}}
	restore := s.bind(paramNames(params))
	s.expr(e)
	restore()
	return s.errs
}

func paramNames(ps []ast.Pattern) []string {
	var out []string
	for _, p := range ps {
		out = patternNames(p, out)
	}
	return out
}

// bind introduces local binders at the current position and returns the undo,
// so sibling scopes that reuse a name do not see each other's entries.
func (s *stageChecker) bind(names []string) func() {
	type saved struct {
		name string
		prev binderStage
		had  bool
	}
	undo := make([]saved, 0, len(names))
	for _, n := range names {
		prev, had := s.binders[n]
		undo = append(undo, saved{n, prev, had})
		s.binders[n] = binderStage{stage: s.stage, quoted: s.quoted}
	}
	return func() {
		for _, u := range undo {
			if u.had {
				s.binders[u.name] = u.prev
			} else {
				delete(s.binders, u.name)
			}
		}
	}
}

func (s *stageChecker) errorf(sp source.Span, format string, args ...any) {
	s.errs = append(s.errs, diag.Errorf(sp, "STAGE ERROR", format, args...))
}

func (s *stageChecker) expr(e ast.Expr) {
	switch e := e.(type) {
	case nil:
		return
	case *ast.Quote:
		if s.quoted > 0 {
			s.errorf(e.Sp, "A quote cannot contain another quote; there is exactly one\nquoted stage.")
			return
		}
		s.quoted++
		s.expr(e.Body)
		s.quoted--
		return
	case *ast.Splice:
		if s.quoted > 0 {
			// A hole: it steps back out of the quote and is evaluated with
			// the quote itself, at the stage the quote was written at.
			s.quoted--
			s.expr(e.Operand)
			s.quoted++
			return
		}
		if s.stage > 0 {
			s.errorf(e.Sp, "A splice cannot contain another splice; there is exactly one\ncompile-time stage.")
			return
		}
		s.stage++
		s.expr(e.Operand)
		s.stage--
		return
	case *ast.Var:
		if b, ok := s.binders[e.Name]; ok && (b.stage != s.stage || b.quoted != s.quoted) {
			s.errorf(e.Sp, "`%s` is a local binding of another stage, so it does not exist here.\n"+
				"Only top-level definitions are available at both stages; pass a runtime\nvalue by applying the generated code to it.", e.Name)
		}
		return
	case *ast.Lambda:
		restore := s.bind(paramNames(e.Params))
		s.expr(e.Body)
		restore()
		return
	case *ast.Block:
		var undo []func()
		for i := range e.Binds {
			b := &e.Binds[i]
			if b.Pattern != nil {
				s.expr(b.Body)
				undo = append(undo, s.bind(patternNames(b.Pattern, nil)))
				continue
			}
			if len(b.Equations) > 0 {
				for _, eq := range b.Equations {
					restore := s.bind(append([]string{b.Name}, paramNames(eq.Params)...))
					s.expr(eq.Body)
					restore()
				}
				undo = append(undo, s.bind([]string{b.Name}))
				continue
			}
			inner := s.bind(paramNames(b.Params))
			if len(b.Params) > 0 {
				inner2 := s.bind([]string{b.Name})
				s.expr(b.Body)
				inner2()
			} else {
				s.expr(b.Body)
			}
			inner()
			undo = append(undo, s.bind([]string{b.Name}))
		}
		for _, it := range e.Items {
			s.expr(it.Expr)
		}
		s.expr(e.Result)
		for i := len(undo) - 1; i >= 0; i-- {
			undo[i]()
		}
		return
	case *ast.Case:
		s.expr(e.Scrutinee)
		for _, br := range e.Branches {
			restore := s.bind(patternNames(br.Pattern, nil))
			s.expr(br.Body)
			restore()
		}
		return
	case *ast.Handle:
		s.expr(e.Body)
		for _, c := range e.Clauses {
			if len(c.Equations) > 0 {
				for _, eq := range c.Equations {
					restore := s.bind(paramNames(eq.Params))
					s.expr(eq.Body)
					restore()
				}
			} else {
				restore := s.bind(paramNames(c.Params))
				s.expr(c.Body)
				restore()
			}
		}
		if e.Return != nil {
			if len(e.Return.Equations) > 0 {
				for _, eq := range e.Return.Equations {
					restore := s.bind(paramNames(eq.Params))
					s.expr(eq.Body)
					restore()
				}
			} else {
				restore := s.bind(patternNames(e.Return.Param, nil))
				s.expr(e.Return.Body)
				restore()
			}
		}
		return
	}
	visitChildren(e, s.expr)
}

func patternNames(p ast.Pattern, out []string) []string {
	switch p := p.(type) {
	case *ast.PVar:
		out = append(out, p.Name)
	case *ast.PCtor:
		for _, a := range p.Args {
			out = patternNames(a, out)
		}
	case *ast.PRecord:
		for _, f := range p.Fields {
			out = patternNames(f.Pattern, out)
		}
	}
	return out
}

// --- expansion --------------------------------------------------------

type stageExpander struct {
	ck            *Checker
	errs          []diag.Error
	stage, quoted int
}

func (ck *Checker) stageExpand(e ast.Expr) (ast.Expr, []diag.Error) {
	x := &stageExpander{ck: ck}
	return x.expr(e), x.errs
}

// expr copies e, registering every quote it passes and replacing every
// depth-0 splice with the code that splice produced. Copying is not
// incidental: inference keys solved types by node pointer, so two splices of
// one template must not share nodes.
func (x *stageExpander) expr(e ast.Expr) ast.Expr {
	return meta.Rewrite(e, func(n ast.Expr) ast.Expr {
		switch n := n.(type) {
		case *ast.Quote:
			return x.quote(n)
		case *ast.Splice:
			return x.splice(n)
		}
		return nil
	})
}

// quote copies the quoted body and records it as a template. Holes are the
// `$(…)` nodes the body reaches without passing through another quote; the
// walk stops at each one, so a splice nested inside a hole's operand is an
// ordinary splice of the enclosing stage and is expanded by x.expr.
func (x *stageExpander) quote(q *ast.Quote) ast.Expr {
	if x.quoted > 0 {
		return meta.Rewrite(q, func(ast.Expr) ast.Expr { return nil }) // already reported
	}
	x.quoted++
	var holes []*ast.Splice
	body := meta.Rewrite(q.Body, func(n ast.Expr) ast.Expr {
		s, ok := n.(*ast.Splice)
		if !ok {
			return nil
		}
		x.quoted--
		hole := &ast.Splice{Operand: x.expr(s.Operand), Sp: s.Sp}
		x.quoted++
		holes = append(holes, hole)
		return hole
	})
	x.quoted--
	out := &ast.Quote{Body: body, Sp: q.Sp}
	x.ck.QuoteTemplates[out] = x.ck.Templates.Add(&meta.Template{Body: body, Holes: holes})
	x.ck.QuoteHoles[out] = holes
	return out
}

func (x *stageExpander) splice(s *ast.Splice) ast.Expr {
	if x.stage > 0 {
		return meta.Rewrite(s, func(ast.Expr) ast.Expr { return nil }) // already reported
	}
	x.stage++
	operand := x.expr(s.Operand)
	x.stage--

	code, errs := x.ck.runSplice(operand, s.Sp)
	x.errs = append(x.errs, errs...)
	if code == nil {
		// Leave the splice in place. Inference gives an unexpanded splice a
		// fresh type without complaining, so the rest of the declaration
		// still reports its own problems instead of a cascade about this one.
		return &ast.Splice{Operand: operand, Sp: s.Sp}
	}
	out := x.ck.Templates.Expand(code)
	if out == nil {
		x.errs = append(x.errs, diag.Errorf(s.Sp, "STAGE ERROR",
			"This splice produced code I cannot expand."))
		return &ast.Splice{Operand: operand, Sp: s.Sp}
	}
	return out
}

// runSplice checks the operand, insists it is pure code, and evaluates it.
func (ck *Checker) runSplice(operand ast.Expr, sp source.Span) (*meta.Code, []diag.Error) {
	code, codeErrs := ck.codeType(sp)
	if len(codeErrs) > 0 {
		return nil, codeErrs
	}
	ty, errs := ck.ExprWhere(operand, false)
	if len(errs) > 0 {
		return nil, errs
	}
	sub, _, solveErrs := Solve([]Constraint{{Left: ty, Right: code, Span: sp, Why: Why{Kind: WhySpliceOperand}}}, nil, ck.Sub, ck.B, ck.Sup)
	ck.Sub = sub
	if len(solveErrs) > 0 {
		return nil, solveErrs
	}
	if ck.CompileTime == nil {
		return nil, []diag.Error{diag.Errorf(sp, "STAGE ERROR",
			"Compile-time evaluation is not available here.")}
	}
	value, evalErrs := ck.CompileTime(operand)
	if len(evalErrs) > 0 {
		return nil, evalErrs
	}
	c, ok := value.(*meta.Code)
	if !ok {
		return nil, []diag.Error{diag.Errorf(sp, "STAGE ERROR",
			"This splice did not produce code.")}
	}
	return c, nil
}

// CompileTimeError turns an interpreter failure into the diagnostic the
// splice site deserves.
func CompileTimeError(err error, sp source.Span) diag.Error {
	var unsafe *eval.UnsafeNativeError
	switch {
	case errors.Is(err, eval.ErrStepBudget):
		return diag.Errorf(sp, "COMPILE-TIME LIMIT",
			"This splice ran for %d evaluation steps without finishing.\nCompile-time code is bounded so a build cannot hang.", eval.DefaultBudget)
	case errors.As(err, &unsafe):
		return diag.Errorf(sp, "COMPILE-TIME NATIVE",
			"Compile-time code runs inside the compiler, so it cannot call\n`%s`, which %s.", unsafe.Name, unsafe.Reason)
	default:
		return diag.Errorf(sp, "COMPILE-TIME FAILURE",
			"This splice failed while the compiler was running it:\n\n    %v", err)
	}
}

// --- generic expression traversal -------------------------------------

// visitExpr walks every expression node, including quoted bodies, stopping a
// subtree when visit returns false.
func visitExpr(e ast.Expr, visit func(ast.Expr) bool) {
	if e == nil || !visit(e) {
		return
	}
	visitChildren(e, func(child ast.Expr) { visitExpr(child, visit) })
}

// visitChildren calls f on each immediate expression child.
func visitChildren(e ast.Expr, f func(ast.Expr)) {
	each := func(children ...ast.Expr) {
		for _, c := range children {
			if c != nil {
				f(c)
			}
		}
	}
	switch e := e.(type) {
	case *ast.RecordLit:
		for _, field := range e.Fields {
			each(field.Value)
		}
	case *ast.RecordGet:
		each(e.Record)
	case *ast.RecordUpdate:
		each(e.Record)
		for _, field := range e.Fields {
			each(field.Value)
		}
	case *ast.App:
		each(e.Fn, e.Arg)
	case *ast.Neg:
		each(e.Operand)
	case *ast.BinOp:
		each(e.L, e.R)
	case *ast.If:
		each(e.Cond, e.Then, e.Else)
	case *ast.Lambda:
		each(e.Body)
	case *ast.Block:
		for i := range e.Binds {
			each(e.Binds[i].Body)
			for _, eq := range e.Binds[i].Equations {
				each(eq.Body)
			}
		}
		for _, it := range e.Items {
			each(it.Expr)
		}
		each(e.Result)
	case *ast.Case:
		each(e.Scrutinee)
		for _, br := range e.Branches {
			each(br.Body)
		}
	case *ast.Handle:
		each(e.Body)
		for _, c := range e.Clauses {
			each(c.Body)
			for _, eq := range c.Equations {
				each(eq.Body)
			}
		}
		if e.Return != nil {
			each(e.Return.Body)
			for _, eq := range e.Return.Equations {
				each(eq.Body)
			}
		}
	case *ast.Quote:
		each(e.Body)
	case *ast.Splice:
		each(e.Operand)
	}
}
