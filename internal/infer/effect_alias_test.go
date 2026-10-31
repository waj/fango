package infer_test

import (
	"strings"
	"testing"
)

const aliasPrelude = `effect Log
    info : String -> ()
effect Db
    query : String -> Int
effect Clock
    now : () -> Int
alias App = {Log, Db}
`

func TestEffectAliasAccepted(t *testing.T) {
	for name, src := range map[string]string{
		"one member":     "f : () ->{App} Int\nf() = query \"x\"",
		"every member":   "f : () ->{App} Int\nf() =\n    info \"x\"\n    query \"y\"",
		"beside a label": "f : () ->{App, Clock} Int\nf() = query \"x\" + now()",
		"through a callee": "f : () ->{App} Int\nf() = query \"x\"\n" +
			"g : () ->{App} Int\ng() = f()\nh : () ->{Log, Db} Int\nh() = f()",
		"open tail":       "f : (() ->{e} a) ->{App, e} a\nf g =\n    info \"x\"\n    g()",
		"point free":      "g : () ->{Db} Int\ng() = query \"x\"\nf : () ->{App} Int\nf = g",
		"returned local":  "f : Int -> (() ->{App} Int)\nf n =\n    g() = query \"x\"\n    g",
		"local":           "f : Int -> Int\nf n =\n    g : () ->{App} Int\n    g() = query \"x\"\n    handle (handle g() on\n        info _ -> resume ()) on\n        query _ -> resume n",
		"pure callback":   "run : (() ->{App} Int) -> Int\nrun g = 0\np() = 1\nx = run p\ny = run { 1 }",
		"stored":          "type A = A (() ->{App} Int)\nxs = [A { 1 }, A { query \"x\" }]",
		"row argument":    "type J e = J (() ->{e} Int)\na : J App\na = J { 1 }\nb : J {App, Clock}\nb = J { now() }",
		"nested":          "alias Web = {App, Clock}\nf : () ->{Web} Int\nf() = now()",
		"declared later":  "f : () ->{Later} Int\nf() = now()\nalias Later = {Clock, Log}",
		"overlap":         "alias Ops = {Log, Clock}\nf : () ->{App, Ops} ()\nf() = info \"x\"",
		"in an operation": "effect Wrap\n    wrapped : (() ->{App} ()) -> ()",
		"member handled":  "f : () ->{App} Int\nf() = query \"x\"\ng : () ->{Log} Int\ng() = handle f() on\n    query _ -> resume 1",
		"in a parameter":  "f : (() ->{App} Int) -> Int\nf g = 0",
		"parameter":       "effect Fail a\n    abort fail : a -> b\nalias Risky e = {Log, Fail e}\nf : e ->{Risky e} Int\nf e = fail e",
		"two at once":     "effect Fail a\n    abort fail : a -> b\nalias Risky e = {Fail e}\nf : () ->{Risky Int, Risky String} Int\nf() =\n    g()\n    fail \"x\"\ng : () ->{Risky Int} ()\ng() = fail 1",
		"they coincide":   "effect Fail a\n    abort fail : a -> b\nalias Both a b = {Fail a, Fail b}\nf : () ->{Both Int Int} Int\nf() = fail 1",
		"row argument at": "effect Fail a\n    abort fail : a -> b\nalias Risky e = {Fail e}\ntype J e = J (() ->{e} Int)\na : J (Risky Int)\na = J { 1 }\nb : J {Risky Int}\nb = J { fail 1 }",
		"scoped runner":   "run : (() ->{App, #s} a) ->{e} a\nrun g = handle (handle g() on\n        info _ -> resume ()) on\n    query _ -> resume 0",
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, errs := check(t, aliasPrelude+src); len(errs) != 0 {
				t.Fatal(errs)
			}
		})
	}
}

