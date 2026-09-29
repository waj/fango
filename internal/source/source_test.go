package source

import "testing"

func TestMergeSpansFromDifferentFiles(t *testing.T) {
	user := NewFile("user.fango", []byte("use value"))
	library := NewFile("Json.fango", []byte("generated value"))
	use := Span{File: user, Start: 0, End: 3}
	generated := Span{File: library, Start: 0, End: 15}
	if got := use.Merge(generated); got != use {
		t.Fatalf("cross-file merge = %+v, want %+v", got, use)
	}
	if got := (Span{}).Merge(use); got != use {
		t.Fatalf("empty receiver merge = %+v, want %+v", got, use)
	}
}
