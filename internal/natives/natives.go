// Package natives is the in-process registry for inline compiler-bundled
// primitives and compile-time-safe adapters. Ordinary call-form sidecars run
// through internal/nativehost instead.
package natives

import (
	"bufio"
	"fmt"
	"io"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/meta"
	"github.com/waj/fango/internal/nativehost"
	stdlib "github.com/waj/fango/stdlib"
)

type Runtime struct {
	Reader *bufio.Reader
	Writer io.Writer
	Args   []string
	Dir    string
	Equal  func(any, any) bool
	Show   func(any) (string, error)

	// Expand renders a compile-time code value as surface AST. Only the
	// compiler's own evaluator supplies it: the Meta natives that build code
	// need their arguments as trees, and the template table lives with the
	// checker (doc/design.md, "Compile-time metaprogramming").
	Expand func(*meta.Code) ast.Expr
}

// ExitError is how the interpreter represents IO.exit without terminating
// the compiler or test process that hosts it. A compiled program calls
// os.Exit through fangort and therefore has the same observable status.
type ExitError = nativehost.ExitError

type Spec struct {
	Arity    int
	Effect   bool
	Foldable bool

	// CompileTimeSafe permits the compiler's own evaluator to run this
	// native while expanding a splice. Entropy remains excluded; deterministic
	// Random transitions are pure functions over handler-local state.
	CompileTimeSafe bool

	Eval func(*Runtime, []any) (any, error)
}

var Table = func() map[string]Spec {
	t := map[string]Spec{}
	installMeta(t)
	installScalarInstances(t)
	// The operator-named Basics values. The registry key is the canonical
	// symbol, so it wears the operator spelling; the evaluator tag stays
	// the alphabetic name of the scalar operation it dispatches to.
	for spelling, op := range map[string]string{
		"+": "add", "-": "sub", "*": "mul", "/": "fdiv", "++": "append",
		"==": "eq", "/=": "neq", "<": "lt", ">": "gt", "<=": "le", ">=": "ge",
	} {
		op := op
		t["Basics."+spelling] = Spec{Arity: 2, Foldable: op == "add" || op == "sub" || op == "mul" || op == "fdiv", Eval: func(rt *Runtime, args []any) (any, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("native Basics.%s expects 2 arguments", op)
			}
			return evalBasics(op, args[0], args[1], rt.Equal), nil
		}}
	}
	t["Basics.remainderBy"] = Spec{Arity: 2, Eval: func(_ *Runtime, args []any) (any, error) {
		return args[1].(int64) % args[0].(int64), nil
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
	t["Json.jsonString"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) { return stdlib.JsonString(args[0].(string)), nil }}
	t["Json.jsonFloat"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) { return stdlib.JsonFloat(args[0].(float64)), nil }}
	t["Json.stringTokenLength"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) { return stdlib.StringTokenLength(args[0].(string)), nil }}
	t["Json.stringTokenValue"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) { return stdlib.StringTokenValue(args[0].(string)), nil }}
	t["IO.lineText"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) {
		return lineText(args[0].(string)), nil
	}}
	t["IO.lineEnding"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) {
		return lineEnding(args[0].(string)), nil
	}}
	t["Random.entropySeed"] = Spec{Arity: 1, Eval: func(_ *Runtime, _ []any) (any, error) {
		return stdlib.EntropySeed(), nil
	}}
	// Bundled natives are compile-time-safe by default: they are pure
	// functions of their arguments. System entropy is Random's one exclusion;
	// seeded draws now use explicit handler-local state.
	for name, spec := range t {
		spec.CompileTimeSafe = !spec.Effect && name != "Random.entropySeed"
		t[name] = spec
	}
	return t
}()

func Lookup(name string) (Spec, bool) { spec, ok := Table[name]; return spec, ok }
