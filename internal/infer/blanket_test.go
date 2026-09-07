package infer_test

import (
	"testing"

	"github.com/waj/fango/internal/types"
)

func TestCallerSelectedConstraints(t *testing.T) {
	const prelude = `class Inspect a
    inspect : a -> String
instance Show a => Inspect a
    inspect x = show x
instance Inspect String
    inspect x = "special"
type Box a = Box a deriving (Show)
`
	for _, tc := range []struct{ name, src, want string }{
		{"bare variable", "f x = inspect x", "Inspect a => a -> String"},
		{"method value", "f = inspect", "Inspect a => a -> String"},
		{"structural", "f x = show (Box x)", "Show (Box a) => a -> String"},
		{"explicit structural", "f : Show (Box a) => a -> String\nf x = show (Box x)", "Show (Box a) => a -> String"},
		{"local", "f x =\n    local y = inspect y\n    local x", "Inspect a => a -> String"},
		{"captured structural", "f x =\n    local y = show (Box x) ++ y\n    local \"!\"", "Show (Box a) => a -> String"},
		{"local structural", "f x =\n    local y = show (Box y)\n    local x", "Show (Box a) => a -> String"},
		{"numeric default", "main = inspect 42", "String"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ck, infos, errs := check(t, prelude+tc.src)
			if len(errs) > 0 {
				t.Fatal(errs)
			}
			if got := types.ShowScheme(checkedScheme(ck, infos[len(infos)-1])); got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func TestBlanketAndContextDiagnostics(t *testing.T) {
	const classes = `class A a
    aa : a -> String
class B a
    bb : a -> String
type Box a = Box a
type Wrap a = Wrap a
`
	for _, tc := range []struct{ name, src, title string }{
		{"annotation must name class", "instance Show a => A a\n    aa x = show x\nf : Show a => a -> String\nf x = aa x", "MISSING CONSTRAINT"},
		{"structural annotation", "instance Show a => Show (Box a)\n    show x = \"box\"\nf : Show a => Box a -> String\nf x = show x", "MISSING CONSTRAINT"},
		{"local annotation", "instance A a\n    aa x = \"a\"\nf x =\n    local : Show a => a -> String\n    local y = aa y\n    local x", "MISSING CONSTRAINT"},
		{"duplicate blanket", "instance Show a => A a\n    aa x = show x\ninstance Show b => A b\n    aa x = show x", "OVERLAPPING INSTANCE"},
		{"self blanket cycle", "instance A a => A a\n    aa x = \"a\"", "INSTANCE CONTEXT"},
		{"indirect blanket cycle", "instance B a => A a\n    aa x = bb x\ninstance A a => B a\n    bb x = aa x", "INSTANCE CONTEXT"},
		{"blanket cycle despite escape", "instance A Int\n    aa x = \"int\"\ninstance B a => A a\n    aa x = bb x\ninstance A a => B a\n    bb x = aa x", "INSTANCE CONTEXT"},
		{"structured blanket context", "instance Show (Box a) => A a\n    aa x = \"a\"", "INSTANCE CONTEXT"},
		{"unknown context variable", "instance Show b => A (Box a)\n    aa x = \"a\"", "NAMING ERROR"},
		{"open context", "instance Show (a ->{e} a) => A (Box a)\n    aa x = \"a\"", "NAMING ERROR"},
		{"missing blanket evidence", "instance Show a => A a\n    aa x = show x\nmain = aa (Box True)", "MISSING INSTANCE"},
		{"no fallback", "instance A a\n    aa x = \"fallback\"\ninstance B a => A (Box a)\n    aa x = \"specific\"\nmain = aa (Box True)", "MISSING INSTANCE"},
		{"structured cycle", "instance B (Wrap a) => A (Box a)\n    aa x = \"a\"\ninstance A (Box a) => B (Wrap a)\n    bb x = \"b\"\nmain = aa (Box True)", "INSTANCE RESOLUTION"},
		{"growing context", "instance A (Box (Box a)) => A (Box a)\n    aa x = \"a\"\nmain = aa (Box True)", "INSTANCE RESOLUTION"},
		{"custom numeric ambiguity", "instance A Int\n    aa x = \"int\"\nmain = aa 42", "AMBIGUOUS CONSTRAINT"},
		{"phantom ambiguity", "instance A (Box a)\n    aa x = \"a\"\ntype Phantom a = Phantom\ninstance A (Phantom a)\n    aa x = \"phantom\"\nmain = aa Phantom", "AMBIGUOUS CONSTRAINT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, errs := check(t, classes+tc.src)
			if len(errs) == 0 || errs[0].(checkErr).title != tc.title {
				t.Fatalf("got %v, want %s", errs, tc.title)
			}
		})
	}
}

func TestBlanketAvailability(t *testing.T) {
	ck, _, errs := check(t, `class A a
    aa : a -> String
class B a
    bb : a -> String
type Box a = Box a
type Wrap a = Wrap a
instance B (Wrap a) => A (Box a)
    aa x = "a"
instance A (Box a) => B (Wrap a)
    bb x = "b"
`)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	box := ck.TypeNames["Box"].(*types.TCon)
	if ck.CanResolve(types.Pred{Class: "A", Ty: &types.TCon{Unique: box.Unique, Name: box.Name, Args: []types.Type{ck.B.Bool}}}, "") {
		t.Fatal("a cyclic context must not be available")
	}
}
