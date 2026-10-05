package eval

import (
	"fmt"
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/runtime/fangort"
	"sync"
)

func (in *interp) asyncRebase(e *core.AsyncRebase, fr *Frame) (Value, error) {
	row, err := in.argumentRow(e.Call.Row, fr)
	if err != nil {
		return nil, err
	}
	bindings := []fangort.EvidenceBinding{}
	for _, arg := range e.Call.EvidenceArgs {
		ev := resolveEvidence(in.evidence[arg.Key()])
		if ev == nil {
			return nil, fmt.Errorf("Async: missing child handler %s", arg.Name)
		}
		args := make([]*fangort.TypeDescriptor, len(arg.Args))
		for i, ty := range arg.Args {
			args[i], err = in.typeDescriptor(ty, fr)
			if err != nil {
				return nil, err
			}
		}
		bindings = append(bindings, fangort.EvidenceBinding{Name: arg.Name, Arguments: args, Family: fangort.EvidenceFamily{Origin: ev.origin, Direct: ev, Exit: ev}})
	}
	fork := fangort.NewEvidenceFork(fangort.ExtendEvidenceRow(nil, bindings...))
	row = fork.Row(row)
	call := *e.Call
	call.Row = &core.RowArgument{From: -2}
	return in.eval(&call, &Frame{parent: fr, rows: rowEnv{-2: row}})
}

func (in *interp) asyncLaunch(e *core.AsyncLaunch, fr *Frame) (Value, error) {
	if in.compileTime {
		return nil, &UnsafeNativeError{Name: "Async.spawn", Reason: "starts concurrent execution"}
	}
	owner, err := in.eval(e.Call.Args[0], fr)
	if err != nil {
		return nil, err
	}
	if _, ok := asExit(owner); ok {
		return owner, nil
	}
	value, err := in.eval(e.Call.Callee, fr)
	if err != nil {
		return nil, err
	}
	if _, ok := asExit(value); ok {
		return value, nil
	}
	body := value.(*Closure)
	row, err := in.argumentRow(e.Call.Row, fr)
	if err != nil {
		return nil, err
	}
	// Publish inherited activations before the goroutine exists.
	fangort.ShareEvidenceRow(row)
	hostContext := in.hostContext
	if hostContext == nil {
		hostContext = in.ctx
	}
	task := fangort.SpawnAsync(owner.(*CtorVal).Fields[0].(*fangort.AsyncScope), func(scope *fangort.AsyncScope) fangort.AsyncCompletion {
		evidence := cloneEvidence(body.Evidence)
		child := &interp{ctx: hostContext, hostContext: hostContext, env: in.env, ioctx: in.ioctx, out: in.out, evidence: evidence, forcing: map[*Cell]bool{}, pollOwned: 1}
		rows, rowErr := child.bindInvocationRow(body.rowParam, body.rowEffects, row, evidence, body.Env)
		if rowErr != nil {
			panic(rowErr)
		}
		result, err := child.eval(body.Body, &Frame{parent: body.Env, vars: map[string]Value{body.Param: &CtorVal{Ctor: e.ScopeCtor, Fields: []Value{scope}}}, rows: rows})
		if err != nil {
			panic(err)
		}
		if _, ok := asExit(result); ok {
			panic("Async: child escaped its abort boundary")
		}
		outcome := result.(*CtorVal)
		if outcome.Ctor.Name == e.CancelledCtor.Name {
			return fangort.AsyncCompletion{Cancelled: true}
		}
		return fangort.AsyncCompletion{Value: fangort.PackNativeValue(outcome.Fields[0])}
	})
	return &CtorVal{Ctor: e.TaskCtor, Fields: []Value{task}}, nil
}

func (in *interp) parallelMap(e *core.ParallelMap, fr *Frame) (Value, error) {
	function, err := in.eval(e.Function, fr)
	if err != nil {
		return nil, err
	}
	if _, ok := asExit(function); ok {
		return function, nil
	}
	input, err := in.eval(e.Input, fr)
	if err != nil {
		return nil, err
	}
	if _, ok := asExit(input); ok {
		return input, nil
	}
	body := function.(*Closure)
	var failure error
	var firstFailure sync.Once
	result := fangort.ParallelMap(func(value Value) Value {
		child := &interp{ctx: in.ctx, hostContext: in.hostContext, env: in.env, ioctx: in.ioctx, out: in.out, evidence: cloneEvidence(body.Evidence), forcing: map[*Cell]bool{}, compileTime: in.compileTime, pollOwned: in.pollOwned}
		output, err := child.eval(body.Body, &Frame{parent: body.Env, vars: map[string]Value{body.Param: value}})
		if err == nil {
			if _, ok := asExit(output); ok {
				err = fmt.Errorf("Async.parMap: unexpected abort")
			}
		}
		if err != nil {
			firstFailure.Do(func() { failure = err })
			return nil
		}
		return output
	}, input.(fangort.List[Value]))
	if failure != nil {
		return nil, failure
	}
	return result, nil
}

func (in *interp) asyncSupervise(e *core.AsyncSupervise, fr *Frame) (Value, error) {
	in.pollOwned++
	defer func() { in.pollOwned-- }()
	return in.eval(e.Call, fr)
}
