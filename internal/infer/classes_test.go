package infer

import (
	"github.com/waj/fango/internal/types"
	"testing"
)

func TestClassConstraints(t *testing.T) {
	for _, tc := range []struct{ src, want string }{
		{"same x y = x == y", "Eq a => a -> a -> Bool"},
		{"text x = show x", "Show a => a -> String"},
		{"twice : Num number => number -> number\ntwice x = x + x", "Num a => a -> a"},
		{"outer x =\n    equal y = x == y\n    equal x", "Eq a => a -> Bool"},
		{"outer x =\n    same : Eq a => a -> a -> Bool\n    same l r = l == r\n    same x x", "Eq a => a -> Bool"},
	} {
		ck, infos, errs := check(t, tc.src)
		if len(errs) > 0 {
			t.Errorf("%s: %v", tc.src, errs)
			continue
		}
		if got := types.ShowScheme(checkedScheme(ck, infos[len(infos)-1])); got != tc.want {
			t.Errorf("%s: got %s, want %s", tc.src, got, tc.want)
		}
	}
}

func TestStructuralInstanceHeads(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"specialized instance resolves after defaulting",
			"type Box a = Box a\nclass C a\n    c : a -> Int\ninstance C (Box Int)\n    c x = 1\nf = c (Box 0)",
			"Int"},
		{"generic instance still yields a scheme constraint",
			"type Box a = Box a\ninstance Show (Box Int)\n    show b = \"int\"\ninstance Show a => Show (Box a)\n    show b = \"box\"\nf b = show (Box b)",
			"Show (Box a) => a -> String"},
		{"repeated head variable",
			"type Pair a b = Pair a b\ninstance Eq (Pair a a)\n    eq x y = True\nf : Pair Int Int -> Bool\nf p = p == p",
			"Pair Int Int -> Bool"},
		{"nested head with context",
			"type Box a = Box a\ntype Wrap a = Wrap a\ninstance Show a => Show (Box (Wrap a))\n    show x = \"bw\"\nf : Box (Wrap Int) -> String\nf x = show x",
			"Box (Wrap Int) -> String"},
		{"comparable overlap accepted",
			"type Box a = Box a\nclass C a\n    c : a -> Int\ninstance C (Box a)\n    c x = 0\ninstance C (Box Int)\n    c x = 1\nf : Box String -> Int\nf b = c b",
			"Box String -> Int"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ck, infos, errs := check(t, tc.src)
			if len(errs) > 0 {
				t.Fatalf("unexpected errors: %v", errs)
			}
			if got := types.ShowScheme(checkedScheme(ck, infos[len(infos)-1])); got != tc.want {
				t.Errorf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func TestClassDiagnostics(t *testing.T) {
	for _, tc := range []struct{ name, src, title string }{
		{"missing context", "same : a -> a -> Bool\nsame x y = x == y", "MISSING CONSTRAINT"},
		{"number is ordinary", "twice : number -> number\ntwice x = x + x", "MISSING CONSTRAINT"},
		{"no automatic equality", "type T = T\nmain = T == T", "MISSING INSTANCE"},
		{"no function instance", "identity : Int -> Int\nidentity x = x\nmain = show identity", "MISSING INSTANCE"},
		{"extra method variable", "class C a\n    c : a -> b", "NAMING ERROR"},
		{"method must mention parameter", "class C a\n    c : Int -> Int", "CLASS METHOD TYPE"},
		{"open method effects", "class C a\n    c : a ->{e} a", "NAMING ERROR"},
		{"function instance head", "class C a\n    c : a -> a\ninstance C (Int -> Int)\n    c x = x", "INSTANCE HEAD"},
		{"structural duplicate", "type Box a = Box a\nclass C a\n    c : a -> a\ninstance C (Box a)\n    c x = x\ninstance C (Box b)\n    c x = x", "OVERLAPPING INSTANCE"},
		{"incomparable overlap", "type Pair a b = Pair a b\nclass C a\n    c : a -> a\ninstance C (Pair Int a)\n    c x = x\ninstance C (Pair a Int)\n    c x = x", "OVERLAPPING INSTANCE"},
		{"undetermined instance choice", "type Box a = Box\nclass C a\n    c : a -> Bool\ninstance C (Box Int)\n    c x = True\ninstance C (Box a)\n    c x = False\nf = c Box", "AMBIGUOUS CONSTRAINT"},
		{"missing method", "class C a\n    c : a -> a\n    d : a -> a\ninstance C Int\n    c x = x", "MISSING METHOD"},
		{"duplicate method", "class C a\n    c : a -> a\ninstance C Int\n    c x = x\n    c y = y", "DUPLICATE METHOD"},
		{"unknown method", "class C a\n    c : a -> a\ninstance C Int\n    c x = x\n    d x = x", "UNKNOWN METHOD"},
		{"overlap", "instance Eq Int\n    eq x y = True", "OVERLAPPING INSTANCE"},
		{"unknown class", "f : Missing a => a -> a\nf x = x", "UNKNOWN CLASS"},
		{"ambiguous annotation", "f : Eq a => Int -> Int\nf x = x", "AMBIGUOUS CONSTRAINT"},
		{"class source order", "f : C a => a -> a\nf x = x\nclass C a\n    c : a -> a", "UNKNOWN CLASS"},
		{"no custom default", "class C a\n    c : a -> Bool\nf = c 1", "AMBIGUOUS CONSTRAINT"},
		{"unsupported deriving", "type T = T deriving (Ord)", "CANNOT DERIVE"},
		{"function deriving", "type T = T (Int -> Int) deriving (Show)", "MISSING INSTANCE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, errs := check(t, tc.src)
			if len(errs) == 0 || errs[0].(checkErr).title != tc.title {
				t.Fatalf("got %v, want %s", errs, tc.title)
			}
		})
	}
}
