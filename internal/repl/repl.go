// Package repl is the interactive session: one Checker, one substitution,
// one supply, one module graph with its prompt scope, and one cell
// environment shared across inputs. Expressions evaluate through the same
// Core the compiler consumes; definitions install lazy memo cells. Every
// input's errors are recovered — no input kills the session.
package repl

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/check"
	"github.com/waj/fango/internal/compilecache"
	"github.com/waj/fango/internal/compileevent"
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/elaborate"
	"github.com/waj/fango/internal/eval"
	"github.com/waj/fango/internal/infer"
	"github.com/waj/fango/internal/lexer"
	"github.com/waj/fango/internal/modules"
	"github.com/waj/fango/internal/nativehost"
	"github.com/waj/fango/internal/parser"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/staging"
	"github.com/waj/fango/internal/token"
	"github.com/waj/fango/internal/types"
)

// Options configures a session.
type Options struct {
	// ErrorWriter routes program stderr; nil uses os.Stderr.
	ErrorWriter io.Writer
	// Root is the source root a prompt `import Foo.Bar` resolves beneath as
	// `Foo/Bar.fango`, the way an entry file's directory is in a build.
	// Empty means the working directory.
	Root string
	// Observe reports per-module compilation stages, for tests that count
	// imported-module work apart from the prompt's own input.
	Observe check.Observer
	// Cache overrides the persistent module-object store. DisableCache
	// compiles without reusing or publishing artifacts.
	Cache        check.ObjectCache
	DisableCache bool
	// Interactive edits prompt and program lines with a terminal line
	// editor that reads the process's stdin in place of RunWith's reader;
	// the writer must be that terminal's stdout.
	Interactive bool
	// Color marks unhandled failures with terminal colors.
	Color bool
	// lineEditor replaces the terminal editor of an Interactive session.
	lineEditor func(out *tailWriter, cancel func()) lineEditor
}

func (o Options) objectCache(root string) check.ObjectCache {
	if o.Cache != nil || o.DisableCache {
		return o.Cache
	}
	return compilecache.NewModuleStore(filepath.Join(root, "<repl>"))
}

type Session struct {
	ck         *infer.Checker
	env        *eval.Env
	gen        int // generation counter incremented on redefinition
	out        io.Writer
	ioctx      *eval.IOContext
	evalExec   atomic.Pointer[nativehost.Executor]
	interrupts <-chan struct{}
	color      bool
	loading    *importProgress

	// levels are the handler levels installed with `use`, outermost first;
	// undo records what their names replaced.
	levels      []*level
	levelIO     *eval.ChannelLevels
	undo        []savedName
	levelsReady bool
	cancelMu    sync.Mutex
	cancelInput context.CancelFunc

	// graph holds every module the session has resolved, the bundled prelude
	// closure included; prompt is the resolver scope prompts and imports
	// extend (doc/design.md, "Interpreter and REPL").
	graph  *modules.Graph
	prompt *modules.Prompt

	// natives are the user sidecars imported so far, and exec the worker
	// built over them and the bundled ones. Nil exec means only bundled
	// sidecars have been needed, which the shared bundled worker serves.
	natives []nativehost.Source
	exec    *nativehost.Executor

	// modules installs imported modules through the shared compilation
	// session, so a fresh session reuses the same checked objects a build
	// does. Its summaries persist across inputs, which is what lets a later
	// import key against the modules already installed.
	modules *check.Installer
	stage   *staging.Session

	// installed is every module definition the session holds, prelude
	// first: the Core lint checks a program, and an imported module's calls
	// into modules imported earlier are only well-formed against it.
	installed []core.Def
	// promptDefs retains the active Core generation of prompt definitions.
	promptDefs map[string]core.Def
}

