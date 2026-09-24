package eval

import (
	"fmt"
	"github.com/waj/fango/internal/core"
)

type workOwner struct{ closed bool }
type workPackage struct {
	owner  *workOwner
	cursor Value
}

func (in *interp) evalWork(e *core.Work, fr *Frame) (Value, error) {
	args := make([]Value, len(e.Args))
	for i, arg := range e.Args {
		v, err := in.eval(arg, fr)
		if err != nil {
			return nil, err
		}
		if _, ok := asExit(v); ok {
			return v, nil
		}
		args[i] = v
	}
	switch e.Kind {
	case "registration", "invocation-slot", "invocation-argument":
		return args[0], nil
	case "registry-owner":
		return args[0].(*MachineIteratorSession).work, nil
	case "register":
		scope := args[0].(*MachineIteratorSession)
		cursor, err := registerCoroutine(scope, in.openCoroutine(in.env.machine, args[1].(*Closure), args[0].(*MachineIteratorSession).evidence.Row()))
		if err != nil {
			return nil, err
		}
		return &workPackage{owner: scope.work, cursor: cursor}, nil
	case "create":
		return registerCoroutine(args[0].(*MachineIteratorSession), in.openCoroutine(in.env.machine, args[1].(*Closure), args[0].(*MachineIteratorSession).evidence.Row()))
	case "begin":
		return &workOwner{}, nil
	case "facet":
		return args[0], nil
	case "end":
		args[0].(*workOwner).closed = true
		return struct{}{}, nil
	case "pack":
		owner, ok := args[0].(*workOwner)
		if !ok || owner.closed {
			return nil, fmt.Errorf("eval: work package has no live owner")
		}
		return &workPackage{owner: owner, cursor: args[1]}, nil
	case "open":
		owner, ok := args[0].(*workOwner)
		work, valid := args[1].(*workPackage)
		if !ok || !valid || owner.closed || work.owner != owner {
			return nil, fmt.Errorf("eval: work package belongs to a different owner")
		}
		return work.cursor, nil
	}
	return nil, fmt.Errorf("eval: invalid work operation %q", e.Kind)
}
