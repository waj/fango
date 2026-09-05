// Package natives is the interpreter-side registry for compiler-bundled
// native declarations. User sidecars deliberately do not enter this table:
// they run only through the compiled backend.
package natives

import (
	"bufio"
	"fmt"
	"io"

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
	Eval     func(*Runtime, []any) (any, error)
}

var Table = func() map[string]Spec {
	t := map[string]Spec{}
	for _, name := range []string{"add", "sub", "mul", "fdiv", "append", "eq", "neq", "lt", "gt", "le", "ge"} {
		name := name
		t["Basics."+name] = Spec{Arity: 2, Foldable: name == "add" || name == "sub" || name == "mul" || name == "fdiv", Eval: func(rt *Runtime, args []any) (any, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("native Basics.%s expects 2 arguments", name)
			}
			return stdlib.EvalBasics(name, args[0], args[1], rt.Equal), nil
		}}
	}
	t["IO.print"] = Spec{Arity: 1, Effect: true, Eval: func(rt *Runtime, args []any) (any, error) {
		text, err := rt.Show(args[0])
		if err != nil {
			return nil, err
		}
		return struct{}{}, stdlib.PrintTo(rt.Writer, text)
	}}
	t["IO.readLine"] = Spec{Arity: 1, Effect: true, Eval: func(rt *Runtime, _ []any) (any, error) {
		return stdlib.ReadLineFrom(rt.Reader)
	}}
	t["IO.write"] = Spec{Arity: 1, Effect: true, Eval: func(rt *Runtime, args []any) (any, error) {
		return struct{}{}, stdlib.WriteTo(rt.Writer, args[0].(string))
	}}
	return t
}()

func Lookup(name string) (Spec, bool) { spec, ok := Table[name]; return spec, ok }
