// Package natives is the interpreter-side registry for compiler-bundled
// native declarations. User sidecars deliberately do not enter this table:
// they run only through the compiled backend.
package natives

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/meta"
	"github.com/waj/fango/internal/types"
	stdlib "github.com/waj/fango/stdlib"
)

type Runtime struct {
	Reader *bufio.Reader
	Writer io.Writer
	Equal  func(any, any) bool
	Show   func(any) (string, error)
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
	t["Meta.liftInt"] = liftSpec(func(v any) ast.Expr { return &ast.IntLit{Value: v.(int64), Raw: true} })
	t["Meta.liftFloat"] = liftSpec(func(v any) ast.Expr { return &ast.FloatLit{Value: v.(float64)} })
	t["Meta.liftString"] = liftSpec(func(v any) ast.Expr { return &ast.StringLit{Value: v.(string)} })
	t["Meta.liftChar"] = liftSpec(func(v any) ast.Expr { return &ast.CharLit{Value: v.(rune)} })
	t["Meta.liftBool"] = liftSpec(func(v any) ast.Expr {
		if v.(bool) {
			return &ast.Ctor{Name: "True"}
		}
		return &ast.Ctor{Name: "False"}
	})
	t["Meta.liftUnit"] = liftSpec(func(any) ast.Expr { return &ast.UnitLit{} })
	t["Meta.sameType"] = Spec{Arity: 2, Eval: func(_ *Runtime, args []any) (any, error) {
		return types.Equal(args[0].(*meta.TypeRepr).Type, args[1].(*meta.TypeRepr).Type), nil
	}}
	t["Meta.head"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) {
		r := args[0].(*meta.TypeRepr)
		if c, ok := r.Type.(*types.TCon); ok && len(c.Args) > 0 {
			return &meta.TypeRepr{Type: &types.TCon{Unique: c.Unique, Name: c.Name}, Visible: r.Visible}, nil
		}
		return r, nil
	}}
	t["Meta.isVar"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) {
		_, ok := args[0].(*meta.TypeRepr).Type.(*types.TVar)
		return ok, nil
	}}
	t["Meta.typeName"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) {
		return types.Show(args[0].(*meta.TypeRepr).Type), nil
	}}
	t["Meta.fail"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) {
		return nil, fmt.Errorf("%s", args[0].(string))
	}}
	installScalarInstances(t)
	for _, name := range []string{"add", "sub", "mul", "fdiv", "append", "eq", "neq", "lt", "gt", "le", "ge"} {
		name := name
		t["Basics."+name] = Spec{Arity: 2, Foldable: name == "add" || name == "sub" || name == "mul" || name == "fdiv", Eval: func(rt *Runtime, args []any) (any, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("native Basics.%s expects 2 arguments", name)
			}
			return stdlib.EvalBasics(name, args[0], args[1], rt.Equal), nil
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
	// Bundled natives are compile-time-safe by default: they are pure
	// functions of their arguments. Random is the exclusion — its draws read
	// and advance a process-global cell the compiler shares.
	for name, spec := range t {
		spec.CompileTimeSafe = !spec.Effect && !strings.HasPrefix(name, "Random.")
		t[name] = spec
	}
	return t
}()

func liftSpec(makeExpr func(any) ast.Expr) Spec {
	return Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) {
		return &meta.Code{Template: -1, Direct: makeExpr(args[0])}, nil
	}}
}

func Lookup(name string) (Spec, bool) { spec, ok := Table[name]; return spec, ok }
