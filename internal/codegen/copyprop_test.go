package codegen

import (
	"bytes"
	"go/format"
	goparser "go/parser"
	gotoken "go/token"
	"strings"
	"testing"
)

func elided(t *testing.T, src string) string {
	t.Helper()
	file, err := goparser.ParseFile(gotoken.NewFileSet(), "gen.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	(&gen{}).elideCopies(file)
	var out bytes.Buffer
	if err := format.Node(&out, gotoken.NewFileSet(), file); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

// A renaming temporary of the same declared type collapses onto its source,
// through chains, and the source's earlier assignment does not matter.
func TestElideCopiesCollapsesRenamingChains(t *testing.T) {
	got := elided(t, `package p

type S struct{ A, B int }

func f(v S) int {
	var r S
	r = v
	var a S = r
	var b S = a
	var c int = b.A
	return c
}
`)
	for _, unwanted := range []string{"var a S", "var b S", "b.A"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("copy %q survived:\n%s", unwanted, got)
		}
	}
	if !strings.Contains(got, "var c int = r.A") {
		t.Errorf("projection does not read the source:\n%s", got)
	}
}

// A copy must stay when the source changes type, is reassigned later other
// than by a tail jump, is addressed, is shadowed in the copy's scope, or is
// captured by a closure inside a loop that reassigns the source.
func TestElideCopiesKeepsCopiesWithDifferentMeaning(t *testing.T) {
	src := `package p

type S struct{ A int }
type T = S

func different(v S) int {
	var a T = v
	return a.A
}

func reassigned(v S) int {
	var a S = v
	v = S{}
	return a.A
}

func addressed(v S) *S {
	var a S = v
	return &a
}

func shadowed(v S) int {
	var a S = v
	{
		var v S
		_ = v
		return a.A
	}
}

func captured(v S) func() int {
	for {
		var a S = v
		f := func() int { return a.A }
		v = S{A: 1}
		continue
		return f
	}
}

func looped(v S) int {
	for {
		var a S = v
		if a.A > 3 {
			return a.A
		}
		v = S{A: a.A + 1}
		continue
	}
}
`
	got := elided(t, src)
	for _, kept := range []string{"var a T = v", "var a S = v\n\tv = S{}", "var a S = v\n\treturn &a", "var a S = v\n\t{\n\t\tvar v S", "var a S = v\n\t\tf := func"} {
		if !strings.Contains(got, kept) {
			t.Errorf("a copy with its own meaning was elided; wanted %q:\n%s", kept, got)
		}
	}
	if !strings.Contains(got, "if v.A > 3") || !strings.Contains(got, "v = S{A: v.A + 1}") {
		t.Errorf("a tail-loop copy survived:\n%s", got)
	}
}
