package repl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/elaborate"
	"github.com/waj/fango/internal/eval"
	"github.com/waj/fango/internal/infer"
	"github.com/waj/fango/internal/nativehost"
	"github.com/waj/fango/internal/parser"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/token"
	"github.com/waj/fango/internal/types"
)

// A level is a `use` typed at the prompt. Its head runs with a callback whose
// body is Runtime.Prompt.level, and the evaluator answers that call by running
// the following inputs inside the head's handlers, so their state and
// resources last until the level ends (doc/design/repl.md, "Handler levels").
type level struct {
	source string
	// granted is what the callback may perform besides IO and Fail, which
	// later inputs may therefore perform too.
	granted types.Row
	binders []binder
	result  types.Type
	// names are the binders and the level-local definitions that use them.
	names  map[string]bool
	cancel context.CancelFunc
	// bound is set once the level's first input has named its binders to
	// the evaluator that holds their values.
	bound bool
}

type binder struct {
	name string
	ty   types.Type
}

// savedName is what a name meant before a level's binder or level-local
// definition replaced it. Ending that level puts it back.
type savedName struct {
	level     int
	name      string
	scheme    types.Scheme
	hasScheme bool
	worker    int
	hasWorker bool
	def       core.Def
	hasDef    bool
	canonical string
	resolved  bool
	binding   eval.Binding
}

func (s *Session) snapshot(owner int, name string) savedName {
	u := savedName{level: owner, name: name, binding: s.env.Binding(name)}
	u.scheme, u.hasScheme = s.ck.Env.Lookup(name)
	u.worker, u.hasWorker = s.ck.Workers[name]
	u.def, u.hasDef = s.promptDefs[name]
	u.canonical, u.resolved = s.prompt.Value(name)
	return u
}

func (s *Session) restoreName(u savedName) {
	if u.hasScheme {
		s.ck.Env.Bind(u.name, u.scheme)
	} else {
		s.ck.Env.Unbind(u.name)
	}
	if u.hasWorker {
		s.ck.Workers[u.name] = u.worker
	} else {
		delete(s.ck.Workers, u.name)
	}
	if u.hasDef {
		s.promptDefs[u.name] = u.def
	} else {
		delete(s.promptDefs, u.name)
	}
	s.prompt.SetValue(u.name, u.canonical, u.resolved)
	s.env.Restore(u.binding)
}

// defined records that name now has a definition owned by level owner, or by
// no level when owner is -1. prior is what it meant just before. A deeper
// level no longer owns the name, so its record moves to the new owner, which
// puts back what the name meant before any level took it.
func (s *Session) defined(name string, owner int, prior savedName) {
	var earliest *savedName
	kept := s.undo[:0]
	for _, u := range s.undo {
		if u.name == name && u.level > owner {
			if earliest == nil {
				first := u
				earliest = &first
			}
			continue
		}
		kept = append(kept, u)
	}
	s.undo = kept
	for i := owner + 1; i < len(s.levels); i++ {
		delete(s.levels[i].names, name)
	}
	if owner < 0 {
		return
	}
	s.levels[owner].names[name] = true
	for _, u := range s.undo {
		if u.name == name && u.level == owner {
			return
		}
	}
	if earliest != nil {
		prior = *earliest
	}
	prior.level = owner
	s.undo = append(s.undo, prior)
}

// owner is the deepest level whose names d refers to, or -1.
func (s *Session) owner(d ast.Decl) int {
	if len(s.levels) == 0 {
		return -1
	}
	dump := ast.Dump(&ast.Module{Decls: []ast.Decl{d}})
	for i := len(s.levels) - 1; i >= 0; i-- {
		for name := range s.levels[i].names {
			if strings.Contains(dump, "(var "+name+")") {
				return i
			}
		}
	}
	return -1
}

