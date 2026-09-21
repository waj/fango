package natives

import (
	"github.com/waj/fango/runtime/fangort"
)

// The bundled Bytes module's inline templates, as the interpreter runs them.
// Generated Go inlines the same fangort helpers; where a list crosses the
// boundary the interpreter's elements are erased to `any`, so these reach for
// the `…ValueList`/`…Values` spelling of the one shared implementation.
func installBytes(t map[string]Spec) {
	t["Bytes.length"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) {
		return fangort.BytesLength(args[0].(fangort.Bytes)), nil
	}}
	t["Bytes.byteAtRaw"] = Spec{Arity: 2, Eval: func(_ *Runtime, args []any) (any, error) {
		return fangort.BytesByteAt(args[0].(int64), args[1].(fangort.Bytes)), nil
	}}
	t["Bytes.slice"] = Spec{Arity: 3, Eval: func(_ *Runtime, args []any) (any, error) {
		return fangort.BytesSlice(args[0].(int64), args[1].(int64), args[2].(fangort.Bytes)), nil
	}}
	t["Bytes.append"] = Spec{Arity: 2, Eval: func(_ *Runtime, args []any) (any, error) {
		return fangort.BytesAppend(args[0].(fangort.Bytes), args[1].(fangort.Bytes)), nil
	}}
	t["Bytes.concat"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) {
		return fangort.BytesConcatValues(args[0].(fangort.List[any])), nil
	}}
	t["Bytes.indexOfRaw"] = Spec{Arity: 2, Eval: func(_ *Runtime, args []any) (any, error) {
		return fangort.BytesIndexOf(args[0].(fangort.Bytes), args[1].(fangort.Bytes)), nil
	}}
	t["Bytes.indexOfFromRaw"] = Spec{Arity: 3, Eval: func(_ *Runtime, args []any) (any, error) {
		return fangort.BytesIndexOfFrom(args[0].(int64), args[1].(fangort.Bytes), args[2].(fangort.Bytes)), nil
	}}
	t["Bytes.startsWith"] = Spec{Arity: 2, Eval: func(_ *Runtime, args []any) (any, error) {
		return fangort.BytesStartsWith(args[0].(fangort.Bytes), args[1].(fangort.Bytes)), nil
	}}
	t["Bytes.fromList"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) {
		return fangort.BytesFromValueList(args[0].(fangort.List[any])), nil
	}}
	t["Bytes.toList"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) {
		return fangort.BytesToValueList(args[0].(fangort.Bytes)), nil
	}}
	t["Bytes.fromString"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) {
		return fangort.BytesFromString(args[0].(string)), nil
	}}
	t["Bytes.isUtf8"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) {
		return fangort.BytesIsUtf8(args[0].(fangort.Bytes)), nil
	}}
	t["Bytes.unvalidatedString"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) {
		return fangort.BytesUnvalidatedString(args[0].(fangort.Bytes)), nil
	}}
	t["Bytes.toStringLossy"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) {
		return fangort.BytesToStringLossy(args[0].(fangort.Bytes)), nil
	}}
	t["Bytes.bytesEq"] = Spec{Arity: 2, Eval: func(_ *Runtime, args []any) (any, error) {
		return fangort.BytesEq(args[0].(fangort.Bytes), args[1].(fangort.Bytes)), nil
	}}
	t["Bytes.bytesLt"] = Spec{Arity: 2, Eval: func(_ *Runtime, args []any) (any, error) {
		return fangort.BytesLt(args[0].(fangort.Bytes), args[1].(fangort.Bytes)), nil
	}}
	t["Bytes.bytesGt"] = Spec{Arity: 2, Eval: func(_ *Runtime, args []any) (any, error) {
		return fangort.BytesGt(args[0].(fangort.Bytes), args[1].(fangort.Bytes)), nil
	}}
	t["Bytes.bytesLe"] = Spec{Arity: 2, Eval: func(_ *Runtime, args []any) (any, error) {
		return fangort.BytesLe(args[0].(fangort.Bytes), args[1].(fangort.Bytes)), nil
	}}
	t["Bytes.bytesGe"] = Spec{Arity: 2, Eval: func(_ *Runtime, args []any) (any, error) {
		return fangort.BytesGe(args[0].(fangort.Bytes), args[1].(fangort.Bytes)), nil
	}}
	t["Bytes.bytesShow"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) {
		return fangort.BytesShow(args[0].(fangort.Bytes)), nil
	}}
}
