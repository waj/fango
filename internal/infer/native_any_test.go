package infer

import (
	"testing"

	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/types"
)

func TestNativeAnyRepresentationIsCanonical(t *testing.T) {
	con := &types.TCon{Name: NativeAnyTypeName}
	ctor := &types.CtorInfo{Name: NativeAnyCtorName, Result: con}
	adt := &types.ADTInfo{Con: con, Ctors: []*types.CtorInfo{ctor}}
	if errs := markNativeAnyRepr(adt, source.Span{}); len(errs) != 0 {
		t.Fatalf("mark Runtime.Native.Any: %v", errs)
	}
	if adt.Repr != types.ReprNativeAny || ctor.Repr != types.ReprNativeAny {
		t.Fatal("Runtime.Native.Any did not receive its opaque runtime representation")
	}

	lookalikeCon := &types.TCon{Name: "Mine.Any"}
	lookalike := &types.ADTInfo{Con: lookalikeCon, Ctors: []*types.CtorInfo{{Name: "Mine.Any", Result: lookalikeCon}}}
	if errs := markNativeAnyRepr(lookalike, source.Span{}); len(errs) != 0 {
		t.Fatalf("lookalike: %v", errs)
	}
	if lookalike.Repr == types.ReprNativeAny {
		t.Fatal("a user Any type received the opaque representation")
	}
}

func TestDriftedNativeAnyIsRejected(t *testing.T) {
	con := &types.TCon{Name: NativeAnyTypeName}
	adt := &types.ADTInfo{Con: con, Ctors: []*types.CtorInfo{{Name: NativeAnyCtorName, Result: con, Fields: []types.Type{con}}}}
	if errs := markNativeAnyRepr(adt, source.Span{}); len(errs) != 1 || errs[0].Title != "INVALID BUNDLED NATIVE ANY" {
		t.Fatalf("errors = %#v", errs)
	}
}
