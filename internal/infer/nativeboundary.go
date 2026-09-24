package infer

import (
	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/types"
)

// The native boundary admits three shapes beyond plain scalars (doc/design.md,
// "Go backend and runtime"): a single-constructor, single-boundary-value type
// declared in the sidecar's own module, erased to its field at the Go call;
// the bundled Bytes, in bundled sidecars only, crossing as an ordinary []byte;
// and a bundled File or Net error result produced from a Go `(T, error)`.
// Module validation
// checks the Go signatures against the spelling of these shapes before name
// resolution; this file resolves the same shapes semantically and records the
// constructors both backends construct, so neither re-derives them from names.

const (
	ioErrorName    = "IO.Error"
	ioKindName     = "IO.Kind"
	netErrorName   = "Net.Error"
	netKindName    = "Net.Kind"
	resultTypeName = "Result.Result"
)

// ioKindCtors is IO.Kind's constructor order, the compiler's contract with
// fangort.ClassifyIOError's kind codes.
var ioKindCtors = []string{"NotFound", "PermissionDenied", "AlreadyExists", "IsDirectory", "NotDirectory", "Other"}
var netKindCtors = []string{"ConnectionRefused", "ConnectionReset", "AddressInUse", "TimedOut", "Other"}

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
	defer func() {
		for _, wrapper := range append(append([]*types.CtorInfo(nil), n.ParamWrappers...), n.ResultWrapper) {
			if wrapper != nil {
				if adt := ck.ADTs[wrapper.Result.Unique]; adt != nil && len(adt.Params) > 0 {
					adt.NativeIndexed = true
				}
			}
		}
	}()
	// A wrapper must be declared in the native's own module. Canonical symbols
	// carry that module as their prefix — none for a headerless entry — so the
	// native's own symbol, not its sidecar link name, is the locality key.
	module := symbolModule(n.Name)
	n.ParamWrappers = make([]*types.CtorInfo, 0, n.Arity)
	var errs []diag.Error
	t := n.Scheme.Body
	for i := range n.Arity {
		fn, ok := t.(*types.TFun)
		if !ok {
			return nil // the declaration was already diagnosed
		}
		wrapper := ck.boundaryWrapper(fn.Arg, module)
		n.ParamWrappers = append(n.ParamWrappers, wrapper)
		_, opaque := fn.Arg.(*types.TVar)
		if !opaque && !types.Equal(fn.Arg, ck.B.Unit) && !ck.isBoundaryValue(fn.Arg) && !ck.isBytesType(fn.Arg) && wrapper == nil {
			errs = append(errs, diag.Errorf(sp, "NATIVE DECLARATION", "Parameter %d of native `%s` does not resolve to a boundary value.", i+1, n.Name))
		}
		t = fn.Ret
	}
	if ck.isResultType(t) {
		shape, shapeErrs := ck.fallibleShape(t, n, sp)
		n.Fallible = shape
		if shape != nil {
			n.ResultWrapper = ck.boundaryWrapper(shape.Payload, module)
		}
		return append(errs, shapeErrs...)
	}
	n.ResultWrapper = ck.boundaryWrapper(t, module)
	_, opaque := t.(*types.TVar)
	if !opaque && !types.Equal(t, ck.B.Unit) && !ck.isBoundaryValue(t) && !ck.isBytesType(t) && n.ResultWrapper == nil {
		errs = append(errs, diag.Errorf(sp, "NATIVE DECLARATION", "The result of native `%s` does not resolve to a boundary value.", n.Name))
	}
	storage, err := types.CheckNativeStorage(n)
	if err != nil {
		errs = append(errs, diag.Errorf(sp, "NATIVE STORAGE", "%s", err))
	} else {
		n.Storage = storage
		if storage.Kind != "" {
			if n.Effect != nil {
				errs = append(errs, diag.Errorf(sp, "NATIVE STORAGE", "Opaque storage uses value natives; wrap them in ordinary effect handlers."))
			}
			wrapper := n.ResultWrapper
			if storage.Handle >= 0 {
				wrapper = n.ParamWrappers[storage.Handle]
			}
			if wrapper == nil || !ck.ADTs[wrapper.Result.Unique].Resource {
				errs = append(errs, diag.Errorf(sp, "NATIVE STORAGE", "Opaque storage requires a resource wrapper with a private representation."))
			}
		}
	}
	return errs
}

