package infer

import (
	"strings"
	"testing"

	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/types"
)

// The backends give this declaration a `[]byte` representation and emit no
// struct for it, so a Bytes that had grown a field would miscompile silently.
// As with List, the library is a tree on disk, so drift is a mistake a user
// can make and has to read as one rather than taking the process down.
func TestDriftedBundledBytesReportsADiagnostic(t *testing.T) {
	adt := &types.ADTInfo{Con: &types.TCon{Name: BytesTypeName}}
	adt.Ctors = append(adt.Ctors, &types.CtorInfo{Name: BytesCtorName, Fields: []types.Type{&types.TCon{Name: "Int"}}})
	errs := markBytesRepr(adt, source.Span{})
	if len(errs) != 1 {
		t.Fatalf("errors = %#v, want one", errs)
	}
	if errs[0].Title != "INVALID BUNDLED BYTES" {
		t.Fatalf("title = %q", errs[0].Title)
	}
	if !strings.Contains(errs[0].Body, "has 1 fields, want 0") {
		t.Fatalf("body does not say what is wrong: %q", errs[0].Body)
	}
	if adt.Repr == types.ReprBytes {
		t.Fatal("a drifted Bytes was given the runtime representation anyway")
	}
}

// A user type spelled the same way in another module has a different
// canonical symbol and keeps the ordinary ADT lowering.
func TestUserBytesTypeIsNotTheBundledOne(t *testing.T) {
	adt := &types.ADTInfo{Con: &types.TCon{Name: "Mine.Bytes"}}
	adt.Ctors = append(adt.Ctors, &types.CtorInfo{Name: "Mine.Bytes"})
	if errs := markBytesRepr(adt, source.Span{}); len(errs) != 0 {
		t.Fatalf("errors = %#v, want none", errs)
	}
	if adt.Repr != types.ReprADT {
		t.Fatal("a user type named Bytes was given the bundled representation")
	}
}
