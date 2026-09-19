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
