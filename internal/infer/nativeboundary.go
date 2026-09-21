package infer

import (
	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/types"
)

// The native boundary admits three shapes beyond plain scalars (doc/design.md,
// "Go backend and runtime"): a single-constructor, single-scalar-field type
// declared in the sidecar's own module, erased to its scalar at the Go call;
// the bundled Bytes, in bundled sidecars only, crossing as an ordinary []byte
// and erased to nothing; and, for the bundled File module only, a
// `Result IO.Error T` result produced from a Go `(T, error)`. Module validation
// checks the Go signatures against the spelling of these shapes before name
// resolution; this file resolves the same shapes semantically and records the
// constructors both backends construct, so neither re-derives them from names.

const (
	fallibleNativeModule = "File"
	ioErrorName          = "IO.Error"
	ioKindName           = "IO.Kind"
	resultTypeName       = "Result.Result"
)

// ioKindCtors is IO.Kind's constructor order, the compiler's contract with
// fangort.ClassifyIOError's kind codes.
var ioKindCtors = []string{"NotFound", "PermissionDenied", "AlreadyExists", "IsDirectory", "NotDirectory", "Other"}

// resolveNativeBoundaries fills the boundary metadata of every sidecar native
// the module declares. It runs after the module's constructors are declared,
// because effect operations are declared before constructors and a wrapper's
// field type is only known afterwards.
func (ck *Checker) resolveNativeBoundaries(m *ast.Module) []diag.Error {
	var errs []diag.Error
	for _, d := range m.Decls {
		switch d := d.(type) {
		case *ast.EffectDecl:
			for _, op := range d.Ops {
				if op.Native != nil && op.Native.Template == nil {
					if n := ck.Natives[op.Name]; n != nil {
						errs = append(errs, ck.resolveNativeBoundary(n, op.NameSpan)...)
					}
				}
			}
		case *ast.ValueDecl:
			if d.Native != nil && d.Native.Template == nil && !types.Intrinsic(d.Name) {
				if n := ck.Natives[d.Name]; n != nil {
					errs = append(errs, ck.resolveNativeBoundary(n, d.NameSpan)...)
				}
			}
		}
	}
	return errs
}

func (ck *Checker) resolveNativeBoundary(n *types.NativeInfo, sp source.Span) []diag.Error {
	// A wrapper must be declared in the native's own module. Canonical symbols
	// carry that module as their prefix — none for a headerless entry — so the
	// native's own symbol, not its sidecar link name, is the locality key.
	module := symbolModule(n.Name)
	n.ParamWrappers = make([]*types.CtorInfo, 0, n.Arity)
	t := n.Scheme.Body
	for range n.Arity {
		fn, ok := t.(*types.TFun)
		if !ok {
			return nil // the declaration was already diagnosed
		}
		n.ParamWrappers = append(n.ParamWrappers, ck.boundaryWrapper(fn.Arg, module))
		t = fn.Ret
	}
	if ck.isResultType(t) {
		shape, errs := ck.fallibleShape(t, n, sp)
		n.Fallible = shape
		if shape != nil {
			n.ResultWrapper = ck.boundaryWrapper(shape.Payload, module)
		}
		return errs
	}
	n.ResultWrapper = ck.boundaryWrapper(t, module)
	return nil
}

// boundaryWrapper answers the constructor a type is erased through at the
// boundary, or nil for anything that is not a local single-scalar wrapper.
func (ck *Checker) boundaryWrapper(t types.Type, module string) *types.CtorInfo {
	con, ok := t.(*types.TCon)
	if !ok || len(con.Args) != 0 {
		return nil
	}
	adt := ck.ADTs[con.Unique]
	if adt == nil || adt.IsRecord() || len(adt.Params) != 0 || len(adt.Ctors) != 1 || len(adt.Ctors[0].Fields) != 1 {
		return nil
	}
	if symbolModule(adt.Con.Name) != module || !ck.isBoundaryScalar(adt.Ctors[0].Fields[0]) {
		return nil
	}
	return adt.Ctors[0]
}

// isBytesType answers the bundled Bytes, the one non-scalar boundary type. It
// tests the representation the checker assigned at the declaration (bytes.go),
// so a user type named `Bytes` is not it.
func (ck *Checker) isBytesType(t types.Type) bool {
	con, ok := t.(*types.TCon)
	if !ok || len(con.Args) != 0 {
		return false
	}
	adt := ck.ADTs[con.Unique]
	return adt != nil && adt.Repr == types.ReprBytes
}

