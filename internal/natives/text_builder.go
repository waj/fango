package natives

import textnative "github.com/waj/fango/stdlib/Text"

func installTextBuilder(t map[string]Spec) {
	t["Text.Builder.emptyBuffer"] = Spec{Arity: 1, CompileTimeSafe: true, Eval: func(_ *Runtime, _ []any) (any, error) { return textnative.EmptyBuffer(), nil }}
	t["Text.Builder.zero"] = Spec{Arity: 1, CompileTimeSafe: true, Eval: func(_ *Runtime, _ []any) (any, error) { return textnative.Zero(), nil }}
	t["Text.Builder.sizeIsEmpty"] = Spec{Arity: 1, CompileTimeSafe: true, Eval: func(_ *Runtime, args []any) (any, error) { return args[0].(int64) == 0, nil }}
	t["Text.Builder.bufferAppend"] = Spec{Arity: 3, CompileTimeSafe: true, Eval: func(_ *Runtime, args []any) (any, error) {
		return textnative.BufferAppend(args[0], args[1].(int64), args[2].(string)), nil
	}}
	t["Text.Builder.bufferAppendChar"] = Spec{Arity: 3, CompileTimeSafe: true, Eval: func(_ *Runtime, args []any) (any, error) {
		return textnative.BufferAppendChar(args[0], args[1].(int64), args[2].(rune)), nil
	}}
	t["Text.Builder.bufferAppendInt"] = Spec{Arity: 3, CompileTimeSafe: true, Eval: func(_ *Runtime, args []any) (any, error) {
		return textnative.BufferAppendInt(args[0], args[1].(int64), args[2].(int64)), nil
	}}
	t["Text.Builder.bufferAppendFloat"] = Spec{Arity: 3, CompileTimeSafe: true, Eval: func(_ *Runtime, args []any) (any, error) {
		return textnative.BufferAppendFloat(args[0], args[1].(int64), args[2].(float64)), nil
	}}
	t["Text.Builder.bufferLength"] = Spec{Arity: 1, CompileTimeSafe: true, Eval: func(_ *Runtime, args []any) (any, error) { return textnative.BufferLength(args[0]), nil }}
	t["Text.Builder.bufferText"] = Spec{Arity: 2, CompileTimeSafe: true, Eval: func(_ *Runtime, args []any) (any, error) {
		return textnative.BufferText(args[0], args[1].(int64)), nil
	}}
	t["Text.Builder.intToString"] = Spec{Arity: 1, CompileTimeSafe: true, Eval: func(_ *Runtime, args []any) (any, error) { return textnative.IntToString(args[0].(int64)), nil }}
	t["Text.Builder.floatToString"] = Spec{Arity: 1, CompileTimeSafe: true, Eval: func(_ *Runtime, args []any) (any, error) { return textnative.FloatToString(args[0].(float64)), nil }}
	t["Text.Builder.charToString"] = Spec{Arity: 1, CompileTimeSafe: true, Eval: func(_ *Runtime, args []any) (any, error) { return textnative.CharToString(args[0].(rune)), nil }}
	t["Text.Builder.stringLiteral"] = Spec{Arity: 1, CompileTimeSafe: true, Eval: func(_ *Runtime, args []any) (any, error) { return textnative.StringLiteral(args[0].(string)), nil }}
	t["Text.Builder.charLiteral"] = Spec{Arity: 1, CompileTimeSafe: true, Eval: func(_ *Runtime, args []any) (any, error) { return textnative.CharLiteral(args[0].(rune)), nil }}
	t["Text.Builder.bufferAppendStringLiteral"] = Spec{Arity: 3, CompileTimeSafe: true, Eval: func(_ *Runtime, args []any) (any, error) {
		return textnative.BufferAppendStringLiteral(args[0], args[1].(int64), args[2].(string)), nil
	}}
	t["Text.Builder.bufferAppendCharLiteral"] = Spec{Arity: 3, CompileTimeSafe: true, Eval: func(_ *Runtime, args []any) (any, error) {
		return textnative.BufferAppendCharLiteral(args[0], args[1].(int64), args[2].(rune)), nil
	}}
}
