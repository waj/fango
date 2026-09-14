package core

import (
	"fmt"

	"github.com/waj/fango/internal/types"
)

// verifyIteratorOwnership checks the initial E8 lexical-consumer discipline.
// The source API is not installed yet, but keeping the proof at typed Core
// makes it independent of parser sugar and shared by batch, REPL, interpreter,
// and generated-code entry paths once the declarations are enabled.
func verifyIteratorOwnership(p *Prog) []error {
	if !p.Intrinsics[types.GeneratorWithIteratorName] {
		return nil
	}
	var errs []error
	identity := func(t types.Type) types.Type { return t }
	for i := range p.Defs {
		d := &p.Defs[i]
		Rewrite(d.Body, identity, func(e Expr) Expr {
			app, ok := e.(*App)
			if !ok || app.CalleeKind != Worker || len(app.Args) != 2 {
				return e
			}
			ref, ok := app.Callee.(*VarRef)
			if !ok || ref.Name != types.GeneratorWithIteratorName {
				return e
			}
			consumer, ok := app.Args[1].(*Lambda)
			if !ok {
				errs = append(errs, fmt.Errorf("def %s: iterator consumer must be a lexical lambda", d.Name))
				return e
			}
			errs = append(errs, verifyIteratorConsumer(d.Name, consumer)...)
			return e
		})
	}
	return errs
}

func verifyIteratorConsumer(defName string, consumer *Lambda) []error {
	identity := func(t types.Type) types.Type { return t }
	allowed, total := 0, 0
	Rewrite(consumer.Body, identity, func(e Expr) Expr {
		if ref, ok := e.(*VarRef); ok && ref.Local && ref.Name == consumer.Param {
			total++
		}
		app, ok := e.(*App)
		if !ok || app.CalleeKind != Worker || len(app.Args) == 0 {
			return e
		}
		callee, ok := app.Callee.(*VarRef)
		if !ok || !types.IteratorConsumer(callee.Name) {
			return e
		}
		cursor, ok := app.Args[len(app.Args)-1].(*VarRef)
		if ok && cursor.Local && cursor.Name == consumer.Param {
			allowed++
		}
		return e
	})
	var errs []error
	if total != allowed {
		errs = append(errs, fmt.Errorf("def %s: iterator cursor %q escapes or is used without a consuming combinator", defName, consumer.Param))
	}
	if allowed > 1 {
		errs = append(errs, fmt.Errorf("def %s: iterator cursor %q is consumed %d times", defName, consumer.Param, allowed))
	}
	return errs
}
