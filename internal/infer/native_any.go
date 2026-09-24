package infer

import (
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/types"
)

const (
	NativeAnyTypeName = "Runtime.Native.Any"
	NativeAnyCtorName = "Runtime.Native.Any"
)

func markNativeAnyRepr(adt *types.ADTInfo, at source.Span) []diag.Error {
	if adt.Con.Name != NativeAnyTypeName {
		return nil
	}
	if len(adt.Params) != 0 || len(adt.Ctors) != 1 || adt.Ctors[0].Name != NativeAnyCtorName || len(adt.Ctors[0].Fields) != 0 {
		return []diag.Error{diag.Errorf(at, "INVALID BUNDLED NATIVE ANY",
			"The bundled `%s` must be declared exactly as `type Any = Any`; restore the library shipped with this compiler.", NativeAnyTypeName)}
	}
	adt.Repr = types.ReprNativeAny
	adt.Ctors[0].Repr = types.ReprNativeAny
	return nil
}
