package modules

import (
	"slices"
	"testing"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/source"
)

// A session graph starts from the prelude closure and imports bundled
// modules beyond it on demand; an import already loaded adds nothing, and a
// checkpoint forgets what came after it.
func TestGraphImportsBeyondPrelude(t *testing.T) {
	g, p, errs := NewGraph(t.TempDir())
	if len(errs) > 0 {
		t.Fatalf("NewGraph: %v", errs)
	}
	if !p.Owners["Prelude"] || !g.Loaded("List") || g.Loaded("Dict") {
		t.Fatalf("unexpected initial graph: owners %v", p.Owners)
	}
	inc, errs := g.Import("Dict", source.Span{})
	if len(errs) > 0 {
		t.Fatalf("Import Dict: %v", errs)
	}
	if !slices.Equal(inc.Modules, []string{"Dict"}) || len(inc.Decls) == 0 || inc.InstanceImports["Dict"] == nil {
		t.Fatalf("increment %v", inc.Modules)
	}
	if !inc.InstanceImports["Dict"]["Basics"] {
		t.Fatal("Dict's visibility lacks its dependency Basics")
	}
	if len(inc.Natives) != 0 {
		t.Fatalf("bundled sidecars must not be in an increment: %v", inc.Natives)
	}
	again, errs := g.Import("Dict", source.Span{})
	if len(errs) > 0 || len(again.Modules) != 0 {
		t.Fatalf("re-import: %v %v", again.Modules, errs)
	}
	restore := g.Checkpoint()
	if _, errs := g.Import("String", source.Span{}); len(errs) > 0 || !g.Loaded("String") {
		t.Fatalf("Import String: %v", errs)
	}
	restore()
	if g.Loaded("String") || !g.Loaded("Dict") {
		t.Fatal("checkpoint did not restore the graph")
	}
}

// topo orders a subset whose dependencies were ordered earlier.
func TestTopoIgnoresExternalDependencies(t *testing.T) {
	nodes := map[string]*node{
		"B": {name: "B", deps: []string{"A", "Prelude"}, mod: &ast.Module{}},
		"C": {name: "C", deps: []string{"B", "Basics"}, mod: &ast.Module{}},
	}
	if got := topo(nodes); !slices.Equal(got, []string{"B", "C"}) {
		t.Fatalf("order %v", got)
	}
}
