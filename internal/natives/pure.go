package natives

import (
	"github.com/waj/fango/runtime/fangort"
	stdlib "github.com/waj/fango/stdlib"
)

// installPure supplies bundled inline-template implementations and the
// deterministic sidecar adapters used by staging and the browser interpreter.
// This is an opt-in execution policy, not a catalog of required sidecar entries.
func installPure(t map[string]Spec) {
	// The two operator-named Basics natives. The registry key is the
	// canonical symbol, so it wears the operator spelling.
	t["Basics./"] = Spec{Arity: 2, Foldable: true, Eval: func(_ *Runtime, args []any) (any, error) {
		return args[0].(float64) / args[1].(float64), nil
	}}
	t["Basics.++"] = Spec{Arity: 2, Eval: func(_ *Runtime, args []any) (any, error) {
		return args[0].(string) + args[1].(string), nil
	}}
	t["Basics.remainderBy"] = Spec{Arity: 2, Eval: func(_ *Runtime, args []any) (any, error) {
		return args[1].(int64) % args[0].(int64), nil
	}}
	// Not Foldable, as remainderBy is not: folding a zero divisor would turn a
	// runtime crash into a compiler crash.
	t["Basics.quotientBy"] = Spec{Arity: 2, Eval: func(_ *Runtime, args []any) (any, error) {
		return args[1].(int64) / args[0].(int64), nil
	}}
	t["String.length"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) {
		return stdlib.Length(args[0].(string)), nil
	}}
	t["String.byteLength"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) { return stdlib.ByteLength(args[0].(string)), nil }}
	t["String.byteAt"] = Spec{Arity: 2, Eval: func(_ *Runtime, args []any) (any, error) {
		return stdlib.ByteAt(args[0].(int64), args[1].(string)), nil
	}}
	t["String.slice"] = Spec{Arity: 3, Eval: func(_ *Runtime, args []any) (any, error) {
		return stdlib.Slice(args[0].(int64), args[1].(int64), args[2].(string)), nil
	}}
	t["String.byteSlice"] = Spec{Arity: 3, Eval: func(_ *Runtime, args []any) (any, error) {
		return stdlib.ByteSlice(args[0].(int64), args[1].(int64), args[2].(string)), nil
	}}
	t["String.firstChar"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) { return stdlib.FirstChar(args[0].(string)), nil }}
	t["String.restString"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) { return stdlib.RestString(args[0].(string)), nil }}
	t["String.fromChar"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) { return stdlib.FromChar(args[0].(rune)), nil }}
	t["String.fromList"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) {
		return fangort.StringFromValueList(args[0].(fangort.List[any])), nil
	}}
	t["String.concat"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) {
		return fangort.StringConcatValues(args[0].(fangort.List[any])), nil
	}}
	t["String.toFloatNative"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) { return stdlib.ToFloatNative(args[0].(string)), nil }}
	t["Char.toCode"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) {
		return stdlib.ToCode(args[0].(rune)), nil
	}}
	t["Char.scalar"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) {
		return stdlib.Scalar(args[0].(int64)), nil
	}}
	t["Encoding.utf8CodeAt"] = Spec{Arity: 2, Eval: func(_ *Runtime, args []any) (any, error) {
		return stdlib.Utf8CodeAt(args[0].(fangort.Bytes), args[1].(int64)), nil
	}}
	t["Encoding.utf8SpanUntil"] = Spec{Arity: 3, Eval: func(_ *Runtime, args []any) (any, error) {
		return stdlib.Utf8SpanUntil(args[0].(fangort.Bytes), args[1].(fangort.Bytes), args[2].(int64)), nil
	}}
	t["Encoding.utf8MatchAt"] = Spec{Arity: 3, Eval: func(_ *Runtime, args []any) (any, error) {
		return stdlib.Utf8MatchAt(args[0].(fangort.Bytes), args[1].(int64), args[2].(string)), nil
	}}
	t["IO.lineText"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) {
		return lineText(args[0].(string)), nil
	}}
	t["IO.lineEnding"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) {
		return lineEnding(args[0].(string)), nil
	}}
	t["IO.standardHandle"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) {
		return fangort.StandardIOHandle(args[0].(int64)), nil
	}}
}
