package fangort

import "testing"

func TestTextConstruction(t *testing.T) {
	chars := ListCons('é', ListCons('😀', ListCons('\x00', ListNil[rune]())))
	erasedChars := ListCons[any]('é', ListCons[any]('😀', ListCons[any]('\x00', ListNil[any]())))
	if got := StringFromList(chars); got != "é😀\x00" {
		t.Fatalf("typed chars: %q", got)
	}
	if got := StringFromValueList(erasedChars); got != "é😀\x00" {
		t.Fatalf("erased chars: %q", got)
	}
	parts := ListCons("é", ListCons("", ListCons("😀\x00", ListNil[string]())))
	erasedParts := ListCons[any]("é", ListCons[any]("", ListCons[any]("😀\x00", ListNil[any]())))
	if got := StringConcat(parts); got != "é😀\x00" {
		t.Fatalf("typed parts: %q", got)
	}
	if got := StringConcatValues(erasedParts); got != "é😀\x00" {
		t.Fatalf("erased parts: %q", got)
	}
	if StringFromList(ListNil[rune]()) != "" || StringConcat(ListNil[string]()) != "" {
		t.Fatal("empty construction")
	}
}
