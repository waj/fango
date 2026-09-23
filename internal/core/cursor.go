package core

import (
	"fmt"

	"github.com/waj/fango/internal/types"
)

// CheckCursorResult verifies the nominal element/result packaging shared by
// semantic Core and the independently checked advancement instruction.
func CheckCursorResult(cursor, result types.Type, adt *types.ADTInfo) error {
	c, ok := cursor.(*types.TCon)
	if !ok || c.Name != types.IteratorTypeName || len(c.Args) != 2 {
		return fmt.Errorf("advancement requires an Iterator cursor")
	}
	r, ok := result.(*types.TCon)
	if !ok || r.Name != "Maybe.Maybe" || len(r.Args) != 1 || !types.Equal(r.Args[0], c.Args[0]) || adt == nil || adt.Con == nil || adt.Con.Unique != r.Unique || len(adt.Params) != 1 || len(adt.Ctors) != 2 {
		return fmt.Errorf("cursor advancement has an invalid Maybe result")
	}
	if adt.Ctors[0] == nil || adt.Ctors[1] == nil || adt.Ctors[0].Name != "Maybe.Nothing" || adt.Ctors[1].Name != "Maybe.Just" || len(adt.Ctors[0].Fields) != 0 || len(adt.Ctors[1].Fields) != 1 || !types.Equal(adt.InstFields(adt.Ctors[1], r.Args)[0], c.Args[0]) {
		return fmt.Errorf("cursor advancement has invalid Maybe constructors")
	}
	return nil
}

// CheckCoroutineProducer proves both edges of the scoped callback. No protocol
// type is recovered from an erased runtime value.
func CheckCoroutineProducer(cursor types.Type, producer types.Type) error {
	request, reply, result, ok := types.CoroutineProtocol(cursor)
	factory, fn := producer.(*types.TFun)
	if !ok || !fn {
		return fmt.Errorf("invalid coroutine owner/producer type")
	}
	pause, p := factory.Arg.(*types.TFun)
	body, b := factory.Ret.(*types.TFun)
	if !p || !b || types.FunctionControl(factory).Transport != types.Direct ||
		!EqualValueRepresentation(pause.Arg, request) || !EqualValueRepresentation(pause.Ret, reply) || types.FunctionControl(pause).Transport != types.Machine ||
		!EqualValueRepresentation(body.Arg, reply) || !EqualValueRepresentation(body.Ret, result) || types.FunctionControl(body).Transport != types.Machine {
		return fmt.Errorf("coroutine producer has an inconsistent request/reply/result protocol")
	}
	return nil
}

func CheckCoroutineAdvance(cursor types.Type, reply Expr, close bool, result types.Type, adt *types.ADTInfo) error {
	request, input, output, ok := types.CoroutineProtocol(cursor)
	if !ok {
		return fmt.Errorf("advance requires a Coroutine owner")
	}
	if close {
		u, unit := result.(*types.TCon)
		if reply != nil || adt != nil || !unit || u.Name != "()" {
			return fmt.Errorf("invalid coroutine close protocol")
		}
		return nil
	}
	r, step := result.(*types.TCon)
	if reply == nil || !EqualValueRepresentation(reply.Type(), input) {
		return fmt.Errorf("coroutine reply type disagrees with its owner")
	}
	if !step || r.Name != types.CoroutineStepName || len(r.Args) != 2 || !EqualValueRepresentation(r.Args[0], request) || !EqualValueRepresentation(r.Args[1], output) || adt == nil || adt.Con == nil || adt.Con.Unique != r.Unique || len(adt.Params) != 2 || len(adt.Ctors) != 3 {
		return fmt.Errorf("invalid coroutine Step result")
	}
	for i, name := range []string{"Coroutine.Suspended", "Coroutine.Finished", "Coroutine.Closed"} {
		c := adt.Ctors[i]
		if c == nil || c.Name != name {
			return fmt.Errorf("invalid coroutine Step constructors")
		}
		fields := adt.InstFields(c, r.Args)
		if i == 2 {
			if len(fields) != 0 {
				return fmt.Errorf("Closed carries a value")
			}
			continue
		}
		if len(fields) != 1 || !EqualValueRepresentation(fields[0], r.Args[i]) {
			return fmt.Errorf("mistyped coroutine Step constructor")
		}
	}
	return nil
}
