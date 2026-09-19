package backend

import (
	"bytes"
	"testing"

	"github.com/waj/fango/internal/codegen"
)

func testRecord(path string) unitRecord {
	return unitRecord{Schema: emissionSchema, Unit: "Lib", Path: path, Implementation: "impl", Semantic: "sem", ABI: "abi",
		Linked: []linkedModule{{Module: "Basics", Semantic: "s", ABI: "a"}}}
}

func TestEmittedUnitRoundTripsSourceVerbatim(t *testing.T) {
	file := codegen.File{Path: "modules/Lib/module.go", Data: []byte("package fangomod\n\nvar x = \"\xf0\x9f\x99\x82\"\n")}
	record := testRecord(file.Path)
	data := encodeUnit(record, file)
	got, ok := decodeUnit(data, record)
	if !ok || !bytes.Equal(got, file.Data) {
		t.Fatalf("round trip failed: %q %v", got, ok)
	}
	if bytes.Contains(data, []byte("base64")) || !bytes.Contains(data, file.Data) {
		t.Fatal("generated source is not stored verbatim")
	}
}

// What an artifact was built from is part of it, so a slot holding bytes built
// from anything else is a miss rather than a wrong answer.
func TestEmittedUnitBuiltFromSomethingElseIsAMiss(t *testing.T) {
	file := codegen.File{Path: "modules/Lib/module.go", Data: []byte("package fangomod\n")}
	record := testRecord(file.Path)
	data := encodeUnit(record, file)
	otherPath := testRecord("modules/Other/module.go")
	otherOwn := testRecord(file.Path)
	otherOwn.Implementation = "other"
	otherLinked := testRecord(file.Path)
	otherLinked.Linked = []linkedModule{{Module: "Basics", Semantic: "moved", ABI: "a"}}
	extraLinked := testRecord(file.Path)
	extraLinked.Linked = append(append([]linkedModule(nil), extraLinked.Linked...), linkedModule{Module: "List", Semantic: "s", ABI: "a"})
	entryInputs := testRecord(file.Path)
	entryInputs.Entry, entryInputs.EntrySymbol = true, "Main.main"
	printMain := testRecord(file.Path)
	printMain.PrintMain = true
	for _, c := range []struct {
		name string
		want unitRecord
	}{
		{"another path", otherPath},
		{"an edited owner", otherOwn},
		{"a moved dependency contract", otherLinked},
		{"a widened closure", extraLinked},
		{"entry-only inputs", entryInputs},
		{"another main form", printMain},
	} {
		if _, ok := decodeUnit(data, c.want); ok {
			t.Errorf("%s was accepted", c.name)
		}
	}
	for _, c := range []struct {
		name string
		wire []byte
	}{
		{"truncated", data[:len(data)/2]},
		{"empty source", encodeUnit(record, codegen.File{Path: file.Path})},
		{"no frame", file.Data},
	} {
		if _, ok := decodeUnit(c.wire, record); ok {
			t.Errorf("%s was accepted", c.name)
		}
	}
}
