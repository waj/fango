// Package natives is the in-process registry for inline compiler-bundled
// primitives and compile-time-safe adapters. Ordinary call-form sidecars run
// through internal/nativehost instead.
package natives

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/meta"
	"github.com/waj/fango/runtime/fangort"
	stdlib "github.com/waj/fango/stdlib"
)

type Runtime struct {
	Reader *bufio.Reader
	Host   fangort.SessionHost
	Writer io.Writer
	Args   []string
	Dir    string

	// Expand renders a compile-time code value as surface AST. Only the
	// compiler's own evaluator supplies it: the Meta natives that build code
	// need their arguments as trees, and the template table lives with the
	// checker (doc/design.md, "Compile-time metaprogramming").
	Expand func(*meta.Code) ast.Expr
}

// ExitError is how the interpreter represents Process.exit without terminating
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
	installRegex(t)
	installTextBuilder(t)
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
	// The REPL's evaluator answers Runtime.Prompt.level before the table.
	t["Runtime.Prompt.level"] = Spec{Arity: 1, Eval: func(_ *Runtime, _ []any) (any, error) {
		return nil, errors.New("Runtime.Prompt.level runs only inside a prompt level")
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
	t["IO.handleHasInput"] = Spec{Arity: 1, Eval: func(rt *Runtime, args []any) (any, error) {
		value, err := fangort.IOHandleHasInput(rt.Host, args[0])
		return ioAnswer(value, err)
	}}
	t["IO.readHandleLine"] = Spec{Arity: 1, Eval: func(rt *Runtime, args []any) (any, error) {
		value, err := fangort.ReadIOHandleLine(rt.Host, args[0])
		return ioAnswer(value, err)
	}}
	t["IO.readHandleBytes"] = Spec{Arity: 2, Eval: func(rt *Runtime, args []any) (any, error) {
		value, err := fangort.ReadIOHandleBytes(rt.Host, args[0], args[1].(int64))
		return ioAnswer(value, err)
	}}
	t["IO.writeHandle"] = Spec{Arity: 2, Eval: func(rt *Runtime, args []any) (any, error) {
		return ioAnswer(struct{}{}, fangort.WriteIOHandleBytes(rt.Host, args[0], []byte(args[1].(string))))
	}}
	t["IO.writeHandleBytes"] = Spec{Arity: 2, Eval: func(rt *Runtime, args []any) (any, error) {
		return ioAnswer(struct{}{}, fangort.WriteIOHandleBytes(rt.Host, args[0], args[1].([]byte)))
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
	t["Process.exit"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) {
		return nil, &ExitError{Code: int(args[0].(int64))}
	}}
	t["Random.entropySeed"] = Spec{Arity: 1, Eval: func(_ *Runtime, _ []any) (any, error) {
		return stdlib.EntropySeed(), nil
	}}
	// File acquisition and directory natives run only in the sidecar worker.
	// Their entries record the arity bundled-native validation checks and
	// refuse to run here. Host-only IO and Process calls can run in-process.
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
	// Bundled natives are compile-time-safe by default: they are pure
	// functions of their arguments. System entropy is Random's one exclusion,
	// and the File natives observe the file system; seeded draws now use
	// explicit handler-local state.
	for name, spec := range t {
		_, file := fileNatives[name]
		_, network := netNatives[name]
		ambient := strings.HasPrefix(name, "Process.") || strings.HasPrefix(name, "Console.") || strings.HasPrefix(name, "IO.")
		if name == "IO.lineText" || name == "IO.lineEnding" || name == "IO.standardHandle" {
			ambient = false
		}
		spec.CompileTimeSafe = !spec.Effect && name != "Random.entropySeed" && !file && !network && !ambient
		t[name] = spec
	}
	return t
}()

// fileNatives lists the bundled File sidecar's natives with their arities.
var fileNatives = map[string]int{
	"File.openRead": 1, "File.openWrite": 1, "File.openAppend": 1, "File.closeHandle": 1,
	"File.readFileResult": 1, "File.writeFileResult": 2,
	"File.openDirectory": 1, "File.readDirectoryEntry": 1, "File.closeDirectory": 1,
	"File.isDirectoryPath": 1, "File.fileSize": 1,
}

var netNatives = map[string]int{
	"Net.listen": 1, "Net.closeListener": 1, "Net.acceptConnection": 1,
	"Net.dial": 2, "Net.closeConnection": 1, "Net.connectionHasInput": 1,
	"Net.readConnectionBytes": 2, "Net.writeConnectionBytes": 2,
}

func Lookup(name string) (Spec, bool) { spec, ok := Table[name]; return spec, ok }

// IO uses the same classified outcome as sidecar calls in the worker.
func ioAnswer(value any, err error) (any, error) {
	if err != nil {
		return fangort.ClassifyIOError(err), nil
	}
	return value, nil
}
