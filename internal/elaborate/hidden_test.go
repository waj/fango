package elaborate_test

import (
	"strings"
	"testing"
)

func TestHiddenConstructorCore(t *testing.T) {
	for _, src := range []string{
		"type Box a = Box state (state -> a)\nread (Box value observe) = observe value\nx = read (Box () { _ -> True })",
		"type Box a = Box state (state -> a)\nrepack (Box value observe) = Box value observe",
		"type Box a = Box state (state -> a)\nchoose flag = if flag then Box () { _ -> True } else Box False { b -> b }",
		"type Box a = { initial : state, read : state -> a }\nreadBox (Box { initial = value, read = observe }) = observe value\nx = readBox (Box { initial = (), read = { _ -> True } })",
		"type Box a = { label : String, initial : state, read : state -> a }\nlabel : Box a -> String\nlabel box = box.label",
		"type Box a = { label : String, initial : state, read : state -> a }\nrename : Box a -> Box a\nrename box = { box | label = \"new\" }",
		"type Box a = { initial : state, read : state -> a }\nreadBox : Box a -> a\nreadBox box =\n    { initial = value, read = observe } = box\n    observe value",
	} {
		t.Run(src, func(t *testing.T) { elabPoly(t, src) })
	}
}

func TestKnownHiddenPackageIsEliminated(t *testing.T) {
	dump := elabInlined(t, `type Box a = Box state (state -> a)
result = case Box 41 { n -> n + 1 } of
    Box value observe -> observe value
main() = print result
`)
	body := defDump(t, dump, "main")
	if strings.Contains(body, "pack@") || strings.Contains(body, "open@") {
		t.Fatalf("known hidden package survived:\n%s", body)
	}
}

func TestHiddenFactoriesAndChoicesAreEliminated(t *testing.T) {
	dump := elabInlined(t, `type Box a = Box state (state -> a)
make n = Box n { value -> value + 1 }
choose flag n = if flag then make n else Box (n, 1) { (value, increment) -> value + increment }
read (Box value observe) = observe value
run flag n = read (choose flag n)
main() = print (run True 41)
`)
	body := defDump(t, dump, "run")
	for _, marker := range []string{"pack@", "open@", "var choose", "var make", "var read"} {
		if strings.Contains(body, marker) {
			t.Fatalf("hidden constructor survived (%s):\n%s", marker, body)
		}
	}
	if !strings.Contains(body, "(if ") {
		t.Fatalf("choice was removed:\n%s", body)
	}
}

func TestHiddenConsumerForwardsInvocationRow(t *testing.T) {
	dump := elabInlined(t, `type Progress a = Done | More a
type Source a e = { initial : state, step : state ->{e} Progress (a, state) }
unfold : state -> (state ->{e} Progress (a, state)) -> Source a e
unfold initial step = { initial = initial, step = step }
foldStep : (state ->{e} Progress (Int, state)) -> state -> Int ->{e} Int
foldStep step state total = case step state of
    Done -> total
    More (value, next) -> foldStep step next (total + value)
fold : Source Int e ->{e} Int
fold (Source { initial = state, step = step }) = foldStep step state 0
run : Int -> (Int ->{e} Progress (Int, Int)) ->{e} Int
run n step = fold (unfold n step)
choose flag = if flag then unfold 0 { _ -> Done } else unfold () { _ -> Done }
pureChoice flag = fold (choose flag)
main() = print (run 0 { _ -> Done })
`)
	for _, name := range []string{"run", "pureChoice"} {
		body := defDump(t, dump, name)
		for _, marker := range []string{"pack@", "open@", "var fold)", "var unfold"} {
			if strings.Contains(body, marker) {
				t.Fatalf("hidden row-forwarding worker survived (%s):\n%s", marker, body)
			}
		}
	}
}

func TestHiddenRepresentationWorkers(t *testing.T) {
	dump := elabInlined(t, `type Source a e = { initial : state, step : state ->{e} a }
make : state -> (state ->{e} a) -> Source a e
make initial step = { initial = initial, step = step }
read : Source a e ->{e} a
read (Source { initial = state, step = step }) = step state
main() = print (read (make 42 { n -> n }))
`)
	for _, name := range []string{"_storage_make", "_storage_read"} {
		body := defDump(t, dump, name)
		if strings.Contains(body, "open@") || strings.Contains(body, "pack@") {
			t.Fatalf("typed worker retained existential representation:\n%s", body)
		}
	}
}

func TestHiddenStorageDoesNotDuplicateGlobalValues(t *testing.T) {
	dump := elabInlined(t, `type Box a = Box state (state -> a)
stored : Box Int
stored = Box 7 { value -> value }
result = case stored of
    Box value observe -> observe value
main() = print result
`)
	if strings.Contains(dump, "(def _storage_stored ") {
		t.Fatal("duplicated global initializer")
	}
}
