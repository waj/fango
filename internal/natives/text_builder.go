package natives

import "github.com/waj/fango/runtime/fangort"

func installTextBuilder(t map[string]Spec) {
	t["Text.Builder.emptyBuffer"] = Spec{Arity: 1, CompileTimeSafe: true, Eval: func(_ *Runtime, _ []any) (any, error) { return nil, nil }}
	t["Text.Builder.zero"] = Spec{Arity: 1, CompileTimeSafe: true, Eval: func(_ *Runtime, _ []any) (any, error) { return int64(0), nil }}
	t["Text.Builder.sizeIsEmpty"] = Spec{Arity: 1, CompileTimeSafe: true, Eval: func(_ *Runtime, args []any) (any, error) { return args[0].(int64) == 0, nil }}
	t["Text.Builder.bufferAppend"] = Spec{Arity: 3, CompileTimeSafe: true, Eval: func(_ *Runtime, args []any) (any, error) {
		return fangort.TextBufferAppend(args[0], args[1].(int64), args[2].(string)), nil
	}}
	t["Text.Builder.bufferAppendChar"] = Spec{Arity: 3, CompileTimeSafe: true, Eval: func(_ *Runtime, args []any) (any, error) {
		return fangort.TextBufferAppendChar(args[0], args[1].(int64), args[2].(rune)), nil
	}}
	t["Text.Builder.bufferAppendInt"] = Spec{Arity: 3, CompileTimeSafe: true, Eval: func(_ *Runtime, args []any) (any, error) {
		return fangort.TextBufferAppendInt(args[0], args[1].(int64), args[2].(int64)), nil
	}}
	t["Text.Builder.bufferAppendFloat"] = Spec{Arity: 3, CompileTimeSafe: true, Eval: func(_ *Runtime, args []any) (any, error) {
		return fangort.TextBufferAppendFloat(args[0], args[1].(int64), args[2].(float64)), nil
	}}
	t["Text.Builder.bufferLength"] = Spec{Arity: 1, CompileTimeSafe: true, Eval: func(_ *Runtime, args []any) (any, error) { return fangort.TextBufferLength(args[0]), nil }}
	t["Text.Builder.bufferText"] = Spec{Arity: 2, CompileTimeSafe: true, Eval: func(_ *Runtime, args []any) (any, error) {
		return fangort.TextBufferText(args[0], args[1].(int64)), nil
	}}
}
