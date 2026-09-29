package lsp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/waj/fango/internal/check"
	"github.com/waj/fango/internal/lexer"
	"github.com/waj/fango/internal/modules"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/token"
)

func TestIndexAcrossModulesAndRecordFields(t *testing.T) {
	root := t.TempDir()
	shapes := "module Shapes exposing (Box(..), mk)\n\ntype Box = { value : Int }\n\n-- Returns a box.\nmk : Box\nmk = Box { value = 1 }\n"
	main := "module Main exposing (main)\n\nimport Shapes exposing (Box(..), mk)\n\nmain = mk.value\n"
	for name, data := range map[string]string{"Shapes.fango": shapes, "Main.fango": main} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	entry := filepath.Join(root, "Main.fango")
	result, errs, internal := (&check.Session{DisableObjectCache: true, LoadOptions: modules.LoadOptions{Root: root}}).Compile(entry)
	if internal != nil || len(errs) > 0 {
		t.Fatalf("check: %v %v", internal, errs)
	}
	idx := newIndex(root, result)
	doc := idx.documents[entry]
	if doc == nil {
		t.Fatal("entry missing")
	}
	var value, field, importedModule string
	for _, use := range doc.uses {
		text := string(use.span.File.Content[use.span.Start:use.span.End])
		if text == "Shapes" && use.span.StartPos().Line == 3 {
			importedModule = use.target
		}
		if text == "mk" && use.span.StartPos().Line == 5 {
			value = use.target
		}
		if text == "value" && use.span.StartPos().Line == 5 {
			field = use.target
		}
	}
	if value != "value:Shapes.mk" {
		t.Errorf("mk target = %q", value)
	}
	if field != "field:Shapes.Box.value" {
		t.Errorf("field target = %q", field)
	}
	if importedModule != "module:Shapes" {
		t.Errorf("import target = %q", importedModule)
	}
	if sym := idx.symbols[value]; !strings.Contains(sym.docs, "Returns a box") || !strings.Contains(sym.typeText, "Box") {
		t.Errorf("mk hover = %#v", sym)
	}
	if sym := idx.symbols[field]; sym.span.StartPos().Line != 3 {
		t.Errorf("field definition = %#v", sym)
	}
}

func TestUTF16PositionConversion(t *testing.T) {
	f := source.NewFile("u.fango", []byte("-- 😀x\r\nmain = 1\n"))
	offset := strings.Index(string(f.Content), "x")
	if p := positionAt(f, offset); p.Line != 0 || p.Character != 5 {
		t.Fatalf("position = %#v", p)
	}
	if got, ok := offsetAt(f, position{Line: 0, Character: 5}); !ok || got != offset {
		t.Fatalf("offset = %d, %v", got, ok)
	}
	if _, ok := offsetAt(f, position{Line: 0, Character: 4}); ok {
		t.Fatal("accepted middle of surrogate pair")
	}
}

func TestNestedUnsavedModuleOverlay(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "Main.fango")
	dep := filepath.Join(root, "Foo", "Bar.fango")
	main := "module Main exposing (main)\nimport Foo.Bar exposing (value)\nmain = value\n"
	if err := os.WriteFile(entry, []byte(main), 0o644); err != nil {
		t.Fatal(err)
	}
	result, errs, internal := (&check.Session{DisableObjectCache: true, LoadOptions: modules.LoadOptions{Root: root, Overlays: map[string][]byte{dep: []byte("module Foo.Bar exposing (value)\nvalue = 7\n")}}}).Compile(entry)
	if internal != nil || len(errs) > 0 {
		t.Fatalf("overlay check: %v %v", internal, errs)
	}
	idx := newIndex(root, result)
	if sym, ok := idx.symbols["value:Foo.Bar.value"]; !ok || sym.path != dep {
		t.Fatalf("overlay target: %#v %v", sym, ok)
	}
	result, errs, internal = (&check.Session{DisableObjectCache: true, LoadOptions: modules.LoadOptions{Root: root, Overlays: map[string][]byte{dep: []byte("module Foo.Bar exposing (value)\nvalue = 7\n")}}}).Compile(dep)
	if internal != nil || len(errs) > 0 || result == nil {
		t.Fatalf("nested entry: %v %v", internal, errs)
	}
}

