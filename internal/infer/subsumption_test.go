package infer_test

import "testing"

func TestSubsumptionRejectsEffectLoss(t *testing.T) {
	for name, src := range map[string]string{
		"callback narrowing": `emit() = print "x"
consume : (() -> ()) -> ()
consume action = action()
bad = consume emit`,
		"contravariant argument": `emit() =
    print "x"
    1
consumePure : (() -> Int) -> Int
consumePure action = action()
consume : ((() ->{IO} Int) ->{IO} Int) ->{IO} Int
consume consumer = consumer emit
bad() = consume consumePure`,
		"invariant row": `type Cell e = Cell (() ->{e} Int) ((() ->{e} Int) -> Int)
pure() = 1
ignore action = 0
cell = Cell pure ignore
consume : Cell IO -> Int
consume value = 1
bad = consume cell`,
		"rigid tail": `consume : (() ->{e} ()) ->{e} ()
consume action =
    action()
    print "hidden"`,
		"parameterized label": `effect Read a
    read : () -> a
consume : (() ->{Read Int} Int) ->{Read Int} Int
consume action = action()
text : () ->{Read String} String
text() = read()
bad() = consume text`,
		"exact annotation": `pure : () ->{IO} Int
pure() = 1`,
		"exact local annotation": `outer() =
    inner : () ->{IO} Int
    inner() = 1
    inner()`,
		"stored callback narrowing": `type Box a = Box a
emit() = print "x"
consume : Box (() -> ()) -> ()
consume box = case box of
    Box action -> action()
bad = consume (Box emit)`,
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
		"consume() = pair pure emit", "consume() = pair emit pure",
		"consume() = pair emit fail", "consume() = pair fail emit",
		"consume() = pair { _ -> 1 } emit", "consume() = pair emit { _ -> 1 }",
		"consume = branch [quiet, loud]", "consume = branch [loud, quiet]",
	} {
		t.Run(body, func(t *testing.T) {
			_, _, errs := check(t, prefix+"\n"+body)
			if len(errs) != 0 {
				t.Fatalf("%v", errs)
			}
		})
	}
}

func TestGenericCursorCallbackCanExtendSharedRow(t *testing.T) {
	const prefix = `type Source a e = Source (() ->{e} a)
read : Source a e ->{e} a
read source = case source of
    Source action -> action()
withSource : Source a e -> (Source a e ->{e} b) ->{e} b
withSource source consumer = consumer source
readAndPrint : Source a e ->{IO | e} a
readAndPrint source =
    print "read"
    read source
`
	for _, named := range []bool{false, true} {
		for _, allowIO := range []bool{false, true} {
			row := "e"
			if allowIO {
				row = "IO | e"
			}
			callback := "readAndPrint"
			if !named {
				callback = "{ cursor -> readAndPrint cursor }"
			}
			src := prefix + "\nprinted : Source a e ->{" + row + "} a\nprinted source = withSource source " + callback
			_, _, errs := check(t, src)
			if allowIO && len(errs) != 0 || !allowIO && len(errs) == 0 {
				t.Fatalf("named=%v allowIO=%v errors=%v", named, allowIO, errs)
			}
		}
	}
}
