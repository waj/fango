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
	textnative "github.com/waj/fango/stdlib/Text"
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
	t["Text.Builder.newBuffer"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) {
		return textnative.NewBuffer(args[0].(string)), nil
	}}
	t["Text.Builder.extendBuffer"] = Spec{Arity: 3, Eval: func(_ *Runtime, args []any) (any, error) {
		return textnative.ExtendBuffer(args[0], args[1].(int64), args[2].(string)), nil
	}}
	t["Text.Builder.newBufferChar"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) {
		return textnative.NewBufferChar(args[0].(rune)), nil
	}}
	t["Text.Builder.extendBufferChar"] = Spec{Arity: 3, Eval: func(_ *Runtime, args []any) (any, error) {
		return textnative.ExtendBufferChar(args[0], args[1].(int64), args[2].(rune)), nil
	}}
	t["Text.Builder.bufferText"] = Spec{Arity: 2, Eval: func(_ *Runtime, args []any) (any, error) {
		return textnative.BufferText(args[0], args[1].(int64)), nil
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
	t["IO.hasInput"] = Spec{Arity: 1, Effect: true, Eval: func(rt *Runtime, _ []any) (any, error) {
		if rt.Host != nil {
			ok, err := rt.Host.HasInput()
			if err != nil {
				panic(err)
			}
			return ok, nil
		}
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
		if rt.Host != nil {
			line, err := rt.Host.ReadInputLine()
			if err != nil && err != io.EOF {
				panic(err)
			}
			return string([]rune(string(line))), nil
		}
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
	// Bundled natives are compile-time-safe by default: they are pure
	// functions of their arguments. System entropy is Random's one exclusion,
	// and the File natives observe the file system; seeded draws now use
	// explicit handler-local state.
	for name, spec := range t {
		_, file := fileNatives[name]
		_, network := netNatives[name]
		spec.CompileTimeSafe = !spec.Effect && name != "Random.entropySeed" && !file && !network
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

func Lookup(name string) (Spec, bool) { spec, ok := Table[name]; return spec, ok }
