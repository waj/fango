package modules

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/source"
)

// A session graph starts from the prelude closure and imports bundled
// modules beyond it on demand; an import already loaded adds nothing, and a
// checkpoint forgets what came after it.
func TestGraphImportsBeyondPrelude(t *testing.T) {
	g, p, errs := NewGraph(t.TempDir(), nil)
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
	if _, errs := g.Import("Range", source.Span{}); len(errs) > 0 || !g.Loaded("Range") {
		t.Fatalf("Import Range: %v", errs)
	}
	restore()
	if g.Loaded("Range") || !g.Loaded("Dict") {
		t.Fatal("checkpoint did not restore the graph")
	}
}

// Modules are read ahead concurrently, but the walk that consumes them decides
// what is reported, so diagnostics keep the order of the imports that asked.
func TestConcurrentReadsKeepDiagnosticOrder(t *testing.T) {
	dir := t.TempDir()
	entry := filepath.Join(dir, "Main.fango")
	text := "module Main exposing (main)\nimport Zebra\nimport Mango\nimport Apple\nmain() = ()\n"
	if err := os.WriteFile(entry, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	for range 20 {
		_, errs := Load(entry)
		var got []string
		for _, err := range errs {
			if err.Title == "MISSING MODULE" {
				got = append(got, err.Body)
			}
		}
		if len(got) != 3 || !strings.Contains(got[0], "Zebra") || !strings.Contains(got[1], "Mango") || !strings.Contains(got[2], "Apple") {
			t.Fatalf("diagnostics out of import order: %v", got)
		}
	}
}

// What one import read ahead does not outlive it: a failed import followed by
// a fix sees the fixed file.
func TestGraphRereadsAfterAnImport(t *testing.T) {
	dir := t.TempDir()
	g, _, errs := NewGraph(dir, nil)
	if len(errs) > 0 {
		t.Fatalf("NewGraph: %v", errs)
	}
	path := filepath.Join(dir, "Lib.fango")
	if err := os.WriteFile(path, []byte("module Lib exposing (\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, errs := g.Import("Lib", source.Span{}); len(errs) == 0 {
		t.Fatal("expected the broken module to be reported")
	}
	if err := os.WriteFile(path, []byte("module Lib exposing (value)\nvalue = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, errs := g.Import("Lib", source.Span{}); len(errs) > 0 {
		t.Fatalf("the fixed module was not reread: %v", errs)
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

// One load sees one listing of a directory, which is what lets it ask about many
// absent modules cheaply; a session graph outlives new files and must see them.
func TestLoadProviderRemembersListingsAndOthersDoNot(t *testing.T) {
	root := t.TempDir()
	load, session := newLoadProvider(root), FSProvider{Root: root}
	for _, p := range []FSProvider{load, session} {
		if _, _, err := p.Source("Later"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("before the file exists: %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "Later.fango"), []byte("module Later exposing ()\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := load.Source("Later"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a load saw a second listing: %v", err)
	}
	if _, data, err := session.Source("Later"); err != nil || len(data) == 0 {
		t.Fatalf("a session missed a new file: %q %v", data, err)
	}
	if _, _, err := newLoadProvider(root).Source("later"); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a casing mismatch was not reported: %v", err)
	}
}