func (ck *Checker) isBoundaryScalar(t types.Type) bool {
	con, ok := t.(*types.TCon)
	if !ok || len(con.Args) != 0 {
		return false
	}
	switch con.Unique {
	case ck.B.Int.Unique, ck.B.Float.Unique, ck.B.String.Unique, ck.B.Char.Unique, ck.B.Bool.Unique:
		return true
	}
	return false
}

func (ck *Checker) isResultType(t types.Type) bool {
	con, ok := t.(*types.TCon)
	if !ok {
		return false
	}
	adt := ck.ADTs[con.Unique]
	return adt != nil && adt.Con.Name == resultTypeName
}

// fallibleShape checks a `Result IO.Error T` native result and gathers the
// constructors the backends build. Only the bundled File module's value
// natives may declare one: module validation already rejects the Go signature
// elsewhere, so this is the semantic backstop rather than the user-facing
// diagnostic.
func (ck *Checker) fallibleShape(t types.Type, n *types.NativeInfo, sp source.Span) (*types.FallibleShape, []diag.Error) {
	bad := func(format string, args ...any) (*types.FallibleShape, []diag.Error) {
		return nil, []diag.Error{diag.Errorf(sp, "NATIVE DECLARATION", format, args...)}
	}
	if n.Effect != nil || n.Module != fallibleNativeModule {
		return bad("Only value natives of the bundled `File` module may declare a fallible native result.")
	}
	con := t.(*types.TCon)
	result := ck.ADTs[con.Unique]
	if len(con.Args) != 2 || len(result.Ctors) != 2 {
		return bad("A fallible native result must be `Result IO.Error T`.")
	}
	errCon, ok := con.Args[0].(*types.TCon)
	if !ok || ck.ADTs[errCon.Unique] == nil || ck.ADTs[errCon.Unique].Con.Name != ioErrorName {
		return bad("A fallible native result must be `Result IO.Error T`; its error type is not `IO.Error`.")
	}
	errADT := ck.ADTs[errCon.Unique]
	if !errADT.IsRecord() || len(errADT.RecordFields) != 3 {
		return bad("`IO.Error` must be a record of `kind`, `path`, and `message`.")
	}
	kindIdx, kindField := errADT.RecordField("kind")
	pathIdx, pathField := errADT.RecordField("path")
	messageIdx, messageField := errADT.RecordField("message")
	if kindField == nil || pathField == nil || messageField == nil {
		return bad("`IO.Error` must be a record of `kind`, `path`, and `message`.")
	}
	if !types.Equal(pathField.Type, ck.B.String) || !types.Equal(messageField.Type, ck.B.String) {
		return bad("`IO.Error`'s `path` and `message` fields must be strings.")
	}
	kindCon, ok := kindField.Type.(*types.TCon)
	if !ok || ck.ADTs[kindCon.Unique] == nil || ck.ADTs[kindCon.Unique].Con.Name != ioKindName {
		return bad("`IO.Error`'s `kind` field must be `IO.Kind`.")
	}
	kindADT := ck.ADTs[kindCon.Unique]
	if len(kindADT.Ctors) != len(ioKindCtors) {
		return bad("`IO.Kind` must declare exactly the constructors %v, in that order.", ioKindCtors)
	}
	for i, c := range kindADT.Ctors {
		if types.SurfaceName(c.Name) != ioKindCtors[i] || len(c.Fields) != 0 {
			return bad("`IO.Kind` must declare exactly the constructors %v, in that order.", ioKindCtors)
		}
	}
	payload := con.Args[1]
	if !ck.isBoundaryScalar(payload) && !ck.isBytesType(payload) && !types.Equal(payload, ck.B.Unit) && ck.boundaryWrapper(payload, symbolModule(n.Name)) == nil {
		return bad("A fallible native's payload must be a boundary scalar, `Bytes`, Unit, or a local scalar wrapper type.")
	}
	var errCtor, okCtor *types.CtorInfo
	for _, c := range result.Ctors {
		switch types.SurfaceName(c.Name) {
		case "Err":
			errCtor = c
		case "Ok":
			okCtor = c
		}
	}
	if errCtor == nil || okCtor == nil {
		return bad("A fallible native result must be `Result IO.Error T`.")
	}
	return &types.FallibleShape{
		Err: errCtor, Ok: okCtor, Error: errADT.Ctors[0], Kinds: kindADT.Ctors,
		KindIdx: kindIdx, PathIdx: pathIdx, MessageIdx: messageIdx, Payload: payload,
	}, nil
}
