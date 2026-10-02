package elaborate_test

import (
	"strings"
	"testing"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/elaborate"
)

func elabInlined(t *testing.T, src string) string {
	t.Helper()
	elaborate.Inlining = true
	defer func() { elaborate.Inlining = false }()
	return core.Dump(elabPoly(t, src))
}

func defDump(t *testing.T, dump, name string) string {
	t.Helper()
	for _, line := range strings.Split(dump, "\n") {
		if strings.Contains(line, "(def "+name+" ") {
			return line
		}
	}
	t.Fatalf("no definition of %s:\n%s", name, dump)
	return ""
}

// A small pure worker's body replaces its call; the copy's binders are
// fresh, and a match on the constructor it builds resolves in place.
func TestInlineSmallPureWorker(t *testing.T) {
	dump := elabInlined(t, `type Point = { x : Int, y : Int }

shift : Point -> Point
shift p = { p | x = p.x + 1 }

total : Point -> Int
total p = (shift p).x + p.y

main = print (total (Point { x = 1, y = 2 }))
`)
	body := defDump(t, dump, "total")
	if strings.Contains(body, "var shift") {
		t.Errorf("shift was not inlined:\n%s", body)
	}
	if !strings.Contains(body, "_inl") {
		t.Errorf("inlined binders were not renamed:\n%s", body)
	}
	if strings.Count(body, "(app/ctor") != 0 {
		t.Errorf("the rebuilt record was not resolved:\n%s", body)
	}
}

// Recursive and effectful workers keep their calls.
func TestInlineKeepsRecursiveAndEffectfulCalls(t *testing.T) {
	dump := elabInlined(t, `count : Int -> Int
count n = if n == 0 then 0 else count (n - 1)

shout : String ->{IO} ()
shout text = print text

main =
    shout "hi"
    print (count 3)
`)
	main := defDump(t, dump, "main")
	for _, want := range []string{"var count", "var shout"} {
		if !strings.Contains(main, want) {
			t.Errorf("main lost its %s call:\n%s", want, main)
		}
	}
}

// A worker taking a function is not a candidate: its copy would carry
// capture obligations.
func TestInlineSkipsHigherOrderWorkers(t *testing.T) {
	dump := elabInlined(t, `apply : (Int -> Int) -> Int -> Int
apply f x = f x

main = print (apply { n -> n + 1 } 2)
`)
	if !strings.Contains(defDump(t, dump, "main"), "var apply") {
		t.Errorf("apply was inlined:\n%s", dump)
	}
}