// boundaryWrapper answers the constructor a type is erased through at the
// boundary, or nil for anything that is not a local one-field wrapper.
func (ck *Checker) boundaryWrapper(t types.Type, module string) *types.CtorInfo {
	con, ok := t.(*types.TCon)
	if !ok {
		return nil
	}
	adt := ck.ADTs[con.Unique]
	if adt == nil || adt.IsRecord() || len(con.Args) != len(adt.Params) || len(adt.Ctors) != 1 || len(adt.Ctors[0].Fields) != 1 {
		return nil
	}
	if symbolModule(adt.Con.Name) != module || !ck.isBoundaryValue(adt.Ctors[0].Fields[0]) {
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

func (ck *Checker) isNativeAnyType(t types.Type) bool {
	con, ok := t.(*types.TCon)
	if !ok || len(con.Args) != 0 {
		return false
	}
	adt := ck.ADTs[con.Unique]
	return adt != nil && adt.Repr == types.ReprNativeAny
}

func (ck *Checker) isBoundaryValue(t types.Type) bool {
	return ck.isBoundaryScalar(t) || ck.isNativeAnyType(t)
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

// fallibleShape checks a bundled File or Net error result and gathers the
// constructors the backends build. Module validation already rejects the Go
// signature elsewhere; this is the resolved-type backstop.
func (ck *Checker) fallibleShape(t types.Type, n *types.NativeInfo, sp source.Span) (*types.FallibleShape, []diag.Error) {
	bad := func(format string, args ...any) (*types.FallibleShape, []diag.Error) {
		return nil, []diag.Error{diag.Errorf(sp, "NATIVE DECLARATION", format, args...)}
	}
	if n.Effect != nil || n.Module != "File" && n.Module != "Net" {
		return bad("Only value natives of the bundled `File` and `Net` modules may declare a fallible native result.")
	}
	errorName, kindName, locationName, classifier, kindNames := ioErrorName, ioKindName, "path", "io", ioKindCtors
	if n.Module == "Net" {
		errorName, kindName, locationName, classifier, kindNames = netErrorName, netKindName, "address", "net", netKindCtors
	}
	con := t.(*types.TCon)
	result := ck.ADTs[con.Unique]
	if len(con.Args) != 2 || len(result.Ctors) != 2 {
		return bad("A fallible native result must be `Result %s T`.", errorName)
	}
	errCon, ok := con.Args[0].(*types.TCon)
	if !ok || ck.ADTs[errCon.Unique] == nil || ck.ADTs[errCon.Unique].Con.Name != errorName {
		return bad("A fallible native result must use `%s` as its error type.", errorName)
	}
	errADT := ck.ADTs[errCon.Unique]
	if !errADT.IsRecord() || len(errADT.RecordFields) != 3 {
		return bad("`%s` must be a record of `kind`, `%s`, and `message`.", errorName, locationName)
	}
	kindIdx, kindField := errADT.RecordField("kind")
	locationIdx, locationField := errADT.RecordField(locationName)
	messageIdx, messageField := errADT.RecordField("message")
	if kindField == nil || locationField == nil || messageField == nil {
		return bad("`%s` must be a record of `kind`, `%s`, and `message`.", errorName, locationName)
	}
	if !types.Equal(locationField.Type, ck.B.String) || !types.Equal(messageField.Type, ck.B.String) {
		return bad("`%s`'s `%s` and `message` fields must be strings.", errorName, locationName)
	}
	kindCon, ok := kindField.Type.(*types.TCon)
	if !ok || ck.ADTs[kindCon.Unique] == nil || ck.ADTs[kindCon.Unique].Con.Name != kindName {
		return bad("`%s`'s `kind` field must be `%s`.", errorName, kindName)
	}
	kindADT := ck.ADTs[kindCon.Unique]
	if len(kindADT.Ctors) != len(kindNames) {
		return bad("`%s` must declare exactly the constructors %v, in that order.", kindName, kindNames)
	}
	for i, c := range kindADT.Ctors {
		if types.SurfaceName(c.Name) != kindNames[i] || len(c.Fields) != 0 {
			return bad("`%s` must declare exactly the constructors %v, in that order.", kindName, kindNames)
		}
	}
	payload := con.Args[1]
	if !ck.isBoundaryValue(payload) && !ck.isBytesType(payload) && !types.Equal(payload, ck.B.Unit) && ck.boundaryWrapper(payload, symbolModule(n.Name)) == nil {
		return bad("A fallible native's payload must be a boundary value, `Bytes`, Unit, or a local wrapper type.")
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
		Classifier: classifier, KindIdx: kindIdx, LocationIdx: locationIdx, MessageIdx: messageIdx, Payload: payload,
	}, nil
}
