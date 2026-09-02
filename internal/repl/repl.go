// Package repl is the interactive session: one Checker, one substitution,
// one supply, and one cell environment shared across inputs. Expressions
// evaluate through the same Core the compiler consumes; definitions install
// lazy memo cells. Every input's errors are recovered — no input kills the
// session.
package repl

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/elaborate"
	"github.com/waj/fango/internal/eval"
	"github.com/waj/fango/internal/infer"
	"github.com/waj/fango/internal/lexer"
	"github.com/waj/fango/internal/parser"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/token"
	"github.com/waj/fango/internal/types"
)

type Session struct {
	ck  *infer.Checker
	env *eval.Env
	gen int // generation counter: incremented on redefinition (plumbing for S6)
	out io.Writer
}

func NewSession(out io.Writer) *Session {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	return &Session{
		ck:  infer.NewChecker(sup, b, infer.NewEnv()),
		env: eval.NewEnv(),
		out: out,
	}
}

const banner = "fango 0.1 — :help for commands"

// Run drives the read-eval-print loop until :quit or EOF.
func Run(in io.Reader, out io.Writer) {
	s := NewSession(out)
	fmt.Fprintln(out, banner)
	scanner := bufio.NewScanner(in)
	var buf strings.Builder
	for {
		if buf.Len() == 0 {
			fmt.Fprint(out, "> ")
		} else {
			fmt.Fprint(out, "| ")
		}
		if !scanner.Scan() {
			fmt.Fprintln(out)
			return
		}
		line := scanner.Text()
		if buf.Len() == 0 && strings.TrimSpace(line) == "" {
			continue
		}
		if buf.Len() > 0 {
			buf.WriteByte('\n')
		}
		buf.WriteString(line)
		input := buf.String()

		if buf.Len() == len(line) && strings.HasPrefix(strings.TrimSpace(input), ":") {
			buf.Reset()
			if quit := s.command(strings.TrimSpace(input)); quit {
				return
			}
			continue
		}
		if s.input(input) == needMoreInput {
			continue // keep buf, show the continuation prompt
		}
		buf.Reset()
	}
}

type inputResult int

const (
	inputDone inputResult = iota
	needMoreInput
)

func (s *Session) command(cmd string) (quit bool) {
	switch {
	case cmd == ":quit" || cmd == ":q":
		return true
	case cmd == ":help":
		fmt.Fprint(s.out, `commands:
  :type <expr>   show an expression's type without evaluating
  :help          this message
  :quit          leave the REPL (also Ctrl-D)
`)
	case strings.HasPrefix(cmd, ":type "):
		s.typeOf(strings.TrimPrefix(cmd, ":type "))
	default:
		fmt.Fprintf(s.out, "unknown command %s — :help lists the commands\n", cmd)
	}
	return false
}

// input handles one (possibly still growing) declaration or expression.
func (s *Session) input(text string) inputResult {
	f := source.NewFile("<repl>", []byte(text))
	toks, lexErrs := lexer.Lex(f)
	if len(lexErrs) > 0 {
		diag.Render(s.out, lexErrs)
		return inputDone
	}
	if isDecl(toks) {
		return s.declInput(toks, f)
	}
	return s.exprInput(toks, f)
}

// isDecl: `name = …`, `name params… = …`, or `name : …` (an annotation
// opening a definition) is a declaration; anything else is an expression.
// `==` lexes as its own token, so comparisons still classify as
// expressions, and `f x y` without `=` stays an application.
func isDecl(toks []token.Token) bool {
	if len(toks) < 2 || toks[0].Kind != token.LIDENT {
		return false
	}
	if toks[1].Kind == token.COLON {
		return true
	}
	i := 1
	for i < len(toks) && toks[i].Kind == token.LIDENT {
		i++
	}
	return i < len(toks) && toks[i].Kind == token.EQ
}

