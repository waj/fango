package apidoc

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/waj/fango/internal/check"
	"github.com/waj/fango/internal/lexer"
	"github.com/waj/fango/internal/modules"
	"github.com/waj/fango/internal/source"
)

const shapes = `{-# no-prelude #-}
-- Geometry helpers.
--
-- ` + "```fango" + `
-- area (Square 2.0)
--     |> ignore
-- ` + "```" + `
module Shapes exposing (Shape(..), Point(..), Pair(..), Hidden, Measure(..), Log(..), area, describe, logged, grouped, (<+>))

import Basics exposing (..)

type Tag = Tag String

-- A shape.
#[Tag "shape"]
type Shape
    -- A circle by radius.
    = Circle Float
    -- A square by side.
    | #[Tag "square"] Square Float
    deriving (Eq)

-- A point.
type Point =
    {
      -- Horizontal.
      x : Float
    , y : Float
    }

-- Two numbers; its fields share this line.
type Pair = { first : Int, second : Int }

-- Opaque.
type Hidden = Hidden Int

type Private = Private

-- Things with a size.
class Measure a
    -- The size.
    size : a -> Int

instance Measure Shape
    size _ = 1

-- Logging.
effect Log
    -- Records a line.
    log : String -> ()

-- Area of a shape.
area : Shape -> Float
area (Circle r) = r * r * 3.0
area (Square s) = s * s

-- Names a shape.
describe (Circle _) = "circle"
describe (Square _) = "square"

-- Logs, then measures.
logged x =
    log "measuring"
    size x

-- Detached.

grouped = 1

-- Adds.
(<+>) : Int -> Int -> Int
(<+>) a b = a + b

infixl 6 (<+>)

secret = Private
`

const facade = `-- Re-exports.
module Facade exposing (Shape(..), Measure(..), area)

import Shapes exposing (Measure(..), Shape(..), area)
`

