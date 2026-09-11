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
		ev.installedIntrinsics = false
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
	installedIntrinsics bool
}

func (ev *evaluator) run(operand ast.Expr) (any, []diag.Error) {
	if errs := ev.sync(); len(errs) > 0 {
		return nil, errs
	}
	body, aux, errs := elaborate.Expr(operand, ev.ck)
	if len(errs) > 0 {
		return nil, errs
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
	if !ev.installedIntrinsics {
		defs = append(defs, elaborate.IntrinsicDefs(ev.ck)...)
		ev.installedIntrinsics = true
	}
	if n := len(ev.ck.Instances); n > ev.installedInstances {
		add(elaborate.Instances(ev.ck.Instances[ev.installedInstances:], ev.ck))
		ev.installedInstances = n
	}
	for ; ev.installedDecls < len(ev.ck.Checked); ev.installedDecls++ {
		add(elaborate.Decl(ev.ck.Checked[ev.installedDecls], ev.ck))
	}
	if len(errs) > 0 {
		return errs
	}
	ev.env.DefineProg(&core.Prog{Defs: defs})
	return nil
}
