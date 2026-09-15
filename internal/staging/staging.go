// Package staging runs splices. It exists as its own package because
// evaluating a splice needs elaboration and the interpreter, which both sit
// above inference in the package graph, while the splice itself has to be
// expanded during inference. The checker holds the seam; this package fills
// it in.
package staging

import (
	"context"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/elaborate"
	"github.com/waj/fango/internal/eval"
	"github.com/waj/fango/internal/infer"
	machineir "github.com/waj/fango/internal/machine"
	"github.com/waj/fango/internal/types"
)

// Install gives ck a compile-time evaluator. Both the batch pipeline and the
// REPL call it, so a deriver behaves the same at the prompt as in a build.
func Install(ck *infer.Checker) {
	ev := &evaluator{ck: ck, env: eval.NewEnv()}
	ev.env.Templates = ck.Templates
	ck.CompileTime = ev.run
	// A failed expansion rolls the checker back past declarations this
	// environment already holds, so the environment is rebuilt from the
	// restored prefix rather than left describing a program that no longer
	// exists.
	ck.CompileTimeRollback = func(checked, instances int) {
		if ev.installedDecls <= checked && ev.installedInstances <= instances {
			return // nothing this environment holds was discarded
		}
		ev.env = eval.NewEnv()
		ev.env.Templates = ck.Templates
		ev.installedDecls, ev.installedInstances = 0, 0
		ev.installedIntrinsics = nil
		ev.defs = nil
	}
}

type evaluator struct {
	ck  *infer.Checker
	env *eval.Env

	// installed counts what the compile-time environment already holds, so
	// each splice elaborates only the prefix that appeared since the last
	// one. A program with no splices elaborates nothing twice.
	installedDecls     int
	installedInstances int

	// installedIntrinsics records whether this environment already holds the
	// compiler intrinsics. They have no declaration prefix to follow.
	installedIntrinsics map[string]bool
	defs                []core.Def
}

func (ev *evaluator) run(operand ast.Expr) (any, []diag.Error) {
	if errs := ev.sync(); len(errs) > 0 {
		return nil, errs
	}
	body, aux, errs := elaborate.ExprIn(operand, ev.defs, ev.ck)
	if len(errs) > 0 {
		return nil, errs
	}
	if ev.ck.Intrinsics[types.StreamWithProducerName].Body != nil || ev.ck.Intrinsics[types.IteratorNextName].Body != nil {
		defs := ev.executionDefs(body, aux)
		p := ev.program(defs)
		if captureErrs := core.InferCaptures(p, ev.ck.B); len(captureErrs) > 0 {
			return nil, []diag.Error{diag.Errorf(operand.Span(), "INTERNAL CAPTURE INVARIANT", "%v", captureErrs[0])}
		}
		mp, lowerErrs := machineir.LowerStage(p, ev.ck.B)
		if len(lowerErrs) > 0 {
			return nil, []diag.Error{diag.Errorf(operand.Span(), "INTERNAL MACHINE INVARIANT", "%v", lowerErrs[0])}
		}
		if err := ev.env.DefineMachineProg(mp); err != nil {
			return nil, []diag.Error{infer.CompileTimeError(err, operand.Span())}
		}
	}
	for i := range aux {
		ev.env.DefineWorker(&aux[i])
	}
	v, err := eval.EvalCompileTime(context.Background(), body, ev.env, eval.DefaultBudget)
	if err != nil {
		return nil, []diag.Error{infer.CompileTimeError(err, operand.Span())}
	}
	return v, nil
}

// An in-progress deriving group can have a dictionary declaration whose
// methods are still being expanded. Such a dictionary cannot be called by the
// current operand. Check and lower the operand's dependency closure, retaining
// source order, rather than pretending the entire prefix is a finished module.
func (ev *evaluator) executionDefs(body core.Expr, aux []core.Def) []core.Def {
	all := append(append([]core.Def(nil), ev.defs...), aux...)
	root := core.Def{Name: "_stage_expression", Type: body.Type(), Control: core.ExprControl(body), Body: body}
	all = append(all, root)
	byName := make(map[string]*core.Def, len(all))
	for i := range all {
		byName[all[i].Name] = &all[i]
	}
	reached := map[string]bool{}
	queue := []string{root.Name}
	for len(queue) > 0 {
		name := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		if reached[name] {
			continue
		}
		reached[name] = true
		if def := byName[name]; def != nil {
			core.Inspect(def.Body, func(e core.Expr) {
				if ref, ok := e.(*core.VarRef); ok && !ref.Local {
					queue = append(queue, ref.Name)
				}
			})
		}
	}
	var defs []core.Def
	for i := range all {
		if reached[all[i].Name] && byName[all[i].Name] == &all[i] {
			defs = append(defs, all[i])
		}
	}
	return defs
}

// sync elaborates the already-inferred prefix on demand. Source-order scoping
// is the stage discipline, so everything a splice can name is already checked
// by the time it runs — the same property the REPL relies on per prompt.
func (ev *evaluator) sync() []diag.Error {
	var defs []core.Def
	var errs []diag.Error
	add := func(ds []core.Def, es []diag.Error) {
		defs = append(defs, ds...)
		errs = append(errs, es...)
	}
	missingIntrinsic := false
	for name := range ev.ck.Intrinsics {
		missingIntrinsic = missingIntrinsic || !ev.installedIntrinsics[name]
	}
	if missingIntrinsic {
		for _, d := range elaborate.IntrinsicDefs(ev.ck) {
			if !ev.installedIntrinsics[d.Name] {
				defs = append(defs, d)
			}
		}
	}
	nextInstances := len(ev.ck.Instances)
	if n := nextInstances; n > ev.installedInstances {
		add(elaborate.Instances(ev.ck.Instances[ev.installedInstances:], ev.ck))
	}
	nextDecls := len(ev.ck.Checked)
	for i := ev.installedDecls; i < nextDecls; i++ {
		context := append(append([]core.Def(nil), ev.defs...), defs...)
		add(elaborate.DeclIn(ev.ck.Checked[i], context, ev.ck))
	}
	if len(errs) > 0 {
		return errs
	}
	ev.env.DefineProg(&core.Prog{ADTs: ev.ck.ADTOrder, Defs: defs, Natives: ev.ck.Natives})
	if ev.installedIntrinsics == nil {
		ev.installedIntrinsics = map[string]bool{}
	}
	for name := range ev.ck.Intrinsics {
		ev.installedIntrinsics[name] = true
	}
	ev.defs = append(ev.defs, defs...)
	ev.installedDecls, ev.installedInstances = nextDecls, nextInstances
	return nil
}

func (ev *evaluator) program(defs []core.Def) *core.Prog {
	effects := make([]*types.EffectInfo, 0, len(ev.ck.EffectsByUnique))
	for _, effect := range ev.ck.EffectsByUnique {
		effects = append(effects, effect)
	}
	intrinsics := make(map[string]bool, len(ev.ck.Intrinsics))
	for name := range ev.ck.Intrinsics {
		intrinsics[name] = true
	}
	return &core.Prog{ADTs: ev.ck.ADTOrder, Effects: effects, Defs: defs, Natives: ev.ck.Natives, Intrinsics: intrinsics}
}
