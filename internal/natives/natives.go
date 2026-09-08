// Package natives is the interpreter-side registry for compiler-bundled
// native declarations. User sidecars deliberately do not enter this table:
// they run only through the compiled backend.
package natives

import (
	"bufio"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/meta"
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
type ExitError struct{ Code int }

func (e *ExitError) Error() string { return fmt.Sprintf("program exited with status %d", e.Code) }

func runtimePath(rt *Runtime, path string) string {
	if filepath.IsAbs(path) || rt.Dir == "" {
		return path
	}
	return filepath.Join(rt.Dir, path)
}

type Spec struct {
	Arity    int
	Effect   bool
	Foldable bool

	// CompileTimeSafe permits the compiler's own evaluator to run this
	// native while expanding a splice. Purity is not enough: `Random`'s
	// draws are pure in the effect row after `runSeeded` handles them away,
	// but they advance fangort's process-global PRNG cell, which the
	// compiler shares with the program it is compiling.
	CompileTimeSafe bool

	Eval func(*Runtime, []any) (any, error)
}

var Table = func() map[string]Spec {
	t := map[string]Spec{}
	installMeta(t)
	installScalarInstances(t)
	// The operator-named Basics values. The registry key is the canonical
	// symbol, so it wears the operator spelling; the EvalBasics tag stays
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
			return stdlib.EvalBasics(op, args[0], args[1], rt.Equal), nil
		}}
	}
	t["Basics.remainderBy"] = Spec{Arity: 2, Eval: func(_ *Runtime, args []any) (any, error) {
		return stdlib.RemainderBy(args[0].(int64), args[1].(int64)), nil
	}}
	t["String.length"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) {
		return stdlib.StringLength(args[0].(string)), nil
	}}
	t["String.byteLength"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) { return stdlib.StringByteLength(args[0].(string)), nil }}
	t["String.byteAt"] = Spec{Arity: 2, Eval: func(_ *Runtime, args []any) (any, error) {
		return stdlib.ByteAt(args[0].(int64), args[1].(string)), nil
	}}
	t["String.slice"] = Spec{Arity: 3, Eval: func(_ *Runtime, args []any) (any, error) {
		return stdlib.StringSlice(args[0].(int64), args[1].(int64), args[2].(string)), nil
	}}
	t["String.byteSlice"] = Spec{Arity: 3, Eval: func(_ *Runtime, args []any) (any, error) {
		return stdlib.StringByteSlice(args[0].(int64), args[1].(int64), args[2].(string)), nil
	}}
	t["String.firstChar"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) { return stdlib.StringFirst(args[0].(string)), nil }}
	t["String.restString"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) { return stdlib.StringRest(args[0].(string)), nil }}
	t["String.fromChar"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) { return stdlib.StringFromChar(args[0].(rune)), nil }}
	t["Json.jsonString"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) { return stdlib.JSONString(args[0].(string)), nil }}
	t["Json.jsonFloat"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) { return stdlib.JSONFloat(args[0].(float64)), nil }}
	t["Json.stringTokenLength"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) { return stdlib.JSONStringTokenLength(args[0].(string)), nil }}
	t["Json.stringTokenValue"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) { return stdlib.JSONStringValue(args[0].(string)), nil }}
	t["IO.lineText"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) {
		return stdlib.LineText(args[0].(string)), nil
	}}
	t["IO.lineEnding"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) {
		return stdlib.LineEnding(args[0].(string)), nil
	}}
	t["Random.swapSeed"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) {
		return stdlib.RandomSwap(args[0].(int64)), nil
	}}
	t["Random.nextInt"] = Spec{Arity: 2, Eval: func(_ *Runtime, args []any) (any, error) {
		return stdlib.RandomInt(args[0].(int64), args[1].(int64)), nil
	}}
	t["Random.entropySeed"] = Spec{Arity: 1, Eval: func(_ *Runtime, _ []any) (any, error) {
		return stdlib.RandomEntropy(), nil
	}}
	t["IO.print"] = Spec{Arity: 1, Effect: true, Eval: func(rt *Runtime, args []any) (any, error) {
		text, err := rt.Show(args[0])
		if err != nil {
			return nil, err
		}
		return struct{}{}, stdlib.PrintTo(rt.Writer, text)
	}}
	t["IO.hasInput"] = Spec{Arity: 1, Effect: true, Eval: func(rt *Runtime, _ []any) (any, error) {
		return stdlib.HasInputFrom(rt.Reader)
	}}
	t["IO.readRawLine"] = Spec{Arity: 1, Effect: true, Eval: func(rt *Runtime, _ []any) (any, error) {
		return stdlib.ReadRawLineFrom(rt.Reader)
	}}
	t["IO.write"] = Spec{Arity: 1, Effect: true, Eval: func(rt *Runtime, args []any) (any, error) {
		return struct{}{}, stdlib.WriteTo(rt.Writer, args[0].(string))
	}}
	t["IO.argCount"] = Spec{Arity: 1, Effect: true, Eval: func(rt *Runtime, _ []any) (any, error) {
		return int64(len(rt.Args)), nil
	}}
	t["IO.argAt"] = Spec{Arity: 1, Effect: true, Eval: func(rt *Runtime, args []any) (any, error) {
		return stdlib.ArgAt(rt.Args, args[0].(int64))
	}}
	t["IO.pathExists"] = Spec{Arity: 1, Effect: true, Eval: func(rt *Runtime, args []any) (any, error) {
		return stdlib.PathExists(runtimePath(rt, args[0].(string)))
	}}
	t["IO.readFileText"] = Spec{Arity: 1, Effect: true, Eval: func(rt *Runtime, args []any) (any, error) {
		return stdlib.ReadFileText(runtimePath(rt, args[0].(string)))
	}}
	t["IO.writeFile"] = Spec{Arity: 2, Effect: true, Eval: func(rt *Runtime, args []any) (any, error) {
		return struct{}{}, stdlib.WriteFileText(runtimePath(rt, args[0].(string)), args[1].(string))
	}}
	t["IO.exit"] = Spec{Arity: 1, Effect: true, Eval: func(_ *Runtime, args []any) (any, error) {
		return nil, &ExitError{Code: int(args[0].(int64))}
	}}
	// Bundled natives are compile-time-safe by default: they are pure
	// functions of their arguments. Random is the exclusion — its draws read
	// and advance a process-global cell the compiler shares.
	for name, spec := range t {
		spec.CompileTimeSafe = !spec.Effect && !strings.HasPrefix(name, "Random.")
		t[name] = spec
	}
	return t
}()

func Lookup(name string) (Spec, bool) { spec, ok := Table[name]; return spec, ok }
