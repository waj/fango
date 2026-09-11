package infer_test

import (
	"testing"

	"github.com/waj/fango/internal/infer"
	"github.com/waj/fango/internal/types"
)

func adtNamed(t *testing.T, ck *infer.Checker, name string) *types.ADTInfo {
	t.Helper()
	ty, ok := ck.TypeNames[name]
	if !ok {
		t.Fatalf("no type named %q in scope", name)
	}
	con, ok := ty.(*types.TCon)
	if !ok {
		t.Fatalf("type %q is %s, not a type constructor", name, types.Show(ty))
	}
	adt := ck.ADT(con.Unique)
	if adt == nil {
		t.Fatalf("type %q has no ADT row", name)
	}
	return adt
}

// The bundled List must be recognized through the real prelude, not a
// test-only stand-in: the whole mechanism is a canonical-symbol match, so a
// test that hand-declared the type would prove nothing.
func TestBundledListIsMarkedForRuntimeRepresentation(t *testing.T) {
	ck, _, errs := checkPoly(t, "main = 1\n")
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	adt := adtNamed(t, ck, infer.ListTypeName)
	if adt.Repr != types.ReprList {
		t.Fatalf("bundled %s has Repr %v, want ReprList", infer.ListTypeName, adt.Repr)
	}
	for _, c := range adt.Ctors {
		if c.Repr != types.ReprList {
			t.Errorf("constructor %s has Repr %v, want ReprList — the interpreter "+
				"discriminates a construction from the CtorInfo alone", c.Name, c.Repr)
		}
	}
	if got := []string{adt.Ctors[0].Name, adt.Ctors[1].Name}; got[0] != infer.ListNilName || got[1] != infer.ListConsName {
		t.Fatalf("constructor order is %v; the backends project head/tail against this layout", got)
	}
}

// A user type of the same shape, and even the same source name, keeps the
// ordinary cons lowering — it has its own canonical symbol and its own
// identity.
func TestUserDeclaredListKeepsConsRepresentation(t *testing.T) {
	ck, _, errs := checkPoly(t, "type List a = Nil | Cons a (List a)\nmain = 1\n")
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	local := adtNamed(t, ck, "List")
	if local.Con.Unique == adtNamed(t, ck, infer.ListTypeName).Con.Unique {
		t.Fatal("the local type shadowed the bundled one; this test proves nothing")
	}
	if local.Repr != types.ReprADT {
		t.Fatalf("locally declared List has Repr %v, want ReprADT", local.Repr)
	}
	for _, c := range local.Ctors {
		if c.Repr != types.ReprADT {
			t.Errorf("local constructor %s has Repr %v, want ReprADT", c.Name, c.Repr)
		}
	}
}
