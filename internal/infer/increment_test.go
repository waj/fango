package infer_test

import (
	"testing"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/infer"
	"github.com/waj/fango/internal/types"
)

// A checker takes in one module graph after another, as the REPL does:
// visibility accumulates per owner and the caller's owner survives.
func TestModuleAccumulatesVisibility(t *testing.T) {
	sup := &types.Supply{}
	ck := infer.NewChecker(sup, types.NewBuiltins(sup), infer.NewEnv())
	ck.CurrentOwner = ""
	if _, errs := ck.Module(&ast.Module{InstanceImports: map[string]map[string]bool{"A": {"B": true}}}); len(errs) > 0 {
		t.Fatal(errs)
	}
	if _, errs := ck.Module(&ast.Module{InstanceImports: map[string]map[string]bool{"C": {"A": true}}}); len(errs) > 0 {
		t.Fatal(errs)
	}
	if !ck.InstanceImports["A"]["B"] || !ck.InstanceImports["C"]["A"] {
		t.Fatalf("visibility %v", ck.InstanceImports)
	}
	if ck.CurrentOwner != "" {
		t.Fatalf("owner %q", ck.CurrentOwner)
	}
}
