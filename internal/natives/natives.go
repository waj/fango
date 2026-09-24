// Package natives is the in-process registry for inline compiler-bundled
// primitives and compile-time-safe adapters. Ordinary call-form sidecars run
// through internal/nativehost instead.
package natives

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/meta"
	"github.com/waj/fango/runtime/fangort"
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
type ExitError = fangort.ExitError

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
	installBytes(t)
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
	t["String.toFloatNative"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) { return stdlib.ToFloatNative(args[0].(string)), nil }}
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
	t["IO.hasInput"] = Spec{Arity: 1, Effect: true, Eval: func(rt *Runtime, _ []any) (any, error) {
		_, err := rt.Reader.Peek(1)
		if err == io.EOF {
			return false, nil
		}
		if err != nil {
			panic(err)
		}
		return true, nil
	}}
	t["IO.readRawLine"] = Spec{Arity: 1, Effect: true, Eval: func(rt *Runtime, _ []any) (any, error) {
		line, err := rt.Reader.ReadString('\n')
		if err != nil && err != io.EOF {
			panic(err)
		}
		return string([]rune(line)), nil
	}}
	t["IO.write"] = Spec{Arity: 1, Effect: true, Eval: func(rt *Runtime, args []any) (any, error) {
		if _, err := io.WriteString(rt.Writer, args[0].(string)); err != nil {
			panic(err)
		}
		return struct{}{}, nil
	}}
	t["IO.argCount"] = Spec{Arity: 1, Effect: true, Eval: func(rt *Runtime, _ []any) (any, error) {
		return int64(len(rt.Args)), nil
	}}
	t["IO.argAt"] = Spec{Arity: 1, Effect: true, Eval: func(rt *Runtime, args []any) (any, error) {
		index := args[0].(int64)
		if index < 0 || index >= int64(len(rt.Args)) {
			panic(fmt.Sprintf("argument index %d is out of range", index))
		}
		return rt.Args[index], nil
	}}
	nativePath := func(rt *Runtime, path string) string {
		if filepath.IsAbs(path) {
			return path
		}
		return filepath.Join(rt.Dir, path)
	}
	t["IO.pathExists"] = Spec{Arity: 1, Effect: true, Eval: func(rt *Runtime, args []any) (any, error) {
		_, err := os.Stat(nativePath(rt, args[0].(string)))
		if os.IsNotExist(err) {
			return false, nil
		}
		if err != nil {
			panic(err)
		}
		return true, nil
	}}
	t["IO.readFileText"] = Spec{Arity: 1, Effect: true, Eval: func(rt *Runtime, args []any) (any, error) {
		data, err := os.ReadFile(nativePath(rt, args[0].(string)))
		if err != nil {
			panic(err)
		}
		return strings.ToValidUTF8(string(data), "\uFFFD"), nil
	}}
	t["IO.writeFile"] = Spec{Arity: 2, Effect: true, Eval: func(rt *Runtime, args []any) (any, error) {
		if err := os.WriteFile(nativePath(rt, args[0].(string)), []byte(args[1].(string)), 0o644); err != nil {
			panic(err)
		}
		return struct{}{}, nil
	}}
	t["IO.exit"] = Spec{Arity: 1, Effect: true, Eval: func(_ *Runtime, args []any) (any, error) {
		return nil, &ExitError{Code: int(args[0].(int64))}
	}}
	t["Random.entropySeed"] = Spec{Arity: 1, Eval: func(_ *Runtime, _ []any) (any, error) {
		return stdlib.EntropySeed(), nil
	}}
	// The File module's natives exist only in the sidecar worker: they touch
	// the file system and return Go errors the compiler turns into IO.Error,
	// which this in-process table cannot represent. Their entries record the
	// arity bundled-native validation checks and refuse to run here.
	for name, arity := range fileNatives {
		t[name] = Spec{Arity: arity, Eval: func(_ *Runtime, _ []any) (any, error) {
			return nil, fmt.Errorf("native %s requires the sidecar worker", name)
		}}
	}
	for name, arity := range netNatives {
		t[name] = Spec{Arity: arity, Eval: func(_ *Runtime, _ []any) (any, error) {
			return nil, fmt.Errorf("native %s requires the sidecar worker", name)
		}}
	}
	for name, arity := range cellNatives {
		t[name] = Spec{Arity: arity, Eval: func(_ *Runtime, _ []any) (any, error) {
			return nil, fmt.Errorf("native %s requires the sidecar worker", name)
		}}
	}
	// Bundled natives are compile-time-safe by default: they are pure
	// functions of their arguments. System entropy is Random's one exclusion,
	// and the File natives observe the file system; seeded draws now use
	// explicit handler-local state.
	for name, spec := range t {
		_, file := fileNatives[name]
		_, network := netNatives[name]
		_, cell := cellNatives[name]
		spec.CompileTimeSafe = !spec.Effect && name != "Random.entropySeed" && !file && !network && !cell
		t[name] = spec
	}
	return t
}()

// fileNatives lists the bundled File sidecar's natives with their arities.
var fileNatives = map[string]int{
	"File.openRead": 1, "File.openWrite": 1, "File.openAppend": 1, "File.closeHandle": 1,
	"File.handleHasInput": 1, "File.readHandleLine": 1, "File.writeHandle": 2,
	"File.readHandleBytes": 2, "File.writeHandleBytes": 2,
	"File.readFileResult": 1, "File.writeFileResult": 2,
	"File.openDirectory": 1, "File.readDirectoryEntry": 1, "File.closeDirectory": 1,
	"File.isDirectoryPath": 1,
}

var netNatives = map[string]int{
	"Net.listen": 1, "Net.closeListener": 1, "Net.acceptConnection": 1,
	"Net.dial": 2, "Net.closeConnection": 1, "Net.connectionHasInput": 1,
	"Net.readConnectionBytes": 2, "Net.writeConnectionBytes": 2,
}

var cellNatives = map[string]int{
	"Cell.cellNew": 1, "Cell.cellReader": 1, "Cell.cellPublish": 2,
	"Cell.cellReady": 1, "Cell.cellRead": 1,
}

func Lookup(name string) (Spec, bool) { spec, ok := Table[name]; return spec, ok }
