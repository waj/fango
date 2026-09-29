package repl

import (
	"strings"
	"testing"
)

func TestAttributesStageAndRollbackInREPL(t *testing.T) {
	var out strings.Builder
	Run(strings.NewReader(`import Meta
import Json
import Result
type Label = Label String
#[Label "first"] type Box = Box
readLabel : Meta.TypeRepr -> Meta.Code
readLabel repr =
    case Meta.info repr of
        Meta.Visible info ->
            case Meta.attributes @Label info.attributes of
                Meta.Item attached _ ->
                    case attached.value of
                        Label text -> Meta.lift text
                Meta.NoItems -> Meta.fail "missing attribute"
        Meta.Opaque -> Meta.fail "hidden"

$(readLabel (typeOf Box))
#[Meta.fail "attachment failed"] type Broken = Broken
:type Broken
#[Label "second"] type Box = Box
$(readLabel (typeOf Box))
type Bad = { #[Json.Skip] x : Int } deriving (Json.Encode)
:type Bad
type Good = { x : Int #[Json.Default (quote 4)] } deriving (Json.Decode)
case Json.parse @Good "{}" of
    Result.Ok good -> good.x
    Result.Err error -> -1

:quit
`), &out)
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
