package check

import (
	"github.com/waj/fango/internal/infer"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/staging"
	"github.com/waj/fango/internal/types"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func attributeSource(t *testing.T, root, name, text string) string {
	t.Helper()
	path := filepath.Join(root, name+".fango")
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAttributeDiagnostics(t *testing.T) {
	for _, tc := range []struct{ name, body, title string }{
		{"unknown", "type T = { #[Unknown] x : Int }", "NAMING ERROR"},
		{"argument", "type T = { #[Label True] x : Int }", "TYPE MISMATCH"},
		{"function", "type T = { #[{ x -> x }] x : Int }", "ATTRIBUTE TYPE"},
		{"resource", "{-# resource #-}\ntype Handle = Handle\ntype T = { #[Handle] x : Int }", "ATTRIBUTE TYPE"},
		{"effect", "type T = { #[print \"no\"] x : Int }", "COMPILE-TIME EFFECT"},
		{"forward", "#[later()]\ntype T = T\nlater() = Label \"late\"", "STAGE ERROR"},
		{"cycle", "#[case Meta.info (typeOf T) of\n    Meta.Visible info -> Label \"x\"\n    Meta.Opaque -> Label \"opaque\"\n]\ntype T = T", "COMPILE-TIME FAILURE"},
		{"limit", "spin : () -> Label\nspin() = spin()\n#[spin()]\ntype T = T", "COMPILE-TIME LIMIT"},
		{"stage leak", "main = case Meta.info (typeOf T) of\n    Meta.Visible info -> \"visible\"\n    Meta.Opaque -> \"opaque\"\ntype T = T", "STAGE ERROR"},
		{"json duplicates", "type T = { #[Json.Key \"x\", Json.Key \"y\"] x : Int } deriving (Json.Encode)", "COMPILE-TIME FAILURE"},
		{"json trailing duplicates", "type T = { x : Int #[Json.Key \"x\", Json.Key \"y\"] } deriving (Json.Encode)", "COMPILE-TIME FAILURE"},
		{"json mixed duplicates", "type T = { #[Json.Key \"x\"] x : Int #[Json.Key \"y\"] } deriving (Json.Encode)", "COMPILE-TIME FAILURE"},
		{"json trailing skip", "type T = { x : Int #[Json.Skip] } deriving (Json.Encode)", "COMPILE-TIME FAILURE"},
		{"json skip", "type T = { #[Json.Skip] x : Int } deriving (Json.Encode)", "COMPILE-TIME FAILURE"},
		{"json keys", "type T = { #[Json.Key \"y\"] x : Int, y : Int } deriving (Json.Decode)", "COMPILE-TIME FAILURE"},
		{"json site", "#[Json.Skip]\ntype T = T deriving (Json.Encode)", "COMPILE-TIME FAILURE"},
		{"json default", "type T = { #[Json.Default `\"wrong\"`] x : Int } deriving (Json.Decode)", "TYPE MISMATCH"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text := "import Meta\nimport Json\ntype Label = Label String\n" + tc.body + "\n"
			if !strings.Contains(tc.body, "main =") {
				text += "main = \"ok\"\n"
			}
			entry := attributeSource(t, t.TempDir(), "Main", text)
			_, diagnostics, err := (&Session{}).Compile(entry)
			if err != nil {
				t.Fatal(err)
			}
			var titles []string
			for _, d := range diagnostics {
				titles = append(titles, d.Title)
			}
			if !strings.Contains(strings.Join(titles, "\n"), tc.title) {
				t.Fatalf("want %s, got %v", tc.title, diagnostics)
			}
			if strings.HasPrefix(tc.name, "json") && tc.title == "COMPILE-TIME FAILURE" {
				found := false
				for _, d := range diagnostics {
					if d.Title == tc.title && d.Span.File != nil && strings.HasPrefix(string(d.Span.File.Content[d.Span.Start:d.Span.End]), "Json.") {
						found = true
					}
				}
				if !found {
					t.Fatalf("JSON failure did not point to its attribute: %v", diagnostics)
				}
			}
		})
	}
}

func TestAttributeDataAndUnconsumedOptions(t *testing.T) {
	body := `import Meta
import Json
#[[1, 2], 3, 2.5, 'a', True, (), typeOf Int]
type T = { #[Json.Skip, Json.Key "ignored"] x : Int }
main = "ok"
`
	compileEvents(t, attributeSource(t, t.TempDir(), "Main", body), nil)
}

func TestCachedAttributesPreserveValuesAndHygiene(t *testing.T) {
	root := t.TempDir()
	attributeSource(t, root, "Options", `module Options exposing (Label(..))
type Label = Label String
`)
	lib := attributeSource(t, root, "Lib", "module Lib exposing (Config(..))\n"+
		"import Options\n"+
		"import Json\n"+
		"privateDefault = 7\n"+
		"#[Options.Label \"first\"]\n"+
		"type Config = { value : Int #[Json.Default `privateDefault`] } deriving (Json.Encode, Json.Decode)\n")
	entry := attributeSource(t, root, "Main", `module Main exposing (main)
import Lib
import Options
import Meta
readLabel : Meta.Code
readLabel =
    case Meta.info (typeOf Lib.Config) of
        Meta.Visible info ->
            case Meta.attributes @Options.Label info.attributes of
                Meta.Item attached _ ->
                    case attached.value of
                        Options.Label text -> Meta.lift text
                Meta.NoItems -> Meta.fail "missing label"
        Meta.Opaque -> Meta.fail "hidden"
main = $(readLabel)
`)
	cache := newMemoryObjectCache()
	first, _ := compileEvents(t, entry, cache)
	sources := map[string]*source.File{}
	for _, module := range first.Graph.Modules {
		if module.Source != nil {
			sources[module.Source.Name] = module.Source
		}
	}
	sup := &types.Supply{}
	ck := infer.NewChecker(sup, types.NewBuiltins(sup), infer.NewEnv())
	stage := staging.Install(ck)
	for _, object := range first.Objects {
		data, err := EncodeObject(object, nil)
		if err != nil {
			t.Fatalf("encode %s: %v", object.State.Name, err)
		}
		decoded, err := DecodeObject(data, sources)
		if err != nil {
			t.Fatalf("decode %s: %v", object.State.Name, err)
		}
		if err := InstallObject(ck, stage, decoded); err != nil {
			t.Fatalf("install %s: %v", object.State.Name, err)
		}
	}

	second, events := compileEvents(t, entry, cache)
	if stringDefinition(t, first, "Main.main") != "first" || stringDefinition(t, second, "Main.main") != "first" || len(events["check"]) != 0 {
		t.Fatalf("cached metadata changed: %v", events)
	}
	// Recheck the consumer while installing the producer's cached frozen data.
	bytes, err := os.ReadFile(entry)
	if err != nil {
		t.Fatal(err)
	}
	attributeSource(t, root, "Main", strings.Replace(string(bytes), "main = $(readLabel)", "main = $(readLabel) ++ \"!\"", 1))
	_, events = compileEvents(t, entry, cache)
	if events["checked-cache-hit"]["Lib"] != 1 {
		t.Fatalf("producer was not installed from cache: %v", events)
	}
	bytes, err = os.ReadFile(lib)
	if err != nil {
		t.Fatal(err)
	}
	attributeSource(t, root, "Lib", strings.Replace(string(bytes), "first", "second", 1))
	_, events = compileEvents(t, entry, cache)
	if events["check"]["Main"] != 1 {
		t.Fatalf("attribute edit did not invalidate consumer: %v", events)
	}
}
