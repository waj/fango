package elaborate_test

import (
	"strings"
	"testing"

	"github.com/waj/fango/internal/core"
)

func TestScalarSpecialization(t *testing.T) {
	p := elabPoly(t, `step n = if n == 0 then 0 else n + step (n - 1)
main = print (step 5)
`)
	variants := 0
	for _, d := range p.Defs {
		if !strings.HasPrefix(d.Name, "_scalar_") {
			continue
		}
		variants++
		if len(d.TyParams) != 0 || len(d.Params) != 1 {
			t.Fatalf("variant has generic parameters: %+v", d)
		}
		dump := core.Dump(&core.Prog{Defs: []core.Def{d}})
		for _, forbidden := range []string{"_dictionary_", "app/value", "(var step "} {
			if strings.Contains(dump, forbidden) {
				t.Errorf("variant retains %q:\n%s", forbidden, dump)
			}
		}
		if !strings.Contains(dump, "(var "+d.Name+" ") {
			t.Errorf("variant does not recurse directly:\n%s", dump)
		}
	}
	if variants != 2 {
		t.Fatalf("got %d variants, want Int and Float", variants)
	}
}

func TestScalarSpecializationLeavesHandlersGeneric(t *testing.T) {
	p := elabPoly(t, `effect Choose
    choose : () -> Bool
step n =
    handle (if choose () then n + 1 else n) of
        choose () -> resume True
main = print (step 5)
`)
	for _, d := range p.Defs {
		if strings.HasPrefix(d.Name, "_scalar_") {
			t.Fatalf("worker with an internal handler was specialized: %s", d.Name)
		}
	}
}
