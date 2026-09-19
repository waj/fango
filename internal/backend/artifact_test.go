package backend

import (
	"bytes"
	"testing"

	"github.com/waj/fango/internal/codegen"
)

func TestEmittedUnitRoundTripsSourceVerbatim(t *testing.T) {
	file := codegen.File{Path: "modules/Lib/module.go", Data: []byte("package fangomod\n\nvar x = \"\xf0\x9f\x99\x82\"\n")}
	data := encodeUnit("key", file)
	got, ok := decodeUnit(data, "key", file.Path)
	if !ok || !bytes.Equal(got, file.Data) {
		t.Fatalf("round trip failed: %q %v", got, ok)
	}
	if bytes.Contains(data, []byte("base64")) || !bytes.Contains(data, file.Data) {
		t.Fatal("generated source is not stored verbatim")
	}
}

// The key and path an artifact was written for are part of it, so an artifact
// reached under another identity is a miss rather than a wrong answer.
func TestEmittedUnitAnsweringAnotherIdentityIsAMiss(t *testing.T) {
	file := codegen.File{Path: "modules/Lib/module.go", Data: []byte("package fangomod\n")}
	data := encodeUnit("key", file)
	for _, c := range []struct{ name, key, path string }{
		{"other key", "other", file.Path},
		{"other path", "key", "modules/Other/module.go"},
	} {
		if _, ok := decodeUnit(data, c.key, c.path); ok {
			t.Errorf("%s was accepted", c.name)
		}
	}
	for _, c := range []struct {
		name string
		wire []byte
	}{
		{"truncated", data[:len(data)/2]},
		{"empty source", encodeUnit("key", codegen.File{Path: file.Path})},
		{"no frame", file.Data},
	} {
		if _, ok := decodeUnit(c.wire, "key", file.Path); ok {
			t.Errorf("%s was accepted", c.name)
		}
	}
}

func TestUnrepresentableUnitIdentityStoresNothing(t *testing.T) {
	if encodeUnit("key\nmore", codegen.File{Path: "a.go", Data: []byte("x")}) != nil {
		t.Error("a key spanning lines was stored")
	}
	if encodeUnit("key", codegen.File{Path: "a\nb.go", Data: []byte("x")}) != nil {
		t.Error("a path spanning lines was stored")
	}
}
