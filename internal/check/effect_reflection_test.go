package check

import (
	"strings"
	"testing"
)

// effectLibrary declares an attributed effect and its runner. exposing picks
// how much of the effect a consumer may see.
func effectLibrary(exposing string) string {
	return "module Lib exposing (" + exposing + ", runService)\n" +
		"import Options\n" +
		"#[Options.Label \"service\"]\n" +
		"effect Service\n" +
		"    ask : () -> Int\n" +
		"runService : (() ->{Service, e} a) ->{e} a\n" +
		"runService action =\n" +
		"    handle action() on\n" +
		"        ask () -> resume 7\n"
}

const effectOptions = "module Options exposing (Label(..))\ntype Label = Label String\n"

const effectConsumer = `module Main exposing (main)
import Lib
import Options
import Meta exposing (Arrow(..), Items(..), Tail(..))

label : Meta.Effect -> String
label found =
    case Meta.attributes @Options.Label found.attributes of
        Item attached _ ->
            case attached.value of
                Options.Label text -> text
        NoItems -> "none"

serviceEffect : Meta.TypeRepr -> String
serviceEffect ty =
    case Meta.arrow ty of
        Arrow parts ->
            case parts.effects of
                Item found NoItems -> found.moduleName ++ "." ++ found.name ++ " " ++ label found
                _ -> "expected one effect"
        NotArrow -> "expected an arrow"

runnerRows : Meta.Code -> String
runnerRows runner =
    case Meta.arrow (Meta.declaredType runner) of
        Arrow outer ->
            case Meta.arrow outer.param of
                Arrow inner ->
                    case (inner.effects, outer.effects) of
                        (Item found NoItems, NoItems) ->
                            case (inner.tail, outer.tail) of
                                (Open innerRow, Open outerRow) ->
                                    linked = Meta.sameType innerRow outerRow && Meta.sameType inner.result outer.result
                                    found.name ++ (if linked then " linked" else " unlinked")
                                _ -> "expected open rows"
                        _ -> "unexpected rows"
                NotArrow -> "expected a callback"
        NotArrow -> "expected a runner"

result = $(Meta.lift (serviceEffect (typeOf (() ->{Lib.Service} ())) ++ ", " ++ runnerRows ` + "`Lib.runService`" + `))

main() = print result
`

func TestEffectReflectionThroughCache(t *testing.T) {
	root := t.TempDir()
	attributeSource(t, root, "Options", effectOptions)
	lib := attributeSource(t, root, "Lib", effectLibrary("Service(..)"))
	entry := attributeSource(t, root, "Main", effectConsumer)
	cache := newMemoryObjectCache()
	want := "Lib.Service service, Service linked"
	first, _ := compileEvents(t, entry, cache)
	if got := stringDefinition(t, first, "Main.result"); got != want {
		t.Fatalf("result = %q, want %q", got, want)
	}
	// Recheck only the consumer, reflecting the producer's cached effect.
	attributeSource(t, root, "Main", strings.Replace(effectConsumer, `", " ++ runnerRows`, `"," ++ " " ++ runnerRows`, 1))
	second, events := compileEvents(t, entry, cache)
	if events["checked-cache-hit"]["Lib"] != 1 || events["check"]["Main"] != 1 {
		t.Fatalf("expected a cached producer and a checked consumer: %v", events)
	}
	if got := stringDefinition(t, second, "Main.result"); got != want {
		t.Fatalf("cached result = %q, want %q", got, want)
	}
	// Editing the effect's attribute invalidates its reflection.
	attributeSource(t, root, "Lib", strings.Replace(effectLibrary("Service(..)"), `"service"`, `"edited"`, 1))
	third, _ := compileEvents(t, entry, cache)
	if got := stringDefinition(t, third, "Main.result"); !strings.HasPrefix(got, "Lib.Service edited") {
		t.Fatalf("edited result = %q", got)
	}
	_ = lib
}

func TestEffectReflectionDiagnostics(t *testing.T) {
	for _, tc := range []struct{ name, lib, main, title, message string }{
		{"hidden attributes", effectLibrary("Service"), effectConsumer, "COMPILE-TIME FAILURE", "import it with `Service(..)`"},
		{"function payload", "module Lib exposing (Service(..))\n#[{ x -> x }]\neffect Service\n    ask : () -> Int\n",
			"module Main exposing (main)\nimport Lib\nmain() = print \"ok\"\n", "ATTRIBUTE TYPE", ""},
		{"not a reference", effectLibrary("Service(..)"),
			"module Main exposing (main)\nimport Meta\nmain() = print $(Meta.lift (Meta.typeName (Meta.declaredType `1 + 2`)))\n",
			"COMPILE-TIME FAILURE", "needs a quote of one top-level name"},
		{"incomplete declaration", effectLibrary("Service(..)"),
			"module Main exposing (main)\nimport Meta\nmain() = print $(Meta.lift (Meta.typeName (Meta.declaredType `later`)))\nlater = 1\n",
			"COMPILE-TIME FAILURE", "before its declaration is complete"},
		{"failure at code site", effectLibrary("Service(..)"),
			"module Main exposing (main)\nimport Meta\nmain() = print $(Meta.failAt (Meta.siteOf `main`) \"rejected\")\n",
			"COMPILE-TIME FAILURE", "rejected"},
		{"own pending attributes", "module Lib exposing (Service(..), probe)\nimport Meta\n" +
			"probe : Meta.Attributes -> ()\nprobe found = Meta.foldItems { _ _ -> () } () (Meta.attributes @Int found)\n" +
			"#[case Meta.arrow (typeOf (() ->{Service} ())) of\n    Meta.Arrow parts ->\n        case parts.effects of\n            Meta.Item found _ -> probe found.attributes\n            Meta.NoItems -> ()\n    Meta.NotArrow -> ()\n]\neffect Service\n    ask : () -> Int\n",
			"module Main exposing (main)\nimport Lib\nmain() = print \"ok\"\n", "COMPILE-TIME FAILURE", "before their declaration is complete"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			attributeSource(t, root, "Options", effectOptions)
			attributeSource(t, root, "Lib", tc.lib)
			entry := attributeSource(t, root, "Main", tc.main)
			_, diagnostics, err := (&Session{}).Compile(entry)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, d := range diagnostics {
				if d.Title == tc.title && strings.Contains(d.Body, tc.message) {
					found = true
					if tc.name == "failure at code site" && (d.Span.File == nil || string(d.Span.File.Content[d.Span.Start:d.Span.End]) != "main") {
						t.Fatalf("failure not reported at the quoted code: %v", d)
					}
				}
			}
			if !found {
				t.Fatalf("want %s containing %q, got %v", tc.title, tc.message, diagnostics)
			}
		})
	}
}
