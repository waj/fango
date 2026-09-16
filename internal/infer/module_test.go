package infer_test

import (
	"github.com/waj/fango/internal/types"
	"testing"
)

func TestModuleFunctionGroups(t *testing.T) {
	for _, tc := range []struct{ name, src string }{
		{"forward polymorphism", `first x = later x
later x = x
one = first 1
two = first "two"`},
		{"mutual inferred", `even n = if n == 0 then True else odd (n - 1)
odd n = if n == 0 then False else even (n - 1)`},
		{"mutual annotations", `left : a -> Bool -> a
left x stop = if stop then x else right x True
right : a -> Bool -> a
right x stop = if stop then x else left x True`},
		{"equations", `left [] = True
left [_ | rest] = right rest
right [] = False
right [_ | rest] = left rest`},
		{"nullary", `first() = second()
second() = 42`},
		{"effects", `effect Ask
    ask : () -> Int
left n = if n == 0 then ask() else right (n - 1)
right n = left n`},
		{"records", `type Point = { x : Int }
left p stop = if stop then p.x else right p True
right : Point -> Bool -> Int
right p stop = left p stop`},
		{"callback", `left x stop = if stop then x else apply right x
right x = left x True
apply action x = action x`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, es := check(t, tc.src)
			if len(es) > 0 {
				t.Fatal(es)
			}
		})
	}
	ck, _, es := check(t, `left n = if n == 0 then True else right (n - 1)
right n = left n`)
	if len(es) > 0 {
		t.Fatal(es)
	}
	for _, name := range []string{"left", "right"} {
		sch, _ := ck.Env.Lookup(name)
		if len(sch.Preds) != 2 {
			t.Fatalf("%s: %s", name, types.ShowScheme(sch))
		}
	}
}

func TestModuleFunctionGroupErrors(t *testing.T) {
	for _, tc := range []struct{ name, src, title string }{
		{"value forward", "first = later\nlater = 1", "NAMING ERROR"},
		{"lambda value forward", "first x = later x\nlater = \\x -> x", "NAMING ERROR"},
		{"local forward", "first x =\n    left y = right y\n    right y = y\n    left x", "NAMING ERROR"},
		{"future parameter shadow", "first later = later\nlater x = x", "SHADOWING"},
		{"future local shadow", "first x =\n    later = x\n    later\nlater x = x", "SHADOWING"},
		{"mixed cycle", "value = function()\nfunction() = value", "CYCLIC VALUE DEFINITION"},
		{"polymorphic recursion", "left x = right [x]\nright x = left x", "TYPE MISMATCH"},
		{"later instance", "class C a\n    c : a -> Bool\nfirst() = later (c ())\ninstance C ()\n    c _ = True\nlater x = x", "MISSING INSTANCE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, es := check(t, tc.src)
			if len(es) == 0 || es[0].(checkErr).title != tc.title {
				t.Fatalf("got %v, want %s", es, tc.title)
			}
		})
	}
}
