package eval

import (
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/runtime/fangort"
)

func (in *interp) spawnTask(e *core.TaskSpawn, fr *Frame) (Value, error) {
	if in.compileTime {
		return nil, &UnsafeNativeError{Name: "Task.spawn", Reason: "starts concurrent execution"}
	}
	scope, err := in.eval(e.Scope, fr)
	if err != nil {
		return nil, err
	}
	if exit, ok := asExit(scope); ok {
		return exit, nil
	}
	input, err := in.eval(e.Input, fr)
	if err != nil {
		return nil, err
	}
	if exit, ok := asExit(input); ok {
		return exit, nil
	}
	descriptors, err := in.instantiateDescriptors(in.env.workers[e.Worker].TyParams, e.TyArgs, fr)
	if err != nil {
		return nil, err
	}
	hostContext := in.hostContext
	if hostContext == nil {
		hostContext = in.ctx
	}
	task := fangort.SpawnTaskIn(scope.(*CtorVal).Fields[0].(*fangort.TaskScope), hostContext, func(ctx *fangort.TaskContext) any {
		child := &interp{ctx: ctx.Context, hostContext: hostContext, env: in.env, ioctx: in.ioctx, out: in.out, evidence: map[int]*evidence{}, forcing: map[*Cell]bool{}}
		// Task cancellation is explicit. Interpreter host polling must not insert
		// additional source cancellation points into CPU loops.
		child.pollOwned = 1
		def := in.env.workers[e.Worker]
		vars := map[string]Value{def.Params[0]: &CtorVal{Ctor: e.ContextCtor, Fields: []Value{ctx}}, def.Params[1]: input}
		value, err := child.eval(def.Body, &Frame{vars: vars, types: descriptors})
		if err != nil {
			panic(err)
		}
		if _, ok := asExit(value); ok {
			panic("unhandled task effect")
		}
		return fangort.PackNativeValue(value)
	})
	return &CtorVal{Ctor: e.TaskCtor, Fields: []Value{task}}, nil
}