func TestOverlayPreservesPathCasing(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "Main.fango")
	if err := os.WriteFile(entry, []byte("module Main exposing (main)\nimport Foo.Bar exposing (value)\nmain = value\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "Foo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Foo", "bar.fango"), []byte("module Foo.Bar exposing (value)\nvalue = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wrong := filepath.Join(root, "Foo", "Bar.fango")
	_, errs, _ := (&check.Session{DisableObjectCache: true, LoadOptions: modules.LoadOptions{Root: root, Overlays: map[string][]byte{wrong: []byte("module Foo.Bar exposing (value)\nvalue = 2\n")}}}).Compile(entry)
	if len(errs) == 0 || errs[0].Title != "MODULE PATH CASING" {
		t.Fatalf("casing diagnostics: %#v", errs)
	}
}

func TestIndependentModuleDiagnostics(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"Main.fango": "module Main exposing (main)\nimport A exposing (a)\nimport B exposing (b)\nmain = a\n",
		"A.fango":    "module A exposing (a)\na = 1 + True\n",
		"B.fango":    "module B exposing (b)\nb = 1 + False\n",
	}
	for name, contents := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_, errs, internal := (&check.Session{DisableObjectCache: true, AccumulateDiagnostics: true}).Compile(filepath.Join(root, "Main.fango"))
	if internal != nil {
		t.Fatal(internal)
	}
	seen := map[string]bool{}
	for _, err := range errs {
		if err.Span.File != nil {
			seen[err.Span.File.Name] = true
		}
	}
	if !seen["A.fango"] || !seen["B.fango"] {
		t.Fatalf("independent diagnostics: %#v", errs)
	}
}

func TestLocalBinderNavigation(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "Main.fango")
	data := "module Main exposing (main)\nhelper x =\n    y = x\n    y\nmain = helper 1\n"
	if err := os.WriteFile(entry, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	result, errs, internal := (&check.Session{DisableObjectCache: true}).Compile(entry)
	if internal != nil || len(errs) > 0 {
		t.Fatalf("check: %v %v", internal, errs)
	}
	idx := newIndex(root, result)
	var xDefinition, yDefinition, xUse, yUse string
	for _, u := range idx.documents[entry].uses {
		text := string(u.span.File.Content[u.span.Start:u.span.End])
		switch {
		case text == "x" && u.span.StartPos().Line == 2:
			xDefinition = u.target
		case text == "x" && u.span.StartPos().Line == 3:
			xUse = u.target
		case text == "y" && u.span.StartPos().Line == 3:
			yDefinition = u.target
		case text == "y" && u.span.StartPos().Line == 4:
			yUse = u.target
		}
	}
	if xDefinition == "" || xUse != xDefinition || yDefinition == "" || yUse != yDefinition {
		t.Fatalf("local targets: x %q/%q, y %q/%q", xDefinition, xUse, yDefinition, yUse)
	}
}

func TestLeadingCommentsAcrossPragma(t *testing.T) {
	file := source.NewFile("Docs.fango", []byte("-- First line\n-- Second line\n{-# scoped s #-}\nvalue : Int\nvalue = 1\n\n-- Detached\n\nother = 2\nprior = 1 -- trailing\nnext = 3\n"))
	_, comments, errs := lexer.LexWithComments(file)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	idx := &index{docs: map[*source.File][]token.Comment{file: comments}}
	sp := source.Span{File: file, Start: strings.Index(string(file.Content), "value :"), End: strings.Index(string(file.Content), "value :") + 5}
	if got := idx.commentBefore(sp); got != "First line\nSecond line" {
		t.Fatalf("docs = %q", got)
	}
	sp.Start = strings.Index(string(file.Content), "other =")
	sp.End = sp.Start + 5
	if got := idx.commentBefore(sp); got != "" {
		t.Fatalf("detached docs = %q", got)
	}
	sp.Start = strings.Index(string(file.Content), "next =")
	sp.End = sp.Start + 4
	if got := idx.commentBefore(sp); got != "" {
		t.Fatalf("trailing comment became docs = %q", got)
	}
}
