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
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/elaborate"
	"github.com/waj/fango/internal/eval"
	"github.com/waj/fango/internal/infer"
	"github.com/waj/fango/internal/lexer"
	"github.com/waj/fango/internal/parser"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/staging"
	"github.com/waj/fango/internal/token"
	"github.com/waj/fango/internal/types"
)

type Session struct {
	ck    *infer.Checker
	env   *eval.Env
	gen   int // generation counter incremented on redefinition
	out   io.Writer
	ioctx *eval.IOContext
}

func NewSession(out io.Writer) *Session {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	ck := infer.NewChecker(sup, b, infer.NewEnv())
	staging.Install(ck)
	if errs := ck.InstallPrelude(); len(errs) > 0 {
		panic("invalid embedded prelude: " + errs[0].Body)
	}
	// Prompt values are lazy memo cells (doc/design.md, "Interpreter and REPL") — evaluated once, so their
	// types stay monotypes (the block-binding monomorphism restriction).
	// Functions and lambdas still generalize.
	ck.MonoValues = true
	prelude, errs := elaborate.Module(nil, ck)
	if len(errs) > 0 {
		panic("invalid elaborated prelude: " + errs[0].Body)
	}
	env := eval.NewEnv()
	env.DefineProg(prelude)
	return &Session{
		ck:    ck,
		env:   env,
		out:   out,
		ioctx: eval.NewIOContext(strings.NewReader(""), out),
	}
}

const banner = "fango 0.1 — :help for commands"

