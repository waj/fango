// Package natives contains in-process adapters for bundled inline templates,
// compiler representations, compile-time evaluation, and host/WASM fallbacks.
// Ordinary call-form sidecars are discovered by internal/nativehost; adding one
// does not require an entry here.
package natives

import (
	"bufio"
	"io"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/meta"
	"github.com/waj/fango/runtime/fangort"
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
	// Declared reflects the declared type of the top-level name a code value
	// quotes; ok is false for any other code. The checker supplies it
	// alongside Expand.
	Declared func(*meta.Code) (repr *meta.TypeRepr, ok bool, err error)
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

var table = func() map[string]Spec {
	t := map[string]Spec{}
	installMeta(t)
	installScalarInstances(t)
	installBytes(t)
	installRegex(t)
	installTextBuilder(t)
	installTime(t)
	installPure(t)
	// These installers contain only deterministic adapters. Runtime-only
	// fallbacks are installed afterwards, so new ambient calls are not made
	// stage-safe by a name-based exclusion list.
	for name, spec := range t {
		spec.CompileTimeSafe = !spec.Effect
		t[name] = spec
	}
	installHost(t)
	installPrompt(t)
	return t
}()

func Lookup(name string) (Spec, bool) { spec, ok := table[name]; return spec, ok }
