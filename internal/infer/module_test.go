package infer_test

import (
	"testing"

	"github.com/waj/fango/internal/types"
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
		{"lambda value forward", "first x = later x\nlater = { x -> x }", "NAMING ERROR"},
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

func TestLocalRecursiveEffects(t *testing.T) {
	const effect = "effect Ask\n    ask : () -> Int\n"
	for _, tc := range []struct{ name, src, want string }{
		{"self", `outer n =
    go x = outer x
    if n == 0 then ask() else go (n - 1)`, ""},
		{"self annotated helper", `outer n =
    go : Int ->{Ask} Int
    go x = outer x
    if n == 0 then ask() else go (n - 1)`, ""},
		{"mutual annotated helper", `outer n =
    go : Int ->{Ask} Int
    go x = other x
    if n == 0 then ask() else go (n - 1)
other n = outer n`, ""},
		{"nested recursion", `outer n =
    loop x =
        go y = loop y
        if x == 0 then ask() else go (x - 1)
    loop n`, ""},
		{"returned lambda", `outer n =
    go : Int ->{Ask} (() -> Int)
    go x =
        value = other x
        { _ -> value }
    if n == 0 then ask() else (go (n - 1))()
other n = outer n`, ""},
		{"independent polymorphism", `outer n =
    identity x = x
    flag = identity True
    if flag then other (identity n) else 0
other n = if n == 0 then 0 else outer (n - 1)`, ""},
		{"overstated returned lambda", `outer n =
    go : Int ->{Ask} (() ->{Ask} Int)
    go x =
        value = other x
        { _ -> value }
    if n == 0 then ask() else (go (n - 1))()
other n = outer n`, "EFFECT MISMATCH"},
		{"nested annotated recursion", `outer n =
    loop x =
        go : Int ->{Ask} Int
        go y = loop y
        if x == 0 then ask() else go (x - 1)
    loop n`, ""},
		{"pure recursive helper", `outer n =
    go x = if x == 0 then 0 else go (x - 1)
    if n == 0 then go n else other (n - 1)
other n = outer n`, ""},
		{"understated annotation", `outer n =
    go : Int -> Int
    go x = other x
    if n == 0 then ask() else go (n - 1)
other n = outer n`, "EFFECT MISMATCH"},
		{"overstated annotation", `outer n =
    go : Int ->{Ask} Int
    go x = other x
    if n == 0 then 0 else go (n - 1)
other n = outer n`, "EFFECT MISMATCH"},
		{"wrong result type", `outer n =
    go : Int ->{Ask} Bool
    go x = other x
    if n == 0 then ask() else go (n - 1)
other n = outer n`, "TYPE MISMATCH"},
		{"escaping annotated row", `outer n =
    go : Int ->{Ask | e} Int
    go x = other x
    if n == 0 then ask() else go (n - 1)
other n = outer n`, "ANNOTATION TOO GENERAL"},
		{"rigid effect parameter", `effect Raise a
    raise : a -> Int
outer payload n =
    go : Int ->{Raise a} Int
    go x = other payload x
    if n == 0 then raise payload else go (n - 1)
other payload n = outer payload n`, "EFFECT MISMATCH"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, errs := check(t, effect+tc.src)
			if tc.want == "" {
				if len(errs) != 0 {
					t.Fatal(errs)
				}
				return
			}
			for _, err := range errs {
				if err.(checkErr).title == tc.want {
					return
				}
			}
			t.Fatalf("got %v, want %s", errs, tc.want)
		})
	}
}