func NewSessionWith(out io.Writer, opts Options) *Session {
	var loading *importProgress
	observe := opts.Observe
	if opts.Interactive {
		loading = &importProgress{out: out}
		observe = func(event compileevent.Event) {
			loading.observe(event)
			opts.Observe.Report(event)
		}
	}
	root := opts.Root
	if root == "" {
		root = "."
	}
	graph, prelude, errs := modules.NewGraph(root)
	if len(errs) > 0 {
		panic("invalid bundled prelude: " + errs[0].Body)
	}
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	ck := infer.NewChecker(sup, b, infer.NewEnv())
	stage := staging.Install(ck)
	stage.Observe(staging.Observer(observe))
	// The prompt resolver canonicalizes every input, so the checker holds
	// the prelude under canonical names only. Its roots install as ordinary
	// dependency-role modules, so a fresh session reuses the very artifacts
	// a build of the same sources produced.
	ck.Fixity, ck.PreludeOwners = prelude.Fixities, prelude.Owners
	installer := check.NewInstaller(ck, stage, opts.objectCache(root), observe)
	preludeDefs, diagnostics, internalErr := installer.Install(prelude.Units, prelude.FixityHash)
	if len(diagnostics) > 0 {
		panic("invalid embedded prelude: " + diagnostics[0].Body)
	}
	if internalErr != nil {
		panic("invalid embedded prelude: " + internalErr.Error())
	}
	ck.PreludeInfos = installer.Infos()
	// Like a batch entry, a prompt sees instances and derivers from every
	// module of the closure it starts with.
	if ck.InstanceImports == nil {
		ck.InstanceImports = map[string]map[string]bool{}
	}
	ck.InstanceImports[""] = prelude.PromptVisible
	ck.CurrentOwner = ""
	// Prompt values are lazy memo cells (doc/design.md, "Interpreter and REPL") — evaluated once, so their
	// types stay monotypes (the block-binding monomorphism restriction).
	// Functions and lambdas still generalize.
	ck.MonoValues = true
	env := eval.NewEnv()
	env.DefineProg(&core.Prog{ADTs: ck.ADTOrder, Defs: preludeDefs, Natives: ck.Natives})
	ioctx := eval.NewIOContext(strings.NewReader(""), out)
	ioctx.ErrorWriter = opts.ErrorWriter
	return &Session{
		ck:         ck,
		env:        env,
		out:        out,
		ioctx:      ioctx,
		graph:      graph,
		prompt:     graph.NewPrompt(),
		modules:    installer,
		stage:      stage,
		installed:  preludeDefs,
		promptDefs: map[string]core.Def{},
		color:      opts.Color,
		loading:    loading,
	}
}

// Close stops the session's own native worker, if it built one.
func (s *Session) Close() {
	if s.exec != nil {
		_ = s.exec.Close()
		s.exec = nil
	}
}

const banner = "Fango 0.1 — :help for commands"

// Run drives the read-eval-print loop until :quit or EOF, rooted at the
// working directory.
func Run(in io.Reader, out io.Writer) {
	RunWith(in, out, Options{})
}

