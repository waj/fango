package lsp

import (
	"context"
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
	for _, target := range []string{xDefinition, yDefinition} {
		got, err := references(context.Background(), map[string]*index{entry: idx}, nil, target, idx.symbols[target], false)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].URI != pathURI(entry) {
			t.Fatalf("local references for %s = %#v", target, got)
		}
	}
}

func TestReferencesKeepProjectIdentity(t *testing.T) {
	good := map[string]*index{}
	for range 2 {
		root := t.TempDir()
		entry := filepath.Join(root, "Main.fango")
		data := "module Main exposing (main)\nvalue = 1\nmain = value\n"
		if err := os.WriteFile(entry, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
		result, errs, internal := (&check.Session{DisableObjectCache: true}).Compile(entry)
		if internal != nil || len(errs) > 0 {
			t.Fatalf("check: %v %v", internal, errs)
		}
		good[entry] = newIndex(root, result)
	}
	for path, idx := range good {
		sym := idx.symbols["value:Main.value"]
		got, err := references(context.Background(), good, nil, "value:Main.value", sym, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].URI != pathURI(path) || got[0].Range.Start.Line != 2 {
			t.Fatalf("references for %s = %#v", path, got)
		}
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

func TestAttributeExpressionNavigation(t *testing.T) {
	root := t.TempDir()
	options := `module Options exposing (Label(..), tag, fallback)
type Label = Label String
-- Computes a label.
tag : String -> Label
tag text = Label text
fallback = 7
`
	main := "module Main exposing (main)\n" +
		"import Options as O\n" +
		"import Json\n" +
		"#[O.tag \"type\"]\n" +
		"type Choice = #[O.Label \"constructor\"] Choice #[O.Label \"payload\"] Int\n" +
		"type Config =\n" +
		"    { #[O.tag \"leading\"]\n" +
		"      x : Int #[O.Label \"trailing\", Json.Default `O.fallback`]\n" +
		"    , y : Int #[O.tag ({ text -> text } \"lambda\")]\n" +
		"    , z : Int #[typeOf O.Label]\n" +
		"    }\n" +
		"main = \"ok\"\n"
	for name, data := range map[string]string{"Options.fango": options, "Main.fango": main} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	entry := filepath.Join(root, "Main.fango")
	result, errs, internal := (&check.Session{DisableObjectCache: true}).Compile(entry)
	if internal != nil || len(errs) > 0 {
		t.Fatalf("check: %v %v", internal, errs)
	}
	idx := newIndex(root, result)
	for _, tc := range []struct{ marker, target string }{
		{`O.tag "type"`, "value:Options.tag"},
		{`O.Label "constructor"`, "ctor:Options.Label"},
		{`O.Label "payload"`, "ctor:Options.Label"},
		{`O.tag "leading"`, "value:Options.tag"},
		{`O.Label "trailing"`, "ctor:Options.Label"},
		{`Json.Default`, "ctor:Json.Default"},
		{`O.fallback`, "value:Options.fallback"},
		{`O.tag ({`, "value:Options.tag"},
		{`O.Label]`, "type:Options.Label"},
	} {
		offset := strings.Index(main, tc.marker)
		found := false
		for _, use := range idx.documents[entry].uses {
			if use.span.Start == offset && use.target == tc.target {
				found = true
				if sym := idx.symbols[use.target]; sym.span.File == nil || sym.typeText == "" {
					t.Errorf("missing definition or hover type for %s", tc.marker)
				}
			}
		}
		if !found {
			t.Errorf("no navigation target for %s", tc.marker)
		}
	}
	if got := idx.symbols["value:Options.tag"].docs; got != "Computes a label." {
		t.Fatalf("helper hover docs = %q", got)
	}
	binder := strings.Index(main, "text ->")
	useOffset := strings.Index(main, "text }")
	var binderID, useID string
	for _, use := range idx.documents[entry].uses {
		if use.span.Start == binder {
			binderID = use.target
		}
		if use.span.Start == useOffset {
			useID = use.target
		}
	}
	if binderID == "" || binderID != useID || !strings.HasPrefix(binderID, "local:") {
		t.Fatalf("attribute local binder = %q, use = %q", binderID, useID)
	}
	// Find References uses the same index, including references from modules
	// that are not open and only mention a helper inside an attribute.
	otherPath := filepath.Join(root, "Other.fango")
	other := "module Other exposing (T)\nimport Options\ntype T = { x : Int #[Options.tag \"unopened\"] }\n"
	if err := os.WriteFile(otherPath, []byte(other), 0o644); err != nil {
		t.Fatal(err)
	}
	indexes, err := scanWorkspace(context.Background(), []string{root}, nil, nil, []byte("tag"))
	if err != nil {
		t.Fatal(err)
	}
	refs, err := references(context.Background(), indexes, nil, "value:Options.tag", idx.symbols["value:Options.tag"], false)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, ref := range refs {
		counts[ref.URI]++
	}
	if counts[pathURI(entry)] != 3 || counts[pathURI(otherPath)] != 1 {
		t.Fatalf("attribute references = %#v", refs)
	}
}

func TestDocumentationAcrossLeadingAttributes(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "Main.fango")
	text := `module Main exposing (main)
type Label = Label String
-- Type documentation.
#[Label "first"]
#[Label
    -- Payload comment, not documentation.
    "second"
]
type Config =
    {
      -- Leading field documentation.
      #[Label "field"]
      x : Int
    , -- Not documentation.
      -- Trailing field documentation.
      y : Int #[Label "trailing"]
    , -- Not documentation.
      -- Detached field documentation.

      #[Label "detached"]
      z : Int
    }
-- Union documentation.
#[Label "union"]
type Choice =
    -- Constructor documentation.
    #[Label
        "constructor"
    ]
    Choice Int
-- Resource documentation.
#[Label "resource"]
{-# resource #-}
type Handle = Handle
-- Detached type documentation.
#[Label "detached"]

type Detached = Detached
main = "ok"
`
	if err := os.WriteFile(entry, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	result, errs, internal := (&check.Session{DisableObjectCache: true}).Compile(entry)
	if internal != nil || len(errs) > 0 {
		t.Fatalf("check: %v %v", internal, errs)
	}
	idx := newIndex(root, result)
	for name, want := range map[string]string{
		"type:Main.Config":    "Type documentation.",
		"field:Main.Config.x": "Leading field documentation.",
		"field:Main.Config.y": "Trailing field documentation.",
		"field:Main.Config.z": "",
		"type:Main.Choice":    "Union documentation.",
		"ctor:Main.Choice":    "Constructor documentation.",
		"type:Main.Handle":    "Resource documentation.",
		"type:Main.Detached":  "",
	} {
		if got := idx.symbols[name].docs; got != want {
			t.Errorf("%s docs = %q, want %q", name, got, want)
		}
	}
}