func (s *Session) levelChannels() *eval.ChannelLevels {
	if s.levelIO == nil {
		s.levelIO = &eval.ChannelLevels{Requests: make(chan eval.LevelRequest), Replies: make(chan eval.LevelReply)}
	}
	s.ioctx.Levels = s.levelIO
	return s.levelIO
}

// request sends an input to the innermost level. A level in the native
// worker receives the session's definitions with it, and a level's first
// input names its binders.
func (s *Session) request(ctx context.Context, e core.Expr) eval.LevelRequest {
	req := eval.LevelRequest{Ctx: ctx, Expr: e}
	if e != nil && s.ioctx.Natives != nil {
		req.Program = s.env.Program()
	}
	if e != nil {
		for _, label := range s.allowed() {
			if label.Scoped {
				continue
			}
			instance := core.EffectInstance{Unique: label.Unique, Name: label.Name}
			for _, arg := range label.Args {
				instance.Args = append(instance.Args, s.ck.Sub.Apply(arg))
			}
			req.Effects = append(req.Effects, instance)
		}
	}
	if innermost := s.levels[len(s.levels)-1]; e != nil && !innermost.bound {
		innermost.bound = true
		for _, b := range innermost.binders {
			req.Binders = append(req.Binders, b.name)
		}
	}
	return req
}

// evaluate runs an input's Core in the innermost level, or in the session's
// own evaluator when there is none, and waits for the answer. Ctrl-C cancels
// it. The context of an input that entered a level lives as long as the level.
func (s *Session) evaluate(e core.Expr) (eval.LevelReply, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	s.setCancel(cancel)
	defer s.setCancel(nil)
	levels := s.levelChannels()
	if s.exec != nil {
		s.evalExec.Store(s.exec)
	} else if bundled, bundledErr := nativehost.Bundled(); bundledErr == nil {
		s.evalExec.Store(bundled)
	}
	defer s.evalExec.Store(nil)
	if len(s.levels) == 0 {
		go func() {
			v, err := s.evalDisplay(ctx, e)
			levels.Replies <- eval.LevelReply{Value: v, Err: err}
		}()
	} else {
		levels.Requests <- s.request(ctx, e)
	}
	reply := <-levels.Replies
	if s.interrupts != nil {
		select {
		case <-s.interrupts:
		default:
		}
	}
	if !reply.Entered {
		cancel()
	}
	return reply, cancel
}

// setCancel makes Ctrl-C cancel an in-process evaluation. The native worker
// is interrupted by signal instead: cancelling the context of its exchange
// would drop the connection, and every level running in it.
func (s *Session) setCancel(cancel context.CancelFunc) {
	if cancel != nil && s.ioctx.Natives != nil {
		return
	}
	s.cancelMu.Lock()
	s.cancelInput = cancel
	s.cancelMu.Unlock()
}

