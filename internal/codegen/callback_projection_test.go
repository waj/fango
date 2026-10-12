package codegen

import (
	"bytes"
	"go/format"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func splitCallbacks(t *testing.T, body string) string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "bundle.go", "package p\n"+body, 0)
	if err != nil {
		t.Fatal(err)
	}
	g := &gen{callableNames: map[string]string{"callback": "Fn"}}
	g.splitCallbackLocals(file)
	var out bytes.Buffer
	if err := format.Node(&out, token.NewFileSet(), file); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestCallbackProjectionPreservesClosureCreation(t *testing.T) {
	got := splitCallbacks(t, `func f(value int) int {
 var cb Fn = Fn{Direct: func(x int) int { return value+x }, Exit: func(x int) int { return x }}
 value = 7
 return fold(cb.Direct) + cb.Direct(1)
}`)
	if strings.Contains(got, "var cb") || strings.Contains(got, "cb.Direct") {
		t.Fatalf("bundle survived:\n%s", got)
	}
	if strings.Count(got, "var t_callablePart0") != 1 || strings.Count(got, "return value + x") != 1 {
		t.Fatalf("closure was duplicated:\n%s", got)
	}
	if strings.Index(got, "func(x int)") > strings.Index(got, "value = 7") {
		t.Fatalf("closure creation moved:\n%s", got)
	}
}

func TestCallbackProjectionKeepsObservableBundles(t *testing.T) {
	for _, body := range []string{
		`func f() Fn { var cb Fn = Fn{Direct: func(x int) int { return x }}; return cb }`,
		`func f() int { var cb Fn = Fn{Direct: func(x int) int { return x }}; cb.Direct = other; return cb.Direct(1) }`,
		`func f() int { var cb Fn = Fn{Direct: func(x int) int { return x }}; mutate(&cb); return cb.Direct(1) }`,
		`func f() int { var cb Fn = Fn{Direct: makeCallback()}; return cb.Direct(1) }`,
		`func f() int { var cb Fn = Fn{Direct: func(x int) int { return x }, Exit: nil}; return cb.Direct(1) }`,
		`func f() int { var cb Fn = Fn{Direct: func(x int) int { return x }}; return func(cb Fn) int { return cb.Direct(1) }(other) + cb.Direct(2) }`,
	} {
		t.Run(body, func(t *testing.T) {
			got := splitCallbacks(t, body)
			if !strings.Contains(got, "var cb Fn") {
				t.Fatalf("observable bundle removed:\n%s", got)
			}
		})
	}
}
