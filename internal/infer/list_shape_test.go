package infer

import (
	"strings"
	"testing"

	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/types"
)

// The backends project head and tail against a fixed constructor layout, so a
// List without it would miscompile silently. This was a compiler-internal
// assertion while the sources were embedded; the library is a tree on disk
// now, so it is a mistake a user can make and has to read as one rather than
// taking the process down. Reaching markListRepr directly is the point: a
// drifted List cannot be declared through the real prelude, which is what
// list_test.go checks the marking against.
func TestDriftedBundledListReportsADiagnostic(t *testing.T) {
	adt := &types.ADTInfo{Con: &types.TCon{Name: ListTypeName}, Params: []*types.TVar{{}}}
	for i, name := range []string{ListNilName, ListConsName, "List.Extra"} {
		adt.Ctors = append(adt.Ctors, &types.CtorInfo{Name: name, Index: i})
	}
	errs := markListRepr(adt, source.Span{})
	if len(errs) != 1 {
		t.Fatalf("errors = %#v, want one", errs)
	}
	if errs[0].Title != "INVALID BUNDLED LIST" {
		t.Fatalf("title = %q", errs[0].Title)
	}
	if !strings.Contains(errs[0].Body, "3 constructors") {
		t.Fatalf("body does not say what is wrong: %q", errs[0].Body)
	}
	if adt.Repr == types.ReprList {
		t.Fatal("a drifted List was given the runtime representation anyway")
	}
}
