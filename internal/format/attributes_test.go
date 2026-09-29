package format

import (
	"github.com/waj/fango/internal/source"
	"strings"
	"testing"
)

func TestAttributedFieldsUseSeparateAlignedLines(t *testing.T) {
	input := "type Config = { #[Json.Key \"foo\"] #[Db.Column \"foo\"] foo : String, #[Json.Key \"bar\", Json.Default `0`] bar : Int, plain : Bool } deriving (Show)\n"
	want := "type Config =\n" +
		"    { #[Json.Key \"foo\"]\n" +
		"      #[Db.Column \"foo\"]\n" +
		"      foo : String\n" +
		"    , #[Json.Key \"bar\", Json.Default `0`]\n" +
		"      bar : Int\n" +
		"    , plain : Bool\n" +
		"    }\n" +
		"    deriving (Show)\n"
	for _, text := range []string{input, want} {
		out, errs := Source(source.NewFile("attributes.fango", []byte(text)))
		if len(errs) > 0 {
			t.Fatal(errs)
		}
		if string(out) != want {
			t.Fatalf("got:\n%s\nwant:\n%s", out, want)
		}
	}
}

func TestAttributeGroupsPreserveExpressionsAndComments(t *testing.T) {
	for _, text := range []string{
		"#[Label (1, 2), Other [\"a\", \"b\"]]\ntype T = T\n",
		"type T = { #[Label (1 + 2),]\n      x : Int }\n",
		"type T =\n    { #[Label \"x\"] -- wire name\n      #[Other \"x\"]\n      x : Int\n    }\n",
		"type T =\n    { #[Label \"x\",\n        Other [1, 2],\n      ]\n      x : Int\n    }\n",
		"type T = #[Label \"ctor\"] C #[Label \"field\"] Int\n",
	} {
		out, errs := Source(source.NewFile("attributes.fango", []byte(text)))
		if len(errs) > 0 {
			t.Fatalf("format %q: %v", text, errs)
		}
		again, errs := Source(source.NewFile("attributes.fango", out))
		if len(errs) > 0 || string(out) != string(again) {
			t.Fatalf("not idempotent:\n%s\n%s\n%v", out, again, errs)
		}
		if strings.Contains(text, "-- wire name") && !strings.Contains(string(out), "-- wire name") {
			t.Fatal("comment lost")
		}
	}
}

func TestTrailingFieldAttributesAlign(t *testing.T) {
	input := "type Config = { name : String #[Json.Key \"full_name\"], count : Int #[Json.Default `7`], secret : String #[Json.Skip, Json.Default `\"local\"`], muchLongerPlainField : List String } deriving (Encode, Decode)\n"
	want := "type Config =\n" +
		"    { name : String    #[Json.Key \"full_name\"]\n" +
		"    , count : Int      #[Json.Default `7`]\n" +
		"    , secret : String  #[Json.Skip, Json.Default `\"local\"`]\n" +
		"    , muchLongerPlainField : List String\n" +
		"    }\n" +
		"    deriving (Encode, Decode)\n"
	for _, text := range []string{input, want} {
		out, errs := Source(source.NewFile("attributes.fango", []byte(text)))
		if len(errs) > 0 || string(out) != want {
			t.Fatalf("got:\n%s\nwant:\n%s\n%v", out, want, errs)
		}
	}
}

func TestFieldAttributePlacementAndContinuation(t *testing.T) {
	input := "type Config =\n" +
		"    { #[Leading \"a\"]\n" +
		"      name : String #[Trailing \"a\"] #[Other \"a\"]\n" +
		"    , count : Int\n" +
		"      #[Default `7`]\n" +
		"      #[Note \"b\"]\n" +
		"    , #[Leading \"c\"]\n" +
		"      secret : String\n" +
		"    }\n"
	want := "type Config =\n" +
		"    { #[Leading \"a\"]\n" +
		"      name : String  #[Trailing \"a\"] #[Other \"a\"]\n" +
		"    , count : Int\n" +
		"          #[Default `7`]\n" +
		"          #[Note \"b\"]\n" +
		"    , #[Leading \"c\"]\n" +
		"      secret : String\n" +
		"    }\n"
	for _, text := range []string{input, want} {
		out, errs := Source(source.NewFile("attributes.fango", []byte(text)))
		if len(errs) > 0 || string(out) != want {
			t.Fatalf("got:\n%s\nwant:\n%s\n%v", out, want, errs)
		}
	}
}

func TestTrailingAttributeCommentsAndMultiline(t *testing.T) {
	for _, text := range []string{
		"type T =\n    { x : Int #[A \"x\"] -- wire name\n          #[B [1, 2]]\n    , y : String #[C]\n    }\n",
		"type T =\n    { x : Int -- explanation\n          #[A]\n          -- another tag\n          #[B]\n    }\n",
		"type T =\n    { #[Before]\n      x : Int #[A,\n                B [1, 2],\n              ] #[C]\n    }\n",
		"type T =\n    { x : Int\n          #[A, -- payload\n            B [1, 2],\n          ]\n    }\n",
		"type T = { x : Int {- comment -} #[A] }\n",
	} {
		out, errs := Source(source.NewFile("attributes.fango", []byte(text)))
		if len(errs) > 0 {
			t.Fatalf("format %q: %v", text, errs)
		}
		again, errs := Source(source.NewFile("attributes.fango", out))
		if len(errs) > 0 || string(out) != string(again) {
			t.Fatalf("not idempotent:\n%s\n%s\n%v", out, again, errs)
		}
	}
}