func TestEffectAliasRejected(t *testing.T) {
	for name, tc := range map[string]struct{ src, want string }{
		"pure body":          {"f : () ->{App} Int\nf() = 1", "`f` declares `App`, but its body performs none"},
		"pure local":         {"f : Int -> Int\nf n =\n    g : () ->{App} Int\n    g() = 1\n    n", "`g` declares `App`, but"},
		"tail only":          {"f : (() ->{e} a) ->{App, e} a\nf g = g()", "`f` declares `App`, but"},
		"one of two":         {"alias Ops = {Clock}\nf : () ->{App, Ops} Int\nf() = query \"x\"", "`f` declares `Ops`, but"},
		"outside":            {"f : () ->{App} Int\nf() = now()", "EFFECT MISMATCH"},
		"unused label":       {"f : () ->{App, Clock} Int\nf() = query \"x\"", "lists `Clock`, which its body does not perform"},
		"narrow caller":      {"f : () ->{App} Int\nf() = query \"x\"\ng : () ->{Db} Int\ng() = f()", "EFFECT MISMATCH"},
		"wide callback":      {"run : (() ->{App} Int) -> Int\nrun g = 0\nx = run { now() }", "EFFECT MISMATCH"},
		"label after":        {"f : () ->{App, Log} Int\nf() = query \"x\"", "DUPLICATE EFFECT"},
		"label before":       {"f : () ->{Log, App} Int\nf() = query \"x\"", "DUPLICATE EFFECT"},
		"wrong application":  {"effect Fail a\n    abort fail : a -> b\nalias Risky e = {Fail e}\nf : () ->{Risky Int, Risky String} Int\nf() = fail 1", "`f` declares `Risky String`, but its body performs none"},
		"unused parameter":   {"alias E a = {Log}", "never uses its parameter `a`"},
		"repeated parameter": {"effect Fail a\nalias E a a = {Fail a}", "appears twice"},
		"too few":            {"effect Fail a\nalias E a = {Fail a}\nf : () ->{E} ()\nf() = ()", "takes 1 type argument(s)"},
		"row as a type":      {"effect Fail a\nalias E a = {Fail a}\nalias T = E Int", "E"},
		"arguments":          {"f : () ->{App Int} Int\nf() = query \"x\"", "takes no type arguments"},
		"empty":              {"alias E = {}", "BAD EFFECT ALIAS"},
		"tail":               {"alias E = {Log, e}", "BAD EFFECT ALIAS"},
		"scoped":             {"alias E = {Log, #s}", "BAD EFFECT ALIAS"},
		"self":               {"alias E = {E}", "BAD EFFECT ALIAS"},
		"cycle":              {"alias E = {G}\nalias G = {E}", "BAD EFFECT ALIAS"},
		"type variable":      {"effect Box a\nalias E = {Box a}", "type variable `a`"},
		"unknown":            {"alias E = {Nope}", "I don't know an effect named `Nope`"},
		"twice":              {"alias E = {Log}\nalias E = {Db}", "more than once"},
		"effect name":        {"alias Db = {Log}", "collides with an effect"},
		"type name":          {"type T = T\nalias T = {Log}", "collides with a type"},
		"as a type":          {"f : App -> Int\nf x = 0", "App"},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, errs := check(t, aliasPrelude+tc.src)
			if len(errs) == 0 {
				t.Fatal("accepted")
			}
			var text strings.Builder
			for _, e := range errs {
				d := e.(checkErr)
				text.WriteString(d.title + "\n" + d.body + "\n")
			}
			if !strings.Contains(text.String(), tc.want) {
				t.Fatalf("want %q in:\n%s", tc.want, text.String())
			}
		})
	}
}

