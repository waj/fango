package infer_test

import "testing"

func TestSubsumptionRejectsEffectLoss(t *testing.T) {
	for name, src := range map[string]string{
		"callback narrowing": `emit() = print "x"
use : (() -> ()) -> ()
use action = action()
bad = use emit`,
		"contravariant argument": `emit() =
    print "x"
    1
consumePure : (() -> Int) -> Int
consumePure action = action()
use : ((() ->{IO} Int) ->{IO} Int) ->{IO} Int
use consumer = consumer emit
bad() = use consumePure`,
		"invariant row": `type Cell e = Cell (() ->{e} Int) ((() ->{e} Int) -> Int)
pure() = 1
ignore action = 0
cell = Cell pure ignore
use : Cell IO -> Int
use value = 1
bad = use cell`,
		"rigid tail": `use : (() ->{e} ()) ->{e} ()
use action =
    action()
    print "hidden"`,
		"parameterized label": `effect Read a
    read : () -> a
use : (() ->{Read Int} Int) ->{Read Int} Int
use action = action()
text : () ->{Read String} String
text() = read()
bad() = use text`,
		"exact annotation": `pure : () ->{IO} Int
pure() = 1`,
		"exact local annotation": `outer() =
    inner : () ->{IO} Int
    inner() = 1
    inner()`,
		"stored callback narrowing": `type Box a = Box a
emit() = print "x"
use : Box (() -> ()) -> ()
use box = case box of
    Box action -> action()
bad = use (Box emit)`,
	} {
		t.Run(name, func(t *testing.T) {
			_, _, errs := check(t, src)
			if len(errs) == 0 {
				t.Fatal("accepted effect loss")
			}
		})
	}
}

func TestCompositionalCallbacks(t *testing.T) {
	const prefix = `effect Boom
    abort boom : () -> Int

pure() = 1
emit() =
    print "emit"
    2
fail() = boom()

pair : (() ->{e} Int) -> (() ->{e} Int) ->{e} Int
pair left right = left() + right()

type Tree e = Leaf (() ->{e} Int) | Branch (List (Tree e))
leaf : (() ->{e} Int) -> Tree e
leaf action = Leaf action
branch : List (Tree e) -> Tree e
branch children = Branch children

quiet = leaf pure
loud = leaf emit
`
	for _, body := range []string{
		"use() = pair pure emit", "use() = pair emit pure",
		"use() = pair emit fail", "use() = pair fail emit",
		"use() = pair (\\_ -> 1) emit", "use() = pair emit (\\_ -> 1)",
		"use = branch [quiet, loud]", "use = branch [loud, quiet]",
	} {
		t.Run(body, func(t *testing.T) {
			_, _, errs := check(t, prefix+"\n"+body)
			if len(errs) != 0 {
				t.Fatalf("%v", errs)
			}
		})
	}
}
