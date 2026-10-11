package natives

import (
	"fmt"

	"github.com/waj/fango/runtime/fangort"
	stdlib "github.com/waj/fango/stdlib"
)

// installHost preserves in-process and WASM execution where no native worker
// is available. These adapters observe the host and are never stage-safe.
func installHost(t map[string]Spec) {
	installTimeHost(t)
	t["IO.handleHasInput"] = Spec{Arity: 1, Eval: func(rt *Runtime, args []any) (any, error) {
		value, err := fangort.IOHandleHasInput(rt.Host, args[0])
		return ioAnswer(value, err)
	}}
	t["IO.readHandleLine"] = Spec{Arity: 1, Eval: func(rt *Runtime, args []any) (any, error) {
		value, err := fangort.ReadIOHandleLine(rt.Host, args[0])
		return ioAnswer(value, err)
	}}
	t["IO.readHandleBytes"] = Spec{Arity: 2, Eval: func(rt *Runtime, args []any) (any, error) {
		value, err := fangort.ReadIOHandleBytes(rt.Host, args[0], args[1].(int64))
		return ioAnswer(value, err)
	}}
	t["IO.writeHandle"] = Spec{Arity: 2, Eval: func(rt *Runtime, args []any) (any, error) {
		return ioAnswer(struct{}{}, fangort.WriteIOHandleBytes(rt.Host, args[0], []byte(args[1].(string))))
	}}
	t["IO.writeHandleBytes"] = Spec{Arity: 2, Eval: func(rt *Runtime, args []any) (any, error) {
		return ioAnswer(struct{}{}, fangort.WriteIOHandleBytes(rt.Host, args[0], args[1].([]byte)))
	}}
	t["Console.panic"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) {
		panic(args[0].(string))
	}}
	t["Process.argCount"] = Spec{Arity: 1, Eval: func(rt *Runtime, _ []any) (any, error) {
		return int64(len(rt.Args)), nil
	}}
	t["Process.argAt"] = Spec{Arity: 1, Eval: func(rt *Runtime, args []any) (any, error) {
		index := args[0].(int64)
		if index < 0 || index >= int64(len(rt.Args)) {
			panic(fmt.Sprintf("argument index %d is out of range", index))
		}
		return rt.Args[index], nil
	}}
	t["Process.exit"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) {
		return nil, &ExitError{Code: int(args[0].(int64))}
	}}
	t["Random.entropySeed"] = Spec{Arity: 1, Eval: func(_ *Runtime, _ []any) (any, error) {
		return stdlib.EntropySeed(), nil
	}}
}

// IO uses the same classified outcome as sidecar calls in the worker.
func ioAnswer(value any, err error) (any, error) {
	if err != nil {
		return fangort.ClassifyIOError(err), nil
	}
	return value, nil
}