func extract(t *testing.T) (*Document, []Missing) {
	t.Helper()
	root := t.TempDir()
	for name, text := range map[string]string{"Shapes.fango": shapes, "Facade.fango": facade, "Main.fango": "import Facade\nimport Shapes\n\nmain = 1\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	result, errs, internal := (&check.Session{DisableObjectCache: true, LoadOptions: modules.LoadOptions{Root: root}}).Compile(filepath.Join(root, "Main.fango"))
	if internal != nil || len(errs) > 0 {
		t.Fatalf("check: %v %v", internal, errs)
	}
	return Extract(result, Options{Modules: []string{"Shapes", "Facade"}, Path: func(f *source.File) string { return f.Name }})
}

func find(t *testing.T, doc *Document, id string) Declaration {
	t.Helper()
	for _, m := range doc.Modules {
		for _, d := range m.Declarations {
			if d.ID == id {
				return d
			}
		}
	}
	t.Fatalf("no declaration %s", id)
	return Declaration{}
}

func TestExtractDeclarations(t *testing.T) {
	doc, _ := extract(t)
	if len(doc.Modules) != 2 || doc.Modules[0].Name != "Facade" || doc.Modules[1].Name != "Shapes" {
		t.Fatalf("modules = %+v", doc.Modules)
	}
	shapes := doc.Modules[1]
	// Module documentation sits above the header, after the pragma, and keeps
	// the indentation its Markdown code depends on.
	if want := "Geometry helpers.\n\n```fango\narea (Square 2.0)\n    |> ignore\n```"; shapes.Documentation != want || shapes.Source != (Location{Path: "Shapes.fango", Line: 8}) {
		t.Fatalf("module = %q at %+v", shapes.Documentation, shapes.Source)
	}
	var ids []string
	for _, d := range shapes.Declarations {
		ids = append(ids, d.ID)
	}
	want := []string{
		"class:Shapes.Measure", "constructor:Shapes.Circle", "constructor:Shapes.Square",
		"effect:Shapes.Log", "field:Shapes.Pair.first", "field:Shapes.Pair.second",
		"field:Shapes.Point.x", "field:Shapes.Point.y", "method:Shapes.Measure.size",
		"operation:Shapes.Log.log", "type:Shapes.Hidden", "type:Shapes.Pair", "type:Shapes.Point",
		"type:Shapes.Shape", "value:Shapes.<+>", "value:Shapes.area", "value:Shapes.describe",
		"value:Shapes.grouped", "value:Shapes.logged",
	}
	// Private values and types, and the opaque type's constructor, stay out.
	if !slices.Equal(ids, want) {
		t.Fatalf("declarations:\n%v\nwant:\n%v", ids, want)
	}
	for _, tc := range []struct {
		id, kind, name, signature, docs, parent string
		line                                    int
	}{
		{"type:Shapes.Shape", "type", "Shape", "type Shape = Circle Float | Square Float", "A shape.", "", 16},
		{"constructor:Shapes.Circle", "constructor", "Circle", "Circle : Float -> Shape", "A circle by radius.", "type:Shapes.Shape", 18},
		{"constructor:Shapes.Square", "constructor", "Square", "Square : Float -> Shape", "A square by side.", "type:Shapes.Shape", 20},
		{"type:Shapes.Point", "type", "Point", "type Point = { x : Float, y : Float }", "A point.", "", 24},
		{"field:Shapes.Point.x", "field", "x", "x : Float", "Horizontal.", "type:Shapes.Point", 27},
		{"field:Shapes.Point.y", "field", "y", "y : Float", "", "type:Shapes.Point", 28},
		// Members sharing their type's line have no documentation of their own.
		{"field:Shapes.Pair.first", "field", "first", "first : Int", "", "type:Shapes.Pair", 32},
		{"type:Shapes.Hidden", "type", "Hidden", "type Hidden", "Opaque.", "", 35},
		{"class:Shapes.Measure", "class", "Measure", "class Measure a", "Things with a size.", "", 40},
		{"method:Shapes.Measure.size", "method", "size", "size : Measure a => a -> Int", "The size.", "class:Shapes.Measure", 42},
		{"effect:Shapes.Log", "effect", "Log", "effect Log", "Logging.", "", 48},
		{"operation:Shapes.Log.log", "operation", "log", "log : String ->{Log} ()", "Records a line.", "effect:Shapes.Log", 50},
		// Documentation attaches above an annotation, or above the first
		// equation of an unannotated group.
		{"value:Shapes.area", "value", "area", "area : Shape -> Float", "Area of a shape.", "", 53},
		{"value:Shapes.describe", "value", "describe", "describe : Shape -> String", "Names a shape.", "", 58},
		// Unannotated: the inferred constraints and effect row.
		{"value:Shapes.logged", "value", "logged", "logged : Measure a => a ->{Log} Int", "Logs, then measures.", "", 62},
		{"value:Shapes.grouped", "value", "grouped", "grouped : Num a => a", "", "", 68},
		{"value:Shapes.<+>", "value", "(<+>)", "(<+>) : Int -> Int -> Int", "Adds.", "", 71},
	} {
		d := find(t, doc, tc.id)
		if d.Kind != tc.kind || d.Name != tc.name || d.Signature != tc.signature || d.Documentation != tc.docs || d.ParentID != tc.parent || d.Source != (Location{Path: "Shapes.fango", Line: tc.line}) || d.TargetID != "" {
			t.Errorf("%s = %+v\nwant kind %s, name %s, signature %q, docs %q, parent %q, line %d", tc.id, d, tc.kind, tc.name, tc.signature, tc.docs, tc.parent, tc.line)
		}
	}
	if d := find(t, doc, "value:Shapes.<+>"); d.Fixity != "infixl 6" {
		t.Errorf("fixity = %q", d.Fixity)
	}
	if d := find(t, doc, "type:Shapes.Shape"); !slices.Equal(d.Instances, []string{"Eq Shape", "Measure Shape"}) {
		t.Errorf("Shape instances = %v", d.Instances)
	}
	// A class's instances span modules, so a type sharing its name with
	// another loaded type (Meta.Shape here) is qualified.
	if d := find(t, doc, "class:Shapes.Measure"); !slices.Equal(d.Instances, []string{"Measure Shapes.Shape"}) {
		t.Errorf("Measure instances = %v", d.Instances)
	}
}

func TestExtractReexports(t *testing.T) {
	doc, missing := extract(t)
	for id, target := range map[string]string{
		"type:Facade.Shape":          "type:Shapes.Shape",
		"constructor:Facade.Circle":  "constructor:Shapes.Circle",
		"class:Facade.Measure":       "class:Shapes.Measure",
		"method:Facade.Measure.size": "method:Shapes.Measure.size",
		"value:Facade.area":          "value:Shapes.area",
	} {
		d, owner := find(t, doc, id), find(t, doc, target)
		if d.TargetID != target || d.Documentation != owner.Documentation || d.Signature != owner.Signature || d.Source != owner.Source {
			t.Errorf("%s = %+v, want the owner's %+v", id, d, owner)
		}
	}
	if d := find(t, doc, "constructor:Facade.Circle"); d.ParentID != "type:Facade.Shape" {
		t.Errorf("re-exported constructor parent = %q", d.ParentID)
	}
	// Strict coverage asks only for owned declarations and module comments.
	var gaps []string
	for _, m := range missing {
		gaps = append(gaps, m.String())
	}
	want := []string{
		"Shapes.fango:28: field:Shapes.Point.y has no documentation",
		"Shapes.fango:32: field:Shapes.Pair.first has no documentation",
		"Shapes.fango:32: field:Shapes.Pair.second has no documentation",
		"Shapes.fango:68: value:Shapes.grouped has no documentation",
	}
	if !slices.Equal(gaps, want) {
		t.Fatalf("missing:\n%s\nwant:\n%s", strings.Join(gaps, "\n"), strings.Join(want, "\n"))
	}
}

func TestCommentText(t *testing.T) {
	file := source.NewFile("Docs.fango", []byte(`{- A block
   that continues,
       indented code.
-}
value = 1
-- A list:
--
--   - item
--     more
other = 2
`))
	_, comments, errs := lexer.LexWithComments(file)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	at := func(text string) source.Span {
		start := strings.Index(string(file.Content), text)
		return source.Span{File: file, Start: start, End: start + len(text)}
	}
	if got := Leading(comments, at("value =")); got != "A block\nthat continues,\n    indented code." {
		t.Errorf("block = %q", got)
	}
	if got := Leading(comments, at("other =")); got != "A list:\n\n  - item\n    more" {
		t.Errorf("lines = %q", got)
	}
}
