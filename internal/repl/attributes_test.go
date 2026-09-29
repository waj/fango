package repl

import (
	"strings"
	"testing"
)

func TestAttributesStageAndRollbackInREPL(t *testing.T) {
	var out strings.Builder
	Run(strings.NewReader("import Meta\n"+
		"import Json\n"+
		"import Result\n"+
		"type Label = Label String\n"+
		"#[Label \"first\"] type Box = Box\n"+
		"readLabel : Meta.TypeRepr -> Meta.Code\n"+
		"readLabel repr =\n"+
		"    case Meta.info repr of\n"+
		"        Meta.Visible info ->\n"+
		"            case Meta.attributes @Label info.attributes of\n"+
		"                Meta.Item attached _ ->\n"+
		"                    case attached.value of\n"+
		"                        Label text -> Meta.lift text\n"+
		"                Meta.NoItems -> Meta.fail \"missing attribute\"\n"+
		"        Meta.Opaque -> Meta.fail \"hidden\"\n"+
		"\n"+
		"$(readLabel (typeOf Box))\n"+
		"#[Meta.fail \"attachment failed\"] type Broken = Broken\n"+
		":type Broken\n"+
		"#[Label \"second\"] type Box = Box\n"+
		"$(readLabel (typeOf Box))\n"+
		"type Bad = { #[Json.Skip] x : Int } deriving (Json.Encode)\n"+
		":type Bad\n"+
		"type Good = { x : Int #[Json.Default `4`] } deriving (Json.Decode)\n"+
		"case Json.parse @Good \"{}\" of\n"+
		"    Result.Ok good -> good.x\n"+
		"    Result.Err error -> -1\n"+
		"\n"+
		":quit\n"), &out)
	got := out.String()
	if strings.Contains(got, "INTERNAL") || strings.Contains(got, "runtime error") {
		t.Fatalf("unexpected failure:\n%s", got)
	}
	for _, want := range []string{"first : String", "second : String", "attachment failed", "I don't know a constructor named `Broken`", "I don't know a constructor named `Bad`", "4 : Int"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q:\n%s", want, got)
		}
	}
}
