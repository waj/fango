package infer_test

import (
	"testing"

	"github.com/waj/fango/internal/types"
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
		{"a head group with a choice keeps the whole predicate",
			"type Box a = Box a\ninstance Show (Box a)\n    show b = \"box\"\ninstance Show a => Show (Box a)\n    show b = \"shown\"\nf b = show (Box b)",
			"Show (Box a) => a -> String"},
		{"repeated head variable",
			"type Pair a b = Pair a b\ninstance Eq (Pair a a)\n    (==) x y = True\nf : Pair Int Int -> Bool\nf p = p == p",
			"Pair Int Int -> Bool"},
		{"nested head with context",
			"type Box a = Box a\ntype Wrap a = Wrap a\ninstance Show a => Show (Box (Wrap a))\n    show x = \"bw\"\nf : Box (Wrap Int) -> String\nf x = show x",
			"Box (Wrap Int) -> String"},
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

// Deferred record obligations are discharged inside instance method bodies
// too, so a projected field has a known type before the method's predicate
// obligations are reduced.
func TestInstanceMethodRecordFields(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"projection feeds another class",
			"type Point = { x : Float }\nclass C a\n    c : a -> String\ninstance C Point\n    c p = show p.x\nf : Point -> String\nf p = c p",
			"Point -> String"},
		{"projection feeds an ordinary function",
			"type Point = { x : Float }\nclass C a\n    c : a -> Float\ninstance C Point\n    c p = p.x\nf : Point -> Float\nf p = c p",
			"Point -> Float"},
		{"functional update in a method body",
			"type Point = { x : Float }\nclass C a\n    c : a -> a\ninstance C Point\n    c p = { p | x = p.x }\nf : Point -> Point\nf p = c p",
			"Point -> Point"},
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
		{"undetermined instance choice", "type Box a = Box\nclass C a\n    c : a -> Bool\ninstance C (Box Int)\n    c x = True\nf = c Box", "AMBIGUOUS CONSTRAINT"},
		{"specialized argument", "type Box a = Box a\nclass C a\n    c : a -> Int\ninstance C (Box a)\n    c x = 0\ninstance C (Box Int)\n    c x = 1", "OVERLAPPING INSTANCE"},
		{"specialized nested argument", "type Box a = Box a\ntype Wrap a = Wrap a\nclass C a\n    c : a -> Int\ninstance C (Box a)\n    c x = 0\ninstance C (Box (Wrap Int))\n    c x = 1", "OVERLAPPING INSTANCE"},
		{"missing method", "class C a\n    c : a -> a\n    d : a -> a\ninstance C Int\n    c x = x", "MISSING METHOD"},
		{"duplicate method", "class C a\n    c : a -> a\n    d : a -> a\ninstance C Int\n    c x = x\n    d x = x\n    c y = y", "DUPLICATE METHOD"},
		{"unknown method", "class C a\n    c : a -> a\ninstance C Int\n    c x = x\n    d x = x", "UNKNOWN METHOD"},
		{"overlap", "instance Eq Int\n    (==) x y = True", "OVERLAPPING INSTANCE"},
		{"unknown class", "f : Missing a => a -> a\nf x = x", "UNKNOWN CLASS"},
		{"ambiguous annotation", "f : Eq a => Int -> Int\nf x = x", "AMBIGUOUS CONSTRAINT"},
		{"class source order", "f : C a => a -> a\nf x = x\nclass C a\n    c : a -> a", "UNKNOWN CLASS"},
		{"no custom default", "class C a\n    c : a -> Bool\nf = c 1", "AMBIGUOUS CONSTRAINT"},
		{"unknown projection receiver in a method", "opaque : (a -> Int) -> String\nopaque _ = \"opaque\"\nclass C a\n    c : a -> String\ninstance C Int\n    c n = opaque (\\r -> r.field)", "AMBIGUOUS FIELD"},
		{"deriving a class with no deriver", "class C a\n    c : a -> Bool\ntype T = T deriving (C)", "CANNOT DERIVE"},
		{"deriver for an unknown class", "deriver Missing\n    m info x = x", "UNKNOWN CLASS"},
		{"deriver missing a method", "class C a\n    c : a -> Bool\n    d : a -> Bool\nderiver C\n    c info x = x", "MISSING METHOD"},
		{"deriver for an unknown method", "class C a\n    c : a -> Bool\nderiver C\n    c info x = x\n    d info x = x", "UNKNOWN METHOD"},
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
