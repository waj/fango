package debugger

import (
	"bytes"
	"encoding/gob"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func composite(kind ValueKind, name string, children ...Value) Value {
	v := Value{Kind: kind, Text: name}
	for _, child := range children {
		v.Fields = append(v.Fields, ValueField{Value: child})
	}
	return v
}

func TestStructuralValueNotationAndTransport(t *testing.T) {
	number := Value{Kind: NumberValue, Text: "7"}
	box := composite(ConstructorValue, "Box", number)
	for _, tc := range []struct {
		value Value
		want  string
	}{
		{composite(ConstructorValue, "Box", box), "Box (Box 7)"},
		{composite(ConstructorValue, "Box", Value{Kind: CharValue, Text: " "}), "Box ' '"},
		{composite(ConstructorValue, "Box", composite(TupleValue, "", number, StringPreview("hi", false))), `Box (7, "hi")`},
		{composite(ConstructorValue, "Nothing"), "Nothing"},
		{Value{Kind: ConstructorValue, Text: "Just", Truncated: true}, "Just …"},
		{Value{Kind: RecordValue}, "{}"},
		{Value{Kind: RecordValue, Fields: []ValueField{{Name: "x", Value: box}}}, "{ x = Box 7 }"},
		{Value{Kind: RecordValue, Truncated: true}, "{ … }"},
		{Value{Kind: TupleValue, Truncated: true}, "(…)"},
		{composite(ListValue, "", number, Value{Kind: CycleValue}), "[7, <cycle>]"},
		{StringPreview("#{x}\n\\\"", false), `"\#{x}\n\\\""`},
		{Value{Kind: BoolValue, Text: "true"}, "True"},
		{Value{Kind: BoolValue, Text: "false"}, "False"},
		{Value{Kind: UnitValue}, "()"},
		{Value{Kind: FunctionValue}, "<function>"},
		{Value{Kind: OpaqueValue, Text: "Handle"}, "<opaque Handle>"},
		{Value{Kind: UnavailableValue, Text: "cannot read memory"}, "<cannot read memory>"},
		{Value{}, "<unavailable>"},
	} {
		// The interpreter worker carries the same bounded tree as the native adapter.
		before := Variable{Name: "x", Value: tc.value, Handle: 9}
		var wire bytes.Buffer
		if err := gob.NewEncoder(&wire).Encode(before); err != nil {
			t.Fatal(err)
		}
		var after Variable
		if err := gob.NewDecoder(&wire).Decode(&after); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before, after) {
			t.Fatalf("transport changed structure: %+v", after)
		}
		if got := RenderValue(after.Value); got != tc.want {
			t.Errorf("got %q, want %q", got, tc.want)
		}
	}
}

func TestStringPreviewUTF8Bounds(t *testing.T) {
	for _, text := range []string{strings.Repeat("界", 1000), strings.Repeat("界", 85) + "\xe7", strings.Repeat("a", 255) + "λ"} {
		v := StringPreview(text, true)
		if !v.Truncated || len(v.Text) > PreviewStringBytes || !utf8.ValidString(v.Text) {
			t.Fatalf("bad clipped string: %+v", v)
		}
		if !strings.HasSuffix(RenderValue(v), "\"…") {
			t.Fatal(RenderValue(v))
		}
	}
	v := StringPreview(strings.Repeat("a", PreviewStringBytes), false)
	if v.Truncated {
		t.Fatal("truncated complete string at boundary")
	}
}

func TestRendererBoundsUnboundedDescriptions(t *testing.T) {
	v := Value{Kind: NumberValue, Text: "1"}
	for range 8 {
		v = composite(ConstructorValue, "Box", v)
	}
	if got := RenderValue(v); got != "Box (Box (Box (Box (Box …))))" {
		t.Fatal(got)
	}
	list := Value{Kind: ListValue}
	for range 100 {
		list.Fields = append(list.Fields, ValueField{Value: Value{Kind: NumberValue, Text: "1"}})
	}
	if got := RenderValue(list); strings.Count(got, "1") != PreviewItems || !strings.HasSuffix(got, ", …]") {
		t.Fatal(got)
	}
	tree := Value{Kind: ListValue}
	for range PreviewItems {
		tree.Fields = append(tree.Fields, ValueField{Value: list})
	}
	got := RenderValue(tree)
	if strings.Count(got, "1") >= PreviewNodes || !strings.Contains(got, "…") {
		t.Fatal("node budget not enforced")
	}
}