func TestTypeAliasAccepted(t *testing.T) {
	for name, src := range map[string]string{
		"plain":            "alias Age = Int\nf : Age -> Int\nf x = x + 1\ny : Int\ny = f 1",
		"applied":          "alias Pair a = (a, a)\nf : Pair Int -> Int\nf (x, _) = x",
		"polymorphic":      "alias Pair a = (a, a)\nf : Pair a -> a\nf (x, _) = x",
		"declared later":   "f : Later -> Int\nf x = x\nalias Later = Inner\nalias Inner = Int",
		"function":         "alias Fn a b = a -> b\nf : Fn Int Int\nf x = x",
		"row parameter":    "alias Fn e = Int ->{e} Int\nf : Fn {Log}\nf x =\n    info \"x\"\n    x",
		"row variable":     "alias Fn e = Int ->{e} Int\nf : Fn e -> Int ->{e} Int\nf g x = g x",
		"row joins tail":   "alias Fn e = Int ->{Log, e} Int\nf : Fn {Log, Db} -> Int\nf g = 0",
		"alias in a row":   "alias Key = String\nalias A = {B Key}\neffect B a\n    b : a -> ()\nf : () ->{A} ()\nf() = b \"x\"",
		"through a type":   "alias Run e = J e\ntype J e = J (() ->{e} Int)\na : Run {Log}\na = J { 1 }\nb : Run e\nb = J { 1 }",
		"type through":     "type A e = A (Run e)\nalias Run e = J e\ntype J e = J (() ->{e} Int)\na : A {Log}\na = A (J { 1 })",
		"type before type": "type A e = A (B e)\ntype B e = B (() ->{e} Int)\na : A {Log}\na = A (B { 1 })",
		"constructor":      "alias Pair a = (a, a)\ntype T = T (Pair Int)\nx = T (1, 2)",
		"recursive type":   "alias Forest = List Tree\ntype Tree = Node Forest\nx : Tree\nx = Node []",
		"in an operation":  "alias Key = String\neffect K\n    get : Key -> Int",
		"instance":         "alias Boxed = Box Int\ntype Box a = Box a\nclass C a\n    c : a -> Int\ninstance C Boxed\n    c x = 1\ny = c (Box 1)",
		"local annotation": "alias Age = Int\nf x =\n    g : Age -> Age\n    g y = y\n    g x",
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, errs := check(t, aliasPrelude+src); len(errs) != 0 {
				t.Fatal(errs)
			}
		})
	}
}

func TestTypeAliasRejected(t *testing.T) {
	for name, tc := range map[string]struct{ src, want string }{
		"self":               {"alias L = List L", "BAD TYPE ALIAS"},
		"cycle":              {"alias A = List B\nalias B = List A", "BAD TYPE ALIAS"},
		"through an effect":  {"alias A = () ->{R} Int\nalias R = {B A}\neffect B a", "cannot include itself"},
		"unused parameter":   {"alias P a = Int", "never uses its parameter `a`"},
		"repeated":           {"alias P a a = (a, a)", "appears twice"},
		"free variable":      {"alias P = List a", "type variable `a`"},
		"free row":           {"alias P = Int ->{e} Int", "row variable `e`"},
		"no arguments":       {"alias P a = (a, a)\nf : P -> Int\nf x = 0", "TYPE ARITY"},
		"too many":           {"alias P a = (a, a)\nf : P Int Int -> Int\nf x = 0", "TYPE ARITY"},
		"plain with args":    {"alias P = Int\nf : P Int -> Int\nf x = 0", "TYPE ARITY"},
		"twice":              {"alias P = Int\nalias P = Bool", "more than once"},
		"both kinds":         {"alias P = Int\nalias P = {Log}", "more than once"},
		"effect name":        {"alias Db = Int", "collides with an effect"},
		"type name":          {"type T = T\nalias T = Int", "collides with a type"},
		"effect alias":       {"alias W = App", "`App` is an effect alias, not a type"},
		"row argument":       {"alias P a = (a, a)\nf : P {Log} -> Int\nf x = 0", "KIND MISMATCH"},
		"type for a row":     {"alias Fn e = Int ->{e} Int\nf : Fn Int -> Int\nf x = 0", "KIND MISMATCH"},
		"both kinds of use":  {"alias Fn e = e ->{e} Int", "KIND MISMATCH"},
		"row through a type": {"alias Run e = J e\ntype J e = J (() ->{e} Int)\nf : Run Int -> Int\nf x = 0", "KIND MISMATCH"},
		"mixed through":      {"alias Run e = (e, J e)\ntype J e = J (() ->{e} Int)", "KIND MISMATCH"},
		"field through":      {"type A = A (Run Int)\nalias Run e = J e\ntype J e = J (() ->{e} Int)", "KIND MISMATCH"},
		"not nominal":        {"alias Age = Int\ninstance Show Age\n    show _ = \"\"", "OVERLAPPING INSTANCE"},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, errs := check(t, aliasPrelude+tc.src)
			if len(errs) == 0 {
				t.Fatal("accepted")
			}
			var text strings.Builder
			for _, e := range errs {
				d := e.(checkErr)
				text.WriteString(d.title + "\n" + d.body + "\n")
			}
			if !strings.Contains(text.String(), tc.want) {
				t.Fatalf("want %q in:\n%s", tc.want, text.String())
			}
		})
	}
}
