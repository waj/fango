package infer

import (
	"testing"

	"github.com/waj/fango/internal/types"
)

// The boundary recognizes Bytes by the representation the checker assigned at
// its declaration, not by its name, so a user type spelled the same way is not
// admitted and a wrapper around an Int still is.
func TestBytesBoundaryIsRecognizedByRepresentation(t *testing.T) {
	ck := &Checker{ADTs: map[int]*types.ADTInfo{}}

	bundled := &types.TCon{Name: BytesTypeName, Unique: 1}
	ck.ADTs[1] = &types.ADTInfo{Con: bundled, Repr: types.ReprBytes,
		Ctors: []*types.CtorInfo{{Name: BytesCtorName}}}
	if !ck.isBytesType(bundled) {
		t.Fatal("the bundled Bytes is not recognized at the boundary")
	}
	// It is not a scalar wrapper: its constructor carries nothing, so nothing
	// is projected on the way in or rebuilt on the way out.
	if ck.boundaryWrapper(bundled, "Bytes") != nil {
		t.Fatal("Bytes was taken for a scalar wrapper")
	}

	mine := &types.TCon{Name: "Mine.Bytes", Unique: 2}
	ck.ADTs[2] = &types.ADTInfo{Con: mine, Repr: types.ReprADT,
		Ctors: []*types.CtorInfo{{Name: "Mine.Bytes"}}}
	if ck.isBytesType(mine) {
		t.Fatal("a user type named Bytes was admitted as the bundled one")
	}
}
