package infer_test

import (
	"github.com/waj/fango/internal/infer"
	"strings"
	"testing"

	"github.com/waj/fango/internal/types"
)

func TestConditionalInstanceDiagnostics(t *testing.T) {
	const prefix = `class Inspect a
    inspect : a -> String
class Other a
    other : a -> String
type Box a = Box a
`
	for _, tc := range []struct{ name, src, title string }{
		{"same context reordered", "instance (Show a, Eq a) => Inspect a\n    inspect x = \"first\"\ninstance (Eq b, Show b, Eq b) => Inspect b\n    inspect x = \"second\"", "OVERLAPPING INSTANCE"},
		{"default does not imply context", "instance Inspect a\n    inspect x = \"fallback\"\ninstance Show a => Inspect a\n    inspect x = show x\nf : Show a => a -> String\nf x = inspect x", "MISSING CONSTRAINT"},
		{"fallback cannot hide earlier blanket edge", "instance Other a => Inspect a\n    inspect x = other x\ninstance Inspect a\n    inspect x = \"fallback\"\ninstance Inspect a => Other a\n    other x = inspect x", "INSTANCE CONTEXT"},
		{"cyclic guard is an error", "instance Inspect (Box a)\n    inspect x = \"fallback\"\ninstance Other (Box a) => Inspect (Box a)\n    inspect x = \"conditional\"\ninstance Inspect (Box a) => Other (Box a)\n    other x = \"cycle\"\nmain = inspect (Box True)", "INSTANCE RESOLUTION"},
		{"growing guard is an error", "instance Inspect (Box a)\n    inspect x = \"fallback\"\ninstance Inspect (Box (Box a)) => Inspect (Box a)\n    inspect x = \"conditional\"\nmain = inspect (Box True)", "INSTANCE RESOLUTION"},
		{"no fallback to less specific head", "instance Inspect a\n    inspect x = \"fallback\"\ninstance Other a => Inspect (Box a)\n    inspect x = \"conditional\"\nmain = inspect (Box True)", "MISSING INSTANCE"},
		{"no numeric seed", "instance Inspect a\n    inspect x = \"fallback\"\nmain = inspect (\\x -> x)", "AMBIGUOUS CONSTRAINT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, errs := check(t, prefix+tc.src)
			if len(errs) == 0 || errs[0].(checkErr).title != tc.title {
				t.Fatalf("got %v, want %s", errs, tc.title)
			}
		})
	}
}

func TestInstanceVisibilityAndGivenContext(t *testing.T) {
	ck, infos, errs := check(t, `class Inspect a
    inspect : a -> String
type Foo = Foo
instance Inspect a
    inspect x = "fallback"
before() = inspect Foo
instance Show a => Inspect a
    inspect x = show x
forward x = inspect x
`)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	p := types.Pred{Class: "Inspect", Ty: ck.TypeNames["Foo"]}
	var before infer.DeclInfo
	for _, info := range infos {
		if info.Name == "before" {
			before = info
		}
	}
	r := ck.ResolveInstance(p, "", before.InstanceLimit, nil)
	if r.Error != nil || r.Instance == nil || len(r.Instance.Preds) != 0 {
		t.Fatalf("earlier call: %+v", r)
	}
	r = ck.ResolveInstance(p, "", len(ck.Instances), []types.Pred{ck.StandardPred("Show", p.Ty)})
	if r.Error != nil || r.Instance == nil || len(r.Instance.Preds) != 1 {
		t.Fatalf("given context was not used: %+v", r)
	}
	if !strings.Contains(r.Instance.Name, "_context_") {
		t.Fatal("conditional factory lacks a distinct name")
	}
	if !ck.CanResolve(p, "") {
		t.Fatal("missing Show Foo should permit the fallback")
	}
}