func (s *Session) declInput(toks []token.Token, f *source.File) inputResult {
	m, errs := parser.Parse(toks, f)
	if wantsMore(errs) {
		return needMoreInput
	}
	if len(errs) > 0 {
		diag.Render(s.out, errs)
		return inputDone
	}
	vd := m.Decls[0].(*ast.ValueDecl)
	redefining := s.ck.Env.Has(vd.Name)
	// Check the body BEFORE binding: a failed definition must not install
	// a broken name into the session. REPL declarations never allow the
	// print cheat — evaluate the expression at the prompt instead.
	info, inferErrs := s.ck.DeclWhere(vd, false)
	if len(inferErrs) > 0 {
		diag.Render(s.out, inferErrs)
		return inputDone
	}
	// The elaborator's spine analysis needs the worker table to include
	// THIS definition (a prompt-defined fib must self-call directly), so
	// install its arity before elaborating — and roll back on failure.
	prevArity, hadWorker := s.ck.Workers[vd.Name]
	if len(vd.Params) > 0 {
		s.ck.Workers[vd.Name] = len(vd.Params)
	} else {
		delete(s.ck.Workers, vd.Name)
	}
	def, elabErrs := elaborate.Decl(info, s.ck)
	if len(elabErrs) > 0 {
		if hadWorker {
			s.ck.Workers[vd.Name] = prevArity
		} else {
			delete(s.ck.Workers, vd.Name)
		}
		diag.Render(s.out, elabErrs)
		return inputDone
	}
	s.ck.BindDecl(info)
	if len(def.Params) > 0 {
		s.env.DefineWorker(&def)
	} else {
		s.env.Define(def.Name, def.Body)
	}
	if redefining {
		s.gen++
	}
	fmt.Fprintf(s.out, "%s : %s\n", def.Name, types.Show(def.Type))
	return inputDone
}

func (s *Session) exprInput(toks []token.Token, f *source.File) inputResult {
	e, errs := parser.ParseExprInput(toks, f)
	if wantsMore(errs) {
		return needMoreInput
	}
	if len(errs) > 0 {
		diag.Render(s.out, errs)
		return inputDone
	}
	ty, inferErrs := s.ck.Expr(e)
	if len(inferErrs) > 0 {
		diag.Render(s.out, inferErrs)
		return inputDone
	}
	if s.rejectPoly(e, ty) {
		return inputDone
	}
	coreExpr, elabErrs := elaborate.Expr(e, s.ck)
	if len(elabErrs) > 0 {
		diag.Render(s.out, elabErrs)
		return inputDone
	}
	v, err := eval.Eval(context.Background(), coreExpr, s.env, s.out)
	if err != nil {
		fmt.Fprintf(s.out, "runtime error: %v\n", err)
		return inputDone
	}
	finalTy := s.ck.Sub.Apply(ty)
	fmt.Fprintf(s.out, "%s : %s\n", eval.Show(v, finalTy, s.ck.B), types.Show(finalTy))
	return inputDone
}

func (s *Session) typeOf(src string) {
	f := source.NewFile("<repl>", []byte(src))
	toks, lexErrs := lexer.Lex(f)
	if len(lexErrs) > 0 {
		diag.Render(s.out, lexErrs)
		return
	}
	e, errs := parser.ParseExprInput(toks, f)
	if len(errs) > 0 {
		diag.Render(s.out, errs)
		return
	}
	ty, inferErrs := s.ck.Expr(e)
	if len(inferErrs) > 0 {
		diag.Render(s.out, inferErrs)
		return
	}
	if s.rejectPoly(e, ty) {
		return
	}
	elaborate.Expr(e, s.ck) // force defaulting so the shown type is ground
	fmt.Fprintln(s.out, types.Show(s.ck.Sub.Apply(ty)))
}

// rejectPoly rejects a prompt expression whose type is still visibly
// polymorphic — the REPL elaborates per input, so unlike batch there is no
// later use to pin it. Reports whether the input was rejected.
func (s *Session) rejectPoly(e ast.Expr, ty types.Type) bool {
	if s.ck.FreeGeneralVar(ty) == nil {
		return false
	}
	p := types.NewPrinter()
	diag.Render(s.out, []diag.Error{diag.Errorf(e.Span(), "UNSUPPORTED POLYMORPHISM",
		"This expression's type is:\n\n    %s\n\nType variables mean polymorphism, which arrives in S5. Apply it to\nconcrete arguments instead.", p.Type(s.ck.Sub.Apply(ty)))})
	return true
}

// wantsMore reports whether the parse failed only because input ran out —
// the multi-line continuation signal.
func wantsMore(errs []diag.Error) bool {
	for _, e := range errs {
		if e.Title == parser.TitleUnexpectedEOF {
			return true
		}
	}
	return false
}
