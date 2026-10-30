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
		"pure body":     {"f : () ->{App} Int\nf() = 1", "`f` declares `App`, but its body performs none"},
		"pure local":    {"f : Int -> Int\nf n =\n    g : () ->{App} Int\n    g() = 1\n    n", "`g` declares `App`, but"},
		"tail only":     {"f : (() ->{e} a) ->{App, e} a\nf g = g()", "`f` declares `App`, but"},
		"one of two":    {"alias Ops = {Clock}\nf : () ->{App, Ops} Int\nf() = query \"x\"", "`f` declares `Ops`, but"},
		"outside":       {"f : () ->{App} Int\nf() = now()", "EFFECT MISMATCH"},
		"unused label":  {"f : () ->{App, Clock} Int\nf() = query \"x\"", "does not exactly match"},
		"narrow caller": {"f : () ->{App} Int\nf() = query \"x\"\ng : () ->{Db} Int\ng() = f()", "EFFECT MISMATCH"},
		"wide callback": {"run : (() ->{App} Int) -> Int\nrun g = 0\nx = run { now() }", "EFFECT MISMATCH"},
		"label after":   {"f : () ->{App, Log} Int\nf() = query \"x\"", "DUPLICATE EFFECT"},
		"label before":  {"f : () ->{Log, App} Int\nf() = query \"x\"", "DUPLICATE EFFECT"},
		"arguments":     {"f : () ->{App Int} Int\nf() = query \"x\"", "takes no type arguments"},
		"empty":         {"alias E = {}", "BAD EFFECT ALIAS"},
		"tail":          {"alias E = {Log, e}", "BAD EFFECT ALIAS"},
		"scoped":        {"alias E = {Log, #s}", "BAD EFFECT ALIAS"},
		"self":          {"alias E = {E}", "BAD EFFECT ALIAS"},
		"cycle":         {"alias E = {G}\nalias G = {E}", "BAD EFFECT ALIAS"},
		"type variable": {"effect Box a\nalias E = {Box a}", "type variable `a`"},
		"unknown":       {"alias E = {Nope}", "I don't know an effect named `Nope`"},
		"twice":         {"alias E = {Log}\nalias E = {Db}", "more than once"},
		"effect name":   {"alias Db = {Log}", "collides with an effect"},
		"type name":     {"type T = T\nalias T = {Log}", "collides with a type"},
		"as a type":     {"f : App -> Int\nf x = 0", "App"},
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
