package objectcodec

import (
	"math"
	"testing"

	"github.com/waj/fango/internal/meta"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/types"
)

type fixture struct {
	Type types.Type
	ADT  *types.ADTInfo
	Span source.Span
	Bits []float64
}

func TestNilAndEmptyReflectionVisibilityStayDistinct(t *testing.T) {
	in := []*meta.TypeRepr{{Visible: nil}, {Visible: map[int]bool{}}}
	data, err := Encode(in)
	if err != nil {
		t.Fatal(err)
	}
	var out []*meta.TypeRepr
	if err := Decode(data, &out, Context{Types: Registry((*meta.TypeRepr)(nil))}); err != nil {
		t.Fatal(err)
	}
	if out[0].Visible != nil || out[1].Visible == nil || len(out[1].Visible) != 0 {
		t.Fatalf("visibility changed: %#v", out)
	}
}

func TestGraphRoundTripPreservesSharingAndBits(t *testing.T) {
	f := source.NewFile("M.fango", []byte("value = 1\n"))
	con := &types.TCon{Unique: 7, Name: "M.T"}
	adt := &types.ADTInfo{Con: con}
	ctor := &types.CtorInfo{Name: "M.T", Result: con}
	adt.Ctors = []*types.CtorInfo{ctor}
	in := fixture{Type: con, ADT: adt, Span: source.Span{File: f, Start: 8, End: 9}, Bits: []float64{math.Float64frombits(0x7ff8000000000001), math.Copysign(0, -1)}}
	data, err := Encode(in)
	if err != nil {
		t.Fatal(err)
	}
	var out fixture
	if err := Decode(data, &out, Context{Types: Registry((*types.TCon)(nil), types.Row{}), Sources: map[string]*source.File{f.Name: f}}); err != nil {
		t.Fatal(err)
	}
	got := out.Type.(*types.TCon)
	if got != out.ADT.Con || got != out.ADT.Ctors[0].Result {
		t.Fatal("shared constructor identity was not preserved")
	}
	if out.Span.File != f || out.Span.Start != 8 || out.Span.End != 9 {
		t.Fatalf("span = %#v", out.Span)
	}
	for i := range in.Bits {
		if math.Float64bits(in.Bits[i]) != math.Float64bits(out.Bits[i]) {
			t.Fatalf("bits[%d] changed", i)
		}
	}
}

func TestDecodeRejectsStaleProvenance(t *testing.T) {
	f := source.NewFile("M.fango", []byte("abc"))
	data, err := Encode(source.Span{File: f, Start: 1, End: 3})
	if err != nil {
		t.Fatal(err)
	}
	var out source.Span
	if err := Decode(data, &out, Context{Sources: map[string]*source.File{"M.fango": source.NewFile("M.fango", []byte("a"))}}); err == nil {
		t.Fatal("accepted offsets outside current source")
	}
}

func TestSpanProvenanceRelocatesAfterCommentInsertion(t *testing.T) {
	old := source.NewFile("M.fango", []byte("value = target\n"))
	data, err := Encode(source.Span{File: old, Start: 8, End: 14})
	if err != nil {
		t.Fatal(err)
	}
	current := source.NewFile("M.fango", []byte("-- inserted\nvalue = target\n"))
	var out source.Span
	if err := Decode(data, &out, Context{Sources: map[string]*source.File{"M.fango": current}}); err != nil {
		t.Fatal(err)
	}
	if got := string(current.Content[out.Start:out.End]); got != "target" || out.Start != 20 {
		t.Fatalf("span relocated to %d:%d %q", out.Start, out.End, got)
	}
}

type ring struct {
	Name string
	Next *ring
}

func TestCyclesAndSelfReferenceRoundTrip(t *testing.T) {
	a := &ring{Name: "a"}
	b := &ring{Name: "b", Next: a}
	a.Next = b
	data, err := Encode(a)
	if err != nil {
		t.Fatal(err)
	}
	var out *ring
	if err := Decode(data, &out, Context{Types: Registry((*ring)(nil))}); err != nil {
		t.Fatal(err)
	}
	if out.Name != "a" || out.Next.Name != "b" || out.Next.Next != out {
		t.Fatalf("cycle was not preserved: %#v", out)
	}
}

func TestRepeatedStringsAreStoredOnce(t *testing.T) {
	one := []string{"a repeated identifier"}
	many := make([]string, 64)
	for i := range many {
		many[i] = one[0]
	}
	small, err := Encode(one)
	if err != nil {
		t.Fatal(err)
	}
	large, err := Encode(many)
	if err != nil {
		t.Fatal(err)
	}
	if grown := len(large) - len(small); grown > 2*len(many) {
		t.Fatalf("64 repeats of one string cost %d extra bytes; it is not pooled", grown)
	}
}

func TestForeignAndDamagedPayloadsAreRejected(t *testing.T) {
	data, err := Encode(&ring{Name: "a"})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		wire []byte
	}{
		{"empty", nil},
		{"other encoding", []byte(`{"root":{"kind":"nil"}}`)},
		{"truncated", data[:len(data)-1]},
		{"padded", append(append([]byte(nil), data...), 0)},
		{"truncated header", data[:3]},
	}
	for _, c := range cases {
		var out *ring
		if err := Decode(c.wire, &out, Context{Types: Registry((*ring)(nil))}); err == nil {
			t.Errorf("%s was accepted", c.name)
		}
	}
}

// The registry is what names an interface element concrete, so a graph whose
// dynamic types are not registered cannot be installed by mistake.
func TestUnregisteredInterfaceElementIsRejected(t *testing.T) {
	data, err := Encode(fixture{Type: &types.TCon{Unique: 1, Name: "M.T"}})
	if err != nil {
		t.Fatal(err)
	}
	var out fixture
	if err := Decode(data, &out, Context{}); err == nil {
		t.Fatal("an unregistered dynamic type was accepted")
	}
}

// sizeTableAt returns the offset of the first node's recorded size.
func sizeTableAt(t *testing.T, data []byte) int {
	t.Helper()
	c := &cursor{data: data, at: len(magic)}
	strings, err := c.count()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < strings; i++ {
		n, err := c.count()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.take(n); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.count(); err != nil {
		t.Fatal(err)
	}
	return c.at
}

// A node's recorded size bounds it. Moving a byte of it elsewhere keeps the
// value area the right length, so only the per-node boundary catches it.
func TestNodeEndingOutsideItsRecordedSizeIsRejected(t *testing.T) {
	data, err := Encode(&ring{Name: "a"})
	if err != nil {
		t.Fatal(err)
	}
	at := sizeTableAt(t, data)
	if data[at] < 2 || data[at] >= 0x80 {
		t.Skipf("node size %d is not a single-byte varint this test can shift", data[at])
	}
	damaged := append([]byte(nil), data...)
	damaged[at]--
	var out *ring
	if err := Decode(damaged, &out, Context{Types: Registry((*ring)(nil))}); err == nil {
		t.Fatal("a node overrunning its recorded size was accepted")
	}
}