// RunWith is Run with options.
//
// Multi-line policy (doc/design.md, "Interpreter and REPL": input continues while the layout stack
// is open): a first line that parses incomplete opens continuation mode;
// indented lines then accumulate WITHOUT re-submitting on the first complete
// parse — a `case` may grow another branch, a `type` another `|` line. A
// blank line, a column-1 line (necessarily a new declaration or expression),
// or EOF submits the buffer.
func RunWith(in io.Reader, out io.Writer, opts Options) {
	var tail *tailWriter
	if opts.Interactive {
		tail = &tailWriter{w: out}
		out = tail
	}
	s := NewSessionWith(out, opts)
	defer s.Close()
	fmt.Fprintln(out, banner)
	reader := bufio.NewReader(in)
	signals := make(chan os.Signal, 1)
	interrupts := make(chan struct{}, 1)
	stopped := make(chan struct{})
	cancelEval := func() {
		if executor := s.evalExec.Load(); executor != nil {
			executor.Interrupt()
		}
		s.cancelCurrent()
	}
	signal.Notify(signals, os.Interrupt)
	defer signal.Stop(signals)
	defer close(stopped)
	go func() {
		for {
			select {
			case <-signals:
				cancelEval()
				select {
				case interrupts <- struct{}{}:
				default:
				}
			case <-stopped:
				return
			}
		}
	}()
	var source lineSource = plainSource{reader}
	var ed lineEditor
	if opts.Interactive {
		if opts.lineEditor != nil {
			ed = opts.lineEditor(tail, cancelEval)
		} else {
			ed = newEditor(tail, cancelEval)
		}
		defer ed.Close()
		source = ed
	}
	input := newLinePump(source, interrupts)
	defer input.Close()
	s.interrupts = interrupts
	s.ioctx = &eval.IOContext{Reader: reader, Input: input, Writer: out, ErrorWriter: s.ioctx.ErrorWriter, Natives: s.ioctx.Natives}
	var buf strings.Builder
	flush := func() {
		if buf.Len() > 0 {
			s.submit(buf.String())
			buf.Reset()
		}
	}
	for {
		req := lineRequest{prompt: "> "}
		if len(s.levels) > 0 {
			req.prompt = fmt.Sprintf("%d> ", len(s.levels))
		}
		if buf.Len() > 0 {
			req.prompt = "| "
			// Continuation lines start at the previous line's indentation.
			last := buf.String()[strings.LastIndexByte(buf.String(), '\n')+1:]
			req.suggestion = last[:len(last)-len(strings.TrimLeft(last, " "))]
		}
		if ed == nil {
			fmt.Fprint(out, req.prompt)
		}
		data, err := input.next(req)
		if errors.Is(err, errInterruptedInput) {
			buf.Reset()
			if !errors.Is(err, errAbortedInput) {
				fmt.Fprintln(out)
			}
			continue
		}
		line := string(data)
		if err != nil && len(line) == 0 {
			fmt.Fprintln(out)
			flush()
			// Ctrl-D in the editor ends one handler level; the end of
			// piped input ends them all.
			if ed != nil && len(s.levels) > 0 {
				s.endLevel()
				continue
			}
			s.endAll()
			return
		}
		line = strings.TrimSuffix(line, "\n")
		line = strings.TrimSuffix(line, "\r")
		trimmed := strings.TrimSpace(line)
		if ed != nil && trimmed != "" {
			ed.record(line)
		}

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
		s.endAll()
		return true
	case cmd == ":end":
		s.endLevel()
	case cmd == ":uses":
		s.listLevels()
	case cmd == ":help":
		fmt.Fprint(s.out, `commands:
  :type <expr>   show an expression's type without evaluating
  :uses          list the handler levels installed with `+"`use`"+`
  :end           end the innermost handler level (also Ctrl-D)
  :help          this message
  :quit          end every level and leave the REPL
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
	if parser.IsUseInput(toks) {
		_, _, _, errs = parser.ParseUseInput(toks, f)
	} else if isDecl(toks) {
		_, errs = parser.Parse(toks, f)
	} else {
		_, errs = parser.ParseExprInput(toks, f)
	}
	return !wantsMore(errs)
}

// input handles one import, declaration, or expression. Unless force is set,
// a parse that failed only by running out of input reports needMoreInput
// instead of rendering errors — the continuation signal.
func (s *Session) input(text string, force bool) inputResult {
	f := source.NewFile("<repl>", []byte(text))
	toks, lexErrs := lexer.Lex(f)
	if len(lexErrs) > 0 {
		diag.Render(s.out, lexErrs)
		return inputDone
	}
	if parser.IsUseInput(toks) {
		return s.levelInput(text, toks, f, force)
	}
	if isDecl(toks) {
		return s.declInput(toks, f, force)
	}
	return s.exprInput(toks, f, force)
}

// isDecl: `import …`, `type …`, `name = …`, `name params… = …`, `name : …`
// (an annotation opening a definition), or a fixity declaration is a
// declaration; anything else is an expression. `==` lexes as one token, so
// comparisons still classify as expressions, and `f x y` without `=` stays
// an application.
//
// An operator declaration opens with `(op)`, which also opens the
// expression `(+) 1 2`; the `=` scan below is what separates them.
func isDecl(toks []token.Token) bool {
	if len(toks) >= 1 && (toks[0].Kind == token.ATTRIBUTE || toks[0].Kind == token.PRAGMA || toks[0].Kind == token.KwImport || toks[0].Kind == token.KwType || toks[0].Kind == token.KwEffect || toks[0].Kind == token.KwClass || toks[0].Kind == token.KwInstance || toks[0].Kind == token.KwDeriver) {
		return true
	}
	if len(toks) >= 1 && (toks[0].Kind == token.KwInfix || toks[0].Kind == token.KwInfixL || toks[0].Kind == token.KwInfixR) {
		return true
	}
	i := 1
	switch {
	case len(toks) >= 2 && toks[0].Kind == token.LIDENT:
	case len(toks) >= 2 && (toks[0].Kind == token.UIDENT || toks[0].Kind == token.LBRACKET || toks[0].Kind == token.LPAREN):
		i = 0
	case len(toks) >= 4 && toks[0].Kind == token.LPAREN && toks[1].Kind == token.OP && toks[2].Kind == token.RPAREN:
		i = 3
	default:
		return false
	}
	if i < len(toks) && toks[i].Kind == token.COLON {
		return true
	}
	depth := 0
	for ; i < len(toks); i++ {
		switch toks[i].Kind {
		case token.LPAREN, token.LBRACKET, token.LBRACE:
			depth++
		case token.RPAREN, token.RBRACKET, token.RBRACE:
			if depth > 0 {
				depth--
			}
		case token.EQ:
			if depth == 0 {
				return true
			}
		}
	}
	return false
}

// checkpoint protects the whole persistent session state an input may touch:
// the checker's declaration environment and the prompt's resolver scope. The
// evaluation environment is only extended once both have accepted the input.
func (s *Session) checkpoint() func() {
	ck, prompt := s.ck.Checkpoint(), s.prompt.Checkpoint()
	return func() {
		ck()
		prompt()
	}
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
	// A valid empty effect header may still grow operation signatures at the
	// prompt. Buffer it until a blank line, a new top-level input, or EOF.
	if !force && len(m.Decls) == 1 {
		if effect, ok := m.Decls[0].(*ast.EffectDecl); ok && len(effect.Ops) == 0 {
			return needMoreInput
		}
	}
	if len(m.Imports) > 0 {
		return s.importInput(m)
	}
	if len(m.Decls) == 0 {
		return inputDone
	}
	restore := s.checkpoint()
	// The prompt is parsed one entry at a time, so grouping happens here
	// against the session table rather than during module loading. A fixity
	// declaration extends the table for later entries.
	errs = append(errs, s.ck.Fixity.Collect(m.Decls)...)
	errs = append(errs, s.ck.Fixity.Resolve(m)...)
	if len(errs) > 0 {
		restore()
		diag.Render(s.out, errs)
		return inputDone
	}
	// Names reach the checker canonical, exactly as a module's do: the
	// resolver binds what this input declares and rewrites what it uses.
	var prior savedName
	if vd, ok := m.Decls[0].(*ast.ValueDecl); ok {
		prior = s.snapshot(-1, vd.Name)
	}
	if errs := s.prompt.Decl(m.Decls[0]); len(errs) > 0 {
		restore()
		diag.Render(s.out, errs)
		return inputDone
	}
	owner := s.owner(m.Decls[0])
	if _, ok := m.Decls[0].(*ast.ValueDecl); !ok && owner >= 0 {
		restore()
		diag.Render(s.out, []diag.Error{diag.Errorf(toks[0].Span, "LEVEL NAME", "Only value definitions may use a handler level's names, since they go away\nwith the level.")})
		return inputDone
	}
	if cl, ok := m.Decls[0].(*ast.ClassDecl); ok {
		errs := s.ck.ClassDecl(cl)
		for _, vd := range infer.ClassDefaultDecls(cl) {
			if len(errs) == 0 {
				_, _, errs = s.defineValue(vd)
			}
		}
		if len(errs) > 0 {
			restore()
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
		if errs := s.ck.DeriverDecl(dr); len(errs) > 0 {
			restore()
			diag.Render(s.out, errs)
		} else {
			fmt.Fprintf(s.out, "deriver %s\n", types.SurfaceName(dr.Class))
		}
		return inputDone
	}
	if in, ok := m.Decls[0].(*ast.InstanceDecl); ok {
		start := len(s.ck.Instances)
		var errs []diag.Error
		for _, method := range in.Methods {
			errs = append(errs, s.ck.StageDecl(method)...)
		}
		if len(errs) > 0 {
			restore()
			diag.Render(s.out, errs)
			return inputDone
		}
		infos, errs := s.ck.InstanceDecl(in)
		if len(errs) == 0 {
			errs = s.installInstances(infos, start)
		}
		if len(errs) > 0 {
			restore()
			diag.Render(s.out, errs)
		} else {
			fmt.Fprintf(s.out, "instance %s %s\n", types.SurfaceName(in.Head.Class), ast.DumpTypeExpr(in.Head.Ty))
		}
		return inputDone
	}
	if td, ok := m.Decls[0].(*ast.TypeDecl); ok {
		return s.typeDeclInput(td, restore)
	}
	if ed, ok := m.Decls[0].(*ast.EffectDecl); ok {
		if errs := s.ck.EffectDecl(ed); len(errs) > 0 {
			restore()
			diag.Render(s.out, errs)
		} else {
			fmt.Fprintf(s.out, "%s : effect\n", ed.Name)
		}
		return inputDone
	}
	if fd, ok := m.Decls[0].(*ast.FixityDecl); ok {
		// Collect above already recorded it, so later entries group by it.
		// A fixity binds no name and has no type to report.
		fmt.Fprintf(s.out, "%s %d (%s)\n", fd.Assoc, fd.Prec, fd.Op)
		return inputDone
	}
	if pd, ok := m.Decls[0].(*ast.PatternDecl); ok {
		infos, inferErrs := s.ck.PatternDecl(pd, false)
		if len(inferErrs) > 0 {
			restore()
			diag.Render(s.out, inferErrs)
			return inputDone
		}
		var allDefs []core.Def
		for _, info := range infos {
			defs, es := elaborate.DeclIn(info, s.installed, s.ck)
			if len(es) > 0 {
				restore()
				diag.Render(s.out, es)
				return inputDone
			}
			allDefs = append(allDefs, defs...)
		}
		for i := range allDefs {
			def := &allDefs[i]
			if def.IsWorker() {
				s.env.DefineWorker(def)
			} else {
				s.env.Define(def.Name, def.Body)
			}
		}
		s.ck.RecordChecked(infos)
		for _, info := range infos[1:] {
			sch := info.Scheme
			sch.Body = s.ck.Sub.Apply(sch.Body)
			fmt.Fprintf(s.out, "%s : %s\n", types.SurfaceName(info.Name), types.ShowScheme(sch))
		}
		return inputDone
	}
	vd := m.Decls[0].(*ast.ValueDecl)
	if len(vd.Equations) > 0 {
		restore()
		diag.Render(s.out, []diag.Error{diag.Errorf(vd.NameSpan, "GROUPED INPUT", "Multiple function equations are supported in source files; enter one exhaustive equation at the REPL.")})
		return inputDone
	}
	if vd.Native != nil {
		restore()
		diag.Render(s.out, []diag.Error{diag.Errorf(vd.Native.Sp, "NATIVE MODULE REQUIRED", "Native declarations belong in source modules with a sidecar and cannot be entered directly at the REPL.")})
		return inputDone
	}
	redefining := s.ck.Env.Has(vd.Name)
	info, def, errs := s.defineValue(vd)
	if len(errs) > 0 {
		restore()
		diag.Render(s.out, errs)
		return inputDone
	}
	s.defined(vd.Name, owner, prior)
	if redefining {
		s.gen++
	}
	sch := info.Scheme
	sch.Body = s.ck.Sub.Apply(sch.Body)
	sch.Preds = s.ck.NormalizePreds(sch.Preds)
	fmt.Fprintf(s.out, "%s : %s\n", def.Name, types.ShowScheme(sch))
	return inputDone
}

// defineValue checks, elaborates, and installs one top-level definition. A
// failed definition installs nothing; the caller restores its checkpoint.
func (s *Session) defineValue(vd *ast.ValueDecl) (infer.DeclInfo, *core.Def, []diag.Error) {
	// A failed input leaves nothing behind, expansion included: a splice that
	// fails half way through has already checked whatever preceded it.
	if errs := s.ck.StageDecl(vd); len(errs) > 0 {
		return infer.DeclInfo{}, nil, errs
	}
	// Check the body BEFORE binding: a failed definition must not install
	// a broken name into the session. REPL declarations are required to be
	// pure; effectful expressions can be evaluated directly at the prompt.
	info, errs := s.ck.DeclWhere(vd, false)
	if len(errs) > 0 {
		return infer.DeclInfo{}, nil, errs
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
	defs, errs := elaborate.DeclIn(info, s.activeExecutionDefs(), s.ck)
	if len(errs) > 0 {
		return infer.DeclInfo{}, nil, errs
	}
	s.ck.BindDecl(info)
	// A later splice may name this definition, so the compile-time
	// evaluator's prefix has to grow with the session.
	s.ck.RecordChecked([]infer.DeclInfo{info})
	def := &defs[0]
	for i := range defs {
		s.promptDefs[defs[i].Name] = defs[i]
	}
	for i := range defs[1:] {
		s.env.DefineWorker(&defs[1+i]) // lambda-lifted locals (doc/design.md, "Go backend and runtime")
	}
	if def.IsWorker() {
		s.env.DefineWorker(def)
	} else {
		s.env.Define(def.Name, def.Body)
	}
	return info, def, nil
}

// importInput brings modules into the session. Each import loads what the
// graph lacks, checks and installs the new modules, and extends the prompt
// scope; the input is all-or-nothing, so a failing import leaves the graph,
// the checker, and the scope exactly as they were.
func (s *Session) importInput(m *ast.Module) inputResult {
	if len(m.Decls) > 0 {
		diag.Render(s.out, []diag.Error{diag.Errorf(m.Imports[0].ModuleSpan, "IMPORT INPUT", "Enter imports on their own; a declaration is its own input.")})
		return inputDone
	}
	restore := s.checkpoint()
	restoreGraph := s.graph.Checkpoint()
	defer s.loading.clear()
	fail := func(errs []diag.Error) inputResult {
		restore()
		restoreGraph()
		s.loading.clear()
		diag.Render(s.out, errs)
		return inputDone
	}
	// Prepare every import the input names before any of them reaches the
	// evaluator, the installed set, or the native worker: a later failure
	// must leave the session exactly as the prompt found it.
	var loaded []string
	var natives []nativehost.Source
	var pending []core.Def
	needsOpaqueWorker := false
	for _, im := range m.Imports {
		if !s.graph.Loaded(im.Module) {
			s.loading.show(im.Module)
		}
		inc, errs := s.prompt.Import(im)
		if len(errs) > 0 {
			return fail(errs)
		}
		if len(inc.Modules) == 0 {
			continue
		}
		defs, errs, internalErr := s.modules.Install(inc.Units, inc.FixityHash)
		if internalErr != nil {
			return fail([]diag.Error{{Title: "INTERNAL COMPILER ERROR", Body: internalErr.Error()}})
		}
		if len(errs) > 0 {
			return fail(errs)
		}
		pending = append(pending, defs...)
		loaded = append(loaded, inc.Modules...)
		for _, n := range s.ck.Natives {
			for _, wrapper := range append(append([]*types.CtorInfo(nil), n.ParamWrappers...), n.ResultWrapper) {
				if wrapper == nil {
					continue
				}
				for _, field := range wrapper.Fields {
					if con, ok := field.(*types.TCon); ok {
						if adt := s.ck.ADTs[con.Unique]; adt != nil && adt.Repr == types.ReprNativeAny {
							needsOpaqueWorker = true
						}
					}
				}
			}
		}

		for _, n := range inc.Natives {
			natives = append(natives, nativehost.Source{Module: n.Module, Content: n.Content})
		}
	}
	// The worker is built, but neither installed nor swapped in, before the
	// transaction commits: a failure here must not close the running one.
	var exec *nativehost.Executor
	if len(natives) > 0 || needsOpaqueWorker && s.exec == nil {
		prepared, err := s.prepareNatives(natives)
		if err != nil {
			return fail([]diag.Error{{Title: "NATIVE WORKER ERROR", Body: err.Error()}})
		}
		exec = prepared
	}
	s.installed = append(s.installed, pending...)
	if len(pending) > 0 {
		s.env.DefineProg(&core.Prog{ADTs: s.ck.ADTOrder, Defs: pending, Natives: s.ck.Natives})
	}
	// Like a batch entry, the prompt sees instances and derivers from every
	// module in its graph.
	for _, name := range loaded {
		s.ck.InstanceImports[""][name] = true
	}
	if exec != nil {
		s.commitNatives(natives, exec)
	}
	if s.loading != nil {
		s.loading.finish(m.Imports, loaded)
	} else {
		for _, name := range loaded {
			fmt.Fprintf(s.out, "loaded %s\n", name)
		}
	}
	return inputDone
}

// prepareNatives builds a worker over the bundled sidecars and every user
// sidecar the session holds or this input adds, without disturbing the
// running one. The worker only builds and starts when a sidecar function is
// first called, and builds are cached by content.
func (s *Session) prepareNatives(sources []nativehost.Source) (*nativehost.Executor, error) {
	bundled, err := nativehost.BundledSources()
	if err != nil {
		return nil, err
	}
	return nativehost.New(append(append(bundled, s.natives...), sources...))
}

// commitNatives adopts a prepared worker and retires the previous one.
func (s *Session) commitNatives(sources []nativehost.Source, exec *nativehost.Executor) {
	s.natives = append(s.natives, sources...)
	old := s.exec
	s.exec, s.ioctx.Natives = exec, exec
	if old != nil {
		_ = old.Close()
	}
}

// typeDeclInput installs a `type` declaration. Redefinition mints a fresh
// generation (a new Unique), exactly like value redefinition (doc/design.md, "Interpreter and REPL").
func (s *Session) typeDeclInput(td *ast.TypeDecl, restore func()) inputResult {
	start := len(s.ck.Instances)
	_, redefining := s.ck.TypeNames[td.Name]
	if errs := s.ck.TypeDecl(td); len(errs) > 0 {
		restore()
		diag.Render(s.out, errs)
		return inputDone
	}
	infos, errs := s.ck.DeriveDecl(td)
	if len(errs) == 0 {
		errs = s.installInstances(infos, start)
	}
	if len(errs) > 0 {
		restore()
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
	e, errs = s.ck.Fixity.ResolveExpr(e)
	if len(errs) > 0 {
		diag.Render(s.out, errs)
		return inputDone
	}
	if errs := s.prompt.Expr(e); len(errs) > 0 {
		diag.Render(s.out, errs)
		return inputDone
	}
	e, errs = s.ck.StageExpr(e)
	if len(errs) > 0 {
		diag.Render(s.out, errs)
		return inputDone
	}
	ty, effects, inferErrs := s.ck.ExprEffects(e)
	if len(inferErrs) > 0 {
		diag.Render(s.out, inferErrs)
		return inputDone
	}
	if _, errs := s.promptEffects(effects, e.Span()); len(errs) > 0 {
		diag.Render(s.out, errs)
		return inputDone
	}
	// The displayed type is the pre-defaulting one — free variables print
	// as the generalized scheme would (`{ x -> x }` echoes `a -> a`), doc/design.md, "Interpreter and REPL".
	// Elaboration then defaults for evaluation; the value renders at the
	// defaulted (ground) type.
	shownTy := types.ShowScheme(types.Scheme{Body: s.ck.Sub.Apply(ty), Preds: s.ck.PendingPreds})
	coreExpr, aux, elabErrs := elaborate.ExprIn(e, s.activeExecutionDefs(), s.ck)
	if len(elabErrs) > 0 {
		diag.Render(s.out, elabErrs)
		return inputDone
	}
	row, _ := s.ck.Sub.Apply(effects).(types.Row)
	fails, _ := s.promptEffects(row, e.Span())
	display := elaborate.PromptOutcome(coreExpr, row, fails, s.ck, "")
	for i := range aux {
		s.env.DefineWorker(&aux[i])
	}
	reply, _ := s.evaluate(display)
	s.settle(reply, shownTy)
	return inputDone
}

// promptEffects sorts the effects a prompt expression performs: IO is
// ambient, and each Fail application is handled for this input alone, so its
// failure prints rather than escaping. An installed level may grant others;
// anything else has no handler here.
func (s *Session) promptEffects(effects types.Type, at source.Span) ([]types.Type, []diag.Error) {
	row, ok := s.ck.Sub.Apply(effects).(types.Row)
	if !ok {
		return nil, nil
	}
	var fails []types.Type
	var errs []diag.Error
	allowed := s.allowed()
	for _, label := range row.Labels {
		switch {
		case label.Scoped, types.SurfaceName(label.Name) == "IO":
		case label.Name == "Fail.Fail" && len(label.Args) == 1:
			fails = append(fails, s.ck.Sub.Apply(label.Args[0]))
		default:
			var candidates []types.EffLabel
			for _, granted := range allowed {
				if granted.Unique == label.Unique && len(granted.Args) == len(label.Args) {
					candidates = append(candidates, granted)
				}
			}
			// Several levels may grant the effect. One whose arguments
			// already agree is the one meant; otherwise the innermost, whose
			// handler is the one that answers.
			if len(candidates) > 1 {
				var exact []types.EffLabel
				for _, granted := range candidates {
					if types.Equal(s.ck.Sub.Apply(types.Row{Labels: []types.EffLabel{granted}}), s.ck.Sub.Apply(types.Row{Labels: []types.EffLabel{label}})) {
						exact = append(exact, granted)
					}
				}
				if len(exact) > 0 {
					candidates = exact
				}
			}
			if len(candidates) > 0 {
				errs = append(errs, s.unifyLabel(label, candidates[len(candidates)-1], at)...)
				errs = append(errs, s.settlePreds(at)...)
				continue
			}
			errs = append(errs, diag.Errorf(at, "UNHANDLED EFFECT",
				"This performs `%s`, and nothing at the prompt handles it. Run it inside\na handler, or install one with `use` for the inputs that follow.", strings.Trim(types.Show(types.Row{Labels: []types.EffLabel{label}}), "{}")))
		}
	}
	return fails, errs
}

// unhandled reports a failure no handler caught, in red on a terminal.
func (s *Session) unhandled(text string) {
	line := "Unhandled Error: " + text
	if s.color {
		line = "\x1b[31m" + line + "\x1b[0m"
	}
	fmt.Fprintln(s.out, line)
}

func (s *Session) evalDisplay(ctx context.Context, display core.Expr) (value eval.Value, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%v", recovered)
		}
	}()
	return eval.EvalIO(ctx, display, s.env, s.ioctx)
}

func (s *Session) activeExecutionDefs() []core.Def {
	defs := append([]core.Def(nil), s.installed...)
	names := make([]string, 0, len(s.promptDefs))
	for name := range s.promptDefs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		defs = append(defs, s.promptDefs[name])
	}
	return defs
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
	if e, errs = s.ck.Fixity.ResolveExpr(e); len(errs) > 0 {
		diag.Render(s.out, errs)
		return
	}
	if errs := s.prompt.Expr(e); len(errs) > 0 {
		diag.Render(s.out, errs)
		return
	}
	ty, inferErrs := s.ck.Expr(e)
	if len(inferErrs) > 0 {
		diag.Render(s.out, inferErrs)
		return
	}
	// Show the generalized view: free variables print as the scheme would
	// (`:type { x -> x }` says `a -> a`), no defaulting forced (doc/design.md, "Interpreter and REPL").
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
	s.env.DefineProg(&core.Prog{ADTs: s.ck.ADTOrder, Defs: defs, Natives: s.ck.Natives})
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