// Run drives the read-eval-print loop until :quit or EOF.
//
// Multi-line policy (doc/design.md, "Interpreter and REPL": input continues while the layout stack
// is open): a first line that parses incomplete opens continuation mode;
// indented lines then accumulate WITHOUT re-submitting on the first complete
// parse — a `case` may grow another branch, a `type` another `|` line. A
// blank line, a column-1 line (necessarily a new declaration or expression),
// or EOF submits the buffer.
func Run(in io.Reader, out io.Writer) {
	s := NewSession(out)
	fmt.Fprintln(out, banner)
	reader := bufio.NewReader(in)
	s.ioctx = &eval.IOContext{Reader: reader, Writer: out}
	var buf strings.Builder
	flush := func() {
		if buf.Len() > 0 {
			s.submit(buf.String())
			buf.Reset()
		}
	}
	for {
		if buf.Len() == 0 {
			fmt.Fprint(out, "> ")
		} else {
			fmt.Fprint(out, "| ")
		}
		line, err := reader.ReadString('\n')
		if err != nil && len(line) == 0 {
			fmt.Fprintln(out)
			flush()
			return
		}
		line = strings.TrimSuffix(line, "\n")
		line = strings.TrimSuffix(line, "\r")
		trimmed := strings.TrimSpace(line)

		if buf.Len() > 0 {
			if trimmed == "" {
				flush()
				continue
			}
			// Indented lines always continue the construct. A column-1 line
			// continues it only while the buffer still parses incomplete —
			// the `name : T` / `name = …` annotation pair is the one
			// construct spanning column-1 lines; a complete buffer means
			// this line starts something new.
			if line[0] == ' ' || !s.parsesComplete(buf.String()) {
				buf.WriteByte('\n')
				buf.WriteString(line)
				continue
			}
			flush()
		}

		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, ":") {
			if quit := s.command(trimmed); quit {
				return
			}
			continue
		}
		if s.input(line, false) == needMoreInput {
			buf.WriteString(line)
		}
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

// submit finalizes an accumulated multi-line input: run-out-of-input errors
// render like any other (there is no more input coming).
func (s *Session) submit(text string) {
	s.input(text, true)
}

// parsesComplete reports whether the buffered input parses without running
// out of input — a syntax-only probe (no type checking, no evaluation).
func (s *Session) parsesComplete(text string) bool {
	f := source.NewFile("<repl>", []byte(text))
	toks, lexErrs := lexer.Lex(f)
	if len(lexErrs) > 0 {
		return true // hopeless input: let flush render it
	}
	var errs []diag.Error
	if isDecl(toks) {
		_, errs = parser.Parse(toks, f)
	} else {
		_, errs = parser.ParseExprInput(toks, f)
	}
	return !wantsMore(errs)
}

// input handles one declaration or expression. Unless force is set, a parse
// that failed only by running out of input reports needMoreInput instead of
// rendering errors — the continuation signal.
func (s *Session) input(text string, force bool) inputResult {
	f := source.NewFile("<repl>", []byte(text))
	toks, lexErrs := lexer.Lex(f)
	if len(lexErrs) > 0 {
		diag.Render(s.out, lexErrs)
		return inputDone
	}
	if isDecl(toks) {
		return s.declInput(toks, f, force)
	}
	return s.exprInput(toks, f, force)
}

// isDecl: `type …`, `name = …`, `name params… = …`, or `name : …` (an
// annotation opening a definition) is a declaration; anything else is an
// expression. `==` lexes as its own token, so comparisons still classify as
// expressions, and `f x y` without `=` stays an application.
func isDecl(toks []token.Token) bool {
	if len(toks) >= 1 && (toks[0].Kind == token.KwType || toks[0].Kind == token.KwEffect || toks[0].Kind == token.KwClass || toks[0].Kind == token.KwInstance || toks[0].Kind == token.KwDeriver) {
		return true
	}
	if len(toks) < 2 || toks[0].Kind != token.LIDENT {
		return false
	}
	if toks[1].Kind == token.COLON {
		return true
	}
	i := 1
	if i+1 < len(toks) && toks[i].Kind == token.LPAREN && toks[i+1].Kind == token.RPAREN {
		i += 2
	}
	for i < len(toks) && (toks[i].Kind == token.LIDENT || toks[i].Kind == token.UNDERSCORE) {
		i++
	}
	return i < len(toks) && toks[i].Kind == token.EQ
}

func (s *Session) declInput(toks []token.Token, f *source.File, force bool) inputResult {
	m, errs := parser.Parse(toks, f)
	if !force && wantsMore(errs) {
		return needMoreInput
	}
	if len(errs) > 0 {
		diag.Render(s.out, errs)
		return inputDone
	}
	if cl, ok := m.Decls[0].(*ast.ClassDecl); ok {
		if errs := s.ck.ClassDecl(cl); len(errs) > 0 {
			diag.Render(s.out, errs)
		} else {
			fmt.Fprintf(s.out, "%s : class\n", cl.Name)
		}
		return inputDone
	}
	if dr, ok := m.Decls[0].(*ast.DeriverDecl); ok {
		// A deriver is compile-time-only, so nothing installs into the
		// evaluation environment: the compile-time evaluator elaborates it
		// from the checked prefix when a `deriving` clause first runs it.
		rollback := s.ck.Checkpoint()
		if errs := s.ck.DeriverDecl(dr); len(errs) > 0 {
			rollback()
			diag.Render(s.out, errs)
		} else {
			fmt.Fprintf(s.out, "deriver %s\n", types.SurfaceName(dr.Class))
		}
		return inputDone
	}
	if in, ok := m.Decls[0].(*ast.InstanceDecl); ok {
		rollback := s.ck.Checkpoint()
		start := len(s.ck.Instances)
		var errs []diag.Error
		for _, method := range in.Methods {
			errs = append(errs, s.ck.StageDecl(method)...)
		}
		if len(errs) > 0 {
			rollback()
			diag.Render(s.out, errs)
			return inputDone
		}
		infos, errs := s.ck.InstanceDecl(in)
		if len(errs) == 0 {
			errs = s.installInstances(infos, start)
		}
		if len(errs) > 0 {
			rollback()
			diag.Render(s.out, errs)
		} else {
			fmt.Fprintf(s.out, "instance %s %s\n", types.SurfaceName(in.Head.Class), ast.DumpTypeExpr(in.Head.Ty))
		}
		return inputDone
	}
	if td, ok := m.Decls[0].(*ast.TypeDecl); ok {
		return s.typeDeclInput(td)
	}
	if ed, ok := m.Decls[0].(*ast.EffectDecl); ok {
		if errs := s.ck.EffectDecl(ed); len(errs) > 0 {
			diag.Render(s.out, errs)
		} else {
			fmt.Fprintf(s.out, "%s : effect\n", ed.Name)
		}
		return inputDone
	}
	vd := m.Decls[0].(*ast.ValueDecl)
	if vd.Native != nil {
		diag.Render(s.out, []diag.Error{diag.Errorf(vd.Native.Sp, "NATIVE MODULE REQUIRED", "Native declarations belong in source modules with a sidecar and cannot be entered directly at the REPL.")})
		return inputDone
	}
	redefining := s.ck.Env.Has(vd.Name)
	// A failed input leaves nothing behind, expansion included: a splice that
	// fails half way through has already checked whatever preceded it.
	rollback := s.ck.Checkpoint()
	if stageErrs := s.ck.StageDecl(vd); len(stageErrs) > 0 {
		rollback()
		diag.Render(s.out, stageErrs)
		return inputDone
	}
	// Check the body BEFORE binding: a failed definition must not install
	// a broken name into the session. REPL declarations are required to be
	// pure; effectful expressions can be evaluated directly at the prompt.
	info, inferErrs := s.ck.DeclWhere(vd, false)
	if len(inferErrs) > 0 {
		rollback()
		diag.Render(s.out, inferErrs)
		return inputDone
	}
	// The elaborator's spine analysis needs the worker table to include
	// THIS definition (a prompt-defined fib must self-call directly), so
	// install its arity before elaborating; the checkpoint above restores it
	// on failure along with everything else.
	if len(vd.Params) > 0 {
		s.ck.Workers[vd.Name] = len(vd.Params)
	} else {
		delete(s.ck.Workers, vd.Name)
	}
	defs, elabErrs := elaborate.Decl(info, s.ck)
	if len(elabErrs) > 0 {
		rollback()
		diag.Render(s.out, elabErrs)
		return inputDone
	}
	s.ck.BindDecl(info)
	// A later splice may name this definition, so the compile-time
	// evaluator's prefix has to grow with the session.
	s.ck.Checked = append(s.ck.Checked, info)
	def := &defs[0]
	for i := range defs[1:] {
		s.env.DefineWorker(&defs[1+i]) // lambda-lifted locals (doc/design.md, "Go backend and runtime")
	}
	if def.IsWorker() {
		s.env.DefineWorker(def)
	} else {
		s.env.Define(def.Name, def.Body)
	}
	if redefining {
		s.gen++
	}
	sch := info.Scheme
	sch.Body = s.ck.Sub.Apply(sch.Body)
	sch.Preds = s.ck.NormalizePreds(sch.Preds)
	fmt.Fprintf(s.out, "%s : %s\n", def.Name, types.ShowScheme(sch))
	return inputDone
}

// typeDeclInput installs a `type` declaration. Redefinition mints a fresh
// generation (a new Unique), exactly like value redefinition (doc/design.md, "Interpreter and REPL").
func (s *Session) typeDeclInput(td *ast.TypeDecl) inputResult {
	rollback := s.ck.Checkpoint()
	start := len(s.ck.Instances)
	_, redefining := s.ck.TypeNames[td.Name]
	if errs := s.ck.TypeDecl(td); len(errs) > 0 {
		rollback()
		diag.Render(s.out, errs)
		return inputDone
	}
	infos, errs := s.ck.DeriveDecl(td)
	if len(errs) == 0 {
		errs = s.installInstances(infos, start)
	}
	if len(errs) > 0 {
		rollback()
		diag.Render(s.out, errs)
		return inputDone
	}
	if redefining {
		s.gen++
	}
	// Echo each constructor with its type, mirroring the `name : type` shape
	// value definitions print.
	adt := s.ck.ADTs[s.ck.TypeNames[td.Name].(*types.TCon).Unique]
	if adt.IsRecord() {
		fmt.Fprintf(s.out, "%s : record\n", td.Name)
		return inputDone
	}
	for _, c := range adt.Ctors {
		fmt.Fprintf(s.out, "%s : %s\n", c.Name, types.Show(c.ValueType()))
	}
	return inputDone
}

func (s *Session) exprInput(toks []token.Token, f *source.File, force bool) inputResult {
	e, errs := parser.ParseExprInput(toks, f)
	if !force && wantsMore(errs) {
		return needMoreInput
	}
	if len(errs) > 0 {
		diag.Render(s.out, errs)
		return inputDone
	}
	e, errs = s.ck.StageExpr(e)
	if len(errs) > 0 {
		diag.Render(s.out, errs)
		return inputDone
	}
	ty, inferErrs := s.ck.Expr(e)
	if len(inferErrs) > 0 {
		diag.Render(s.out, inferErrs)
		return inputDone
	}
	// The displayed type is the pre-defaulting one — free variables print
	// as the generalized scheme would (`\x -> x` echoes `a -> a`), doc/design.md, "Interpreter and REPL".
	// Elaboration then defaults for evaluation; the value renders at the
	// defaulted (ground) type.
	shownTy := types.ShowScheme(types.Scheme{Body: s.ck.Sub.Apply(ty), Preds: s.ck.PendingPreds})
	coreExpr, aux, elabErrs := elaborate.Expr(e, s.ck)
	if len(elabErrs) > 0 {
		diag.Render(s.out, elabErrs)
		return inputDone
	}
	for i := range aux {
		s.env.DefineWorker(&aux[i])
	}
	v, err := eval.EvalIO(context.Background(), elaborate.Display(coreExpr, s.ck, ""), s.env, s.ioctx)
	if err != nil {
		fmt.Fprintf(s.out, "runtime error: %v\n", err)
		return inputDone
	}
	fmt.Fprintf(s.out, "%s : %s\n", v.(string), shownTy)
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
	// Show the generalized view: free variables print as the scheme would
	// (`:type \x -> x` says `a -> a`), no defaulting forced (doc/design.md, "Interpreter and REPL").
	fmt.Fprintln(s.out, types.ShowScheme(types.Scheme{Body: s.ck.Sub.Apply(ty), Preds: s.ck.PendingPreds}))
}

func (s *Session) installInstances(infos []infer.DeclInfo, start int) []diag.Error {
	defs, errs := elaborate.Instances(s.ck.Instances[start:], s.ck)
	for _, info := range infos {
		ds, es := elaborate.Decl(info, s.ck)
		defs = append(defs, ds...)
		errs = append(errs, es...)
	}
	if len(errs) > 0 {
		return errs
	}
	s.env.DefineProg(&core.Prog{Defs: defs})
	return nil
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