func (s *Session) cancelCurrent() {
	s.cancelMu.Lock()
	cancel := s.cancelInput
	s.cancelMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// settle prints the answer to an input sent at the current depth. A reply
// from a shallower level means an abort escaped the input and ended the levels
// between: the reply is then the outcome of the outermost level that ended.
func (s *Session) settle(reply eval.LevelReply, shownTy string) {
	if depth := len(s.levels); reply.Depth < depth {
		ended := s.levels[reply.Depth]
		s.popLevels(reply.Depth)
		s.levelOutcome(reply, ended, depth-reply.Depth, true)
		return
	}
	if reply.Err != nil {
		s.runtimeError(reply.Err)
		return
	}
	outcome := reply.Value.(string)
	if rest, failed := strings.CutPrefix(outcome, elaborate.PromptFailure); failed {
		s.unhandled(rest)
		return
	}
	fmt.Fprintf(s.out, "%s : %s\n", strings.TrimPrefix(outcome, elaborate.PromptValue), shownTy)
}

func (s *Session) runtimeError(err error) {
	if errors.Is(err, context.Canceled) {
		fmt.Fprintln(s.out, "interrupted")
		return
	}
	fmt.Fprintf(s.out, "runtime error: %v\n", err)
}

// levelOutcome prints what a level's head finished with. Ending one level on
// request says nothing about Unit; anything unexpected names how many levels
// ended.
func (s *Session) levelOutcome(reply eval.LevelReply, ended *level, left int, unexpected bool) {
	suffix := ""
	if unexpected || left > 1 {
		suffix = fmt.Sprintf(" (left %d level", left)
		if left > 1 {
			suffix += "s"
		}
		suffix += ")"
	}
	if reply.Err != nil {
		s.runtimeError(fmt.Errorf("%w%s", reply.Err, suffix))
		return
	}
	outcome := reply.Value.(string)
	if rest, failed := strings.CutPrefix(outcome, elaborate.PromptFailure); failed {
		s.unhandled(rest + suffix)
		return
	}
	if suffix == "" && types.Equal(ended.result, s.ck.B.Unit) {
		return
	}
	shown := types.ShowScheme(types.Scheme{Body: ended.result})
	fmt.Fprintf(s.out, "%s : %s%s\n", strings.TrimPrefix(outcome, elaborate.PromptValue), shown, suffix)
}

// popLevels forgets every level from index to the innermost: their binders
// and level-local definitions give their names back.
func (s *Session) popLevels(index int) {
	for i := len(s.levels) - 1; i >= index; i-- {
		for j := len(s.undo) - 1; j >= 0; j-- {
			if s.undo[j].level == i {
				s.restoreName(s.undo[j])
				s.undo = append(s.undo[:j], s.undo[j+1:]...)
			}
		}
		s.levels[i].cancel()
		s.levels = s.levels[:i]
	}
}

// endLevel ends the innermost level: its callback returns, and its head
// finishes and reports.
func (s *Session) endLevel() {
	depth := len(s.levels)
	if depth == 0 {
		fmt.Fprintln(s.out, "no handler level to end")
		return
	}
	s.setCancel(s.levels[depth-1].cancel)
	levels := s.levelChannels()
	levels.Requests <- s.request(nil, nil)
	reply := <-levels.Replies
	s.setCancel(nil)
	// The head's cleanup may abort toward an outer level too; the reply is
	// then the outcome of the outermost level that ended.
	ended := s.levels[reply.Depth]
	s.popLevels(reply.Depth)
	s.levelOutcome(reply, ended, depth-reply.Depth, false)
}

// endAll ends every level, innermost first, as leaving the session does.
func (s *Session) endAll() {
	for len(s.levels) > 0 {
		s.endLevel()
	}
}

func (s *Session) listLevels() {
	if len(s.levels) == 0 {
		fmt.Fprintln(s.out, "no handler levels")
		return
	}
	for i, l := range s.levels {
		fmt.Fprintf(s.out, "%d  %s\n", i+1, l.source)
		if len(l.granted.Labels) > 0 {
			fmt.Fprintf(s.out, "   provides %s\n", types.Show(types.Row{Labels: l.granted.Labels}))
		}
		for _, b := range l.binders {
			fmt.Fprintf(s.out, "   %s : %s\n", b.name, types.ShowScheme(types.Scheme{Body: s.ck.Sub.Apply(b.ty)}))
		}
	}
}

// allowed is what an input at the current depth may perform besides IO and
// Fail: everything the levels' callbacks may.
func (s *Session) allowed() []types.EffLabel {
	var labels []types.EffLabel
	for _, l := range s.levels {
		labels = append(labels, l.granted.Labels...)
	}
	return labels
}

// levelInput installs a `use` typed at the prompt: it checks the head applied
// to a callback whose body is Runtime.Prompt.level over the binders, runs it,
// and, once the callback starts, binds the binders for the inputs that follow.
func (s *Session) levelInput(text string, toks []token.Token, f *source.File, force bool) inputResult {
	w, params, head, errs := parser.ParseUseInput(toks, f)
	if !force && wantsMore(errs) {
		return needMoreInput
	}
	if len(errs) > 0 {
		diag.Render(s.out, errs)
		return inputDone
	}
	if !s.levelsReady {
		quiet := s.out
		s.out = io.Discard
		result := s.input("import Runtime.Prompt", true)
		s.out = quiet
		if result != inputDone || !s.ck.Env.Has(types.PromptLevelName) {
			diag.Render(s.out, []diag.Error{diag.Errorf(w.Keyword, "UNSUPPORTED LEVEL", "The bundled Runtime.Prompt module did not load.")})
			return inputDone
		}
		s.levelsReady = true
	}
	var vars []*ast.PVar
	for _, p := range params {
		vars = append(vars, patternVars(p)...)
	}
	refs := make([]*ast.Var, len(vars))
	var pack ast.Expr = &ast.UnitLit{Sp: w.Keyword}
	for i := len(vars) - 1; i >= 0; i-- {
		refs[i] = &ast.Var{Name: vars[i].Name, Sp: vars[i].Sp}
		if i == len(vars)-1 {
			pack = refs[i]
		} else {
			pair := &ast.Ctor{Name: "Tuple.Pair", Sp: vars[i].Sp, Sugared: true}
			pack = &ast.App{Fn: &ast.App{Fn: pair, Arg: refs[i]}, Arg: pack}
		}
	}
	if len(params) == 0 {
		params = []ast.Pattern{&ast.PUnit{Sp: w.Keyword}}
	}
	call := &ast.Var{Name: "Runtime.Prompt.level", Sp: w.Keyword}
	lambda := &ast.Lambda{Params: params, Body: &ast.App{Fn: call, Arg: pack}, Sp: w.Keyword, Use: w}
	var e ast.Expr = &ast.App{Fn: head, Arg: lambda}

	restore := s.checkpoint()
	e, errs = s.ck.Fixity.ResolveExpr(e)
	if len(errs) == 0 {
		errs = s.prompt.Expr(e)
	}
	if len(errs) == 0 {
		e, errs = s.ck.StageExpr(e)
	}
	var ty, effects types.Type
	if len(errs) == 0 {
		ty, effects, errs = s.ck.ExprEffects(e)
	}
	if len(errs) == 0 {
		_, errs = s.promptEffects(effects, e.Span())
	}
	var coreExpr core.Expr
	if len(errs) == 0 {
		var aux []core.Def
		coreExpr, aux, errs = elaborate.ExprIn(e, s.activeExecutionDefs(), s.ck)
		for i := range aux {
			s.env.DefineWorker(&aux[i])
		}
	}
	if len(errs) > 0 {
		restore()
		diag.Render(s.out, errs)
		return inputDone
	}
	callback, ok := s.ck.Sub.Apply(s.ck.ExprTypes[lambda]).(*types.TFun)
	for i := 1; ok && i < len(params); i++ {
		callback, ok = callback.Ret.(*types.TFun)
	}
	if !ok {
		restore()
		diag.Render(s.out, []diag.Error{diag.Errorf(w.Keyword, "INVALID LEVEL", "The head of this `use` does not take a callback.")})
		return inputDone
	}
	lvl := &level{source: strings.TrimSpace(text), result: s.ck.Sub.Apply(ty), names: map[string]bool{}}
	for _, label := range callback.Eff.Labels {
		if types.SurfaceName(label.Name) != "IO" {
			lvl.granted.Labels = append(lvl.granted.Labels, label)
		}
	}
	for i, v := range vars {
		lvl.binders = append(lvl.binders, binder{name: v.Name, ty: s.ck.Sub.Apply(s.ck.ExprTypes[refs[i]])})
	}
	// A binder rebinds a prompt name the way a definition does, so a name an
	// import exposes is the same collision here.
	var priors []savedName
	for _, b := range lvl.binders {
		priors = append(priors, s.snapshot(len(s.levels), b.name))
		if errs := s.prompt.Decl(&ast.ValueDecl{Name: b.name, NameSpan: w.Keyword, Body: &ast.UnitLit{Sp: w.Keyword}}); len(errs) > 0 {
			restore()
			diag.Render(s.out, errs)
			return inputDone
		}
	}
	row, _ := s.ck.Sub.Apply(effects).(types.Row)
	fails, _ := s.promptEffects(row, e.Span())
	reply, cancel := s.evaluate(elaborate.PromptOutcome(coreExpr, row, fails, s.ck, ""))
	if !reply.Entered {
		// The head finished without calling back, so it is an ordinary
		// result, and its binders were never bound.
		for _, prior := range priors {
			s.prompt.SetValue(prior.name, prior.canonical, prior.resolved)
		}
		s.settle(reply, types.ShowScheme(types.Scheme{Body: lvl.result}))
		return inputDone
	}
	lvl.cancel = cancel
	s.levels = append(s.levels, lvl)
	owner := len(s.levels) - 1
	// The evaluator holding the level binds the values when the level's
	// first input names them; until then nothing can read them.
	for i, b := range lvl.binders {
		canonical, _ := s.prompt.Value(b.name)
		s.defined(b.name, owner, priors[i])
		s.prompt.SetValue(b.name, canonical, true)
		s.ck.Env.Bind(b.name, types.Scheme{Body: b.ty})
		delete(s.ck.Workers, b.name)
		delete(s.promptDefs, b.name)
		s.env.DefineValue(b.name, b.ty, nil)
		fmt.Fprintf(s.out, "%s : %s\n", b.name, types.ShowScheme(types.Scheme{Body: b.ty}))
	}
	return inputDone
}

func patternVars(p ast.Pattern) []*ast.PVar {
	switch p := p.(type) {
	case *ast.PVar:
		return []*ast.PVar{p}
	case *ast.PCtor:
		var out []*ast.PVar
		for _, arg := range p.Args {
			out = append(out, patternVars(arg)...)
		}
		return out
	case *ast.PRecord:
		var out []*ast.PVar
		for _, field := range p.Fields {
			if field.Pattern != nil {
				out = append(out, patternVars(field.Pattern)...)
			}
		}
		return out
	}
	return nil
}

// unifyLabel lets an input perform an effect a level grants: their type
// arguments must agree.
func (s *Session) unifyLabel(label, granted types.EffLabel, at source.Span) []diag.Error {
	var cs []infer.Constraint
	for i := range label.Args {
		cs = append(cs, infer.Constraint{Left: label.Args[i], Right: granted.Args[i], Span: at})
	}
	sub, _, errs := infer.Solve(cs, nil, s.ck.Sub, s.ck.B, s.ck.Sup)
	if len(errs) == 0 {
		s.ck.Sub = sub
	}
	return errs
}

// settlePreds rechecks the expression's pending constraints once a level's
// effect has fixed some of its types: a constraint on a now known type holds
// or is an error, and no longer shows in its type.
func (s *Session) settlePreds(at source.Span) []diag.Error {
	var pending []types.Pred
	var errs []diag.Error
	for _, p := range s.ck.NormalizePreds(s.ck.PendingPreds) {
		if hasTypeVars(p.Ty) {
			pending = append(pending, p)
		} else if !s.ck.CanResolve(p, "") {
			errs = append(errs, diag.Errorf(at, "MISSING INSTANCE", "No instance provides `%s`.", types.ShowPred(p.Class, p.Ty)))
		}
	}
	s.ck.PendingPreds = pending
	return errs
}

func hasTypeVars(t types.Type) bool {
	switch t := t.(type) {
	case *types.TVar:
		return true
	case *types.TCon:
		for _, arg := range t.Args {
			if hasTypeVars(arg) {
				return true
			}
		}
	case *types.TFun:
		return hasTypeVars(t.Arg) || hasTypeVars(t.Ret)
	}
	return false
}
