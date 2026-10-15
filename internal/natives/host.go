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
	t["Console.inputHasBytes"] = Spec{Arity: 1, Eval: func(rt *Runtime, _ []any) (any, error) {
		value, err := fangort.ConsoleHasInput(rt.Host)
		return ioAnswer(value, err)
	}}
	t["Console.readInputLine"] = Spec{Arity: 1, Eval: func(rt *Runtime, _ []any) (any, error) {
		value, err := fangort.ReadConsoleLine(rt.Host)
		return ioAnswer(value, err)
	}}
	t["Console.readInputBytes"] = Spec{Arity: 1, Eval: func(rt *Runtime, args []any) (any, error) {
		value, err := fangort.ReadConsoleBytes(rt.Host, args[0].(int64))
		return ioAnswer(value, err)
	}}
	t["Console.writeOutputBytes"] = Spec{Arity: 2, Eval: func(rt *Runtime, args []any) (any, error) {
		return ioAnswer(struct{}{}, fangort.WriteConsoleBytes(rt.Host, args[0].(int64), args[1].([]byte)))
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
	t["Process.exitWith"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) {
		return nil, &ExitError{Code: int(args[0].(int64))}
	}}
	t["Process.checkedStatus"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) {
		code := args[0].(int64)
		if code < 0 || code > 255 {
			panic(fmt.Sprintf("exit status %d is out of range 0 through 255", code))
		}
		return code, nil
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
