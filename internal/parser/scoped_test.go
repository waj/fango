package parser

import (
	"strings"
	"testing"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/lexer"
	"github.com/waj/fango/internal/source"
)

func parseRows(t *testing.T, src string) (*ast.Module, []string) {
	t.Helper()
	f := source.NewFile("rows.fango", []byte(src))
	toks, lexErr := lexer.Lex(f)
	if len(lexErr) > 0 {
		t.Fatal(lexErr)
	}
	m, errs := Parse(toks, f)
	var bodies []string
	for _, err := range errs {
		bodies = append(bodies, err.Body)
	}
	return m, bodies
}

// annotationRows returns the rows of a declaration's annotation, outermost
// arrow first, with the rows its labels and arguments contain.
func annotationRows(te ast.TypeExpr) []*ast.EffRow {
	var rows []*ast.EffRow
	var walk func(ast.TypeExpr)
	walk = func(te ast.TypeExpr) {
		switch te := te.(type) {
		case *ast.TFunExpr:
			if te.Eff != nil {
				rows = append(rows, te.Eff)
			}
			walk(te.Arg)
			walk(te.Ret)
		case *ast.TRow:
			rows = append(rows, te.Row)
		case *ast.TApp:
			for _, arg := range te.Args {
				walk(arg)
			}
		}
	}
	walk(te)
	return rows
}

func TestEffectRowForms(t *testing.T) {
	for _, c := range []struct {
		row    string
		labels []string
		tail   string
		scoped bool
		dump   string
	}{
		{"{Foo}", []string{"Foo"}, "", false, "(effects (Foo))"},
		{"{e}", nil, "e", false, "(effects (tail e))"},
		{"{Foo, e}", []string{"Foo"}, "e", false, "(effects (Foo) (tail e))"},
		{"{#s}", nil, "s", true, "(effects (scoped s))"},
		{"{Foo, #s}", []string{"Foo"}, "s", true, "(effects (Foo) (scoped s))"},
		{"{State state, #e}", []string{"State"}, "e", true, "(effects (State state) (scoped e))"},
		{"{IO.IO, Fail String, #region}", []string{"IO.IO", "Fail"}, "region", true, "(effects (IO.IO) (Fail String) (scoped region))"},
		{"{ Foo ,\n    -- the scope\n    #s }", []string{"Foo"}, "s", true, "(effects (Foo) (scoped s))"},
	} {
		src := "run : (() ->" + c.row + " a) ->{x} a\nrun f = f ()\n"
		m, errs := parseRows(t, src)
		if len(errs) != 0 {
			t.Fatalf("%s: %v", c.row, errs)
		}
		r := annotationRows(m.Decls[0].(*ast.ValueDecl).Ann.Type)[1]
		var labels []string
		for _, l := range r.Labels {
			labels = append(labels, l.Name)
		}
		if strings.Join(labels, ",") != strings.Join(c.labels, ",") || r.Tail != c.tail || r.Scoped != c.scoped {
			t.Errorf("%s: labels %v tail %q scoped %v", c.row, labels, r.Tail, r.Scoped)
		}
		if got := ast.DumpTypeExpr(&ast.TRow{Row: r}); got != c.dump {
			t.Errorf("%s: dump %s, want %s", c.row, got, c.dump)
		}
		if c.tail != "" {
			want := c.tail
			if c.scoped {
				want = "#" + c.tail
			}
			if got := string(r.TailSp.File.Content[r.TailSp.Start:r.TailSp.End]); got != want {
				t.Errorf("%s: tail span covers %q, want %q", c.row, got, want)
			}
		}
	}
}

func TestScopedRowDeclaration(t *testing.T) {
	for _, src := range []string{
		"run : (Reader {#s} ->{Fail String, #s} a) ->{e} a\nrun action = action value\n",
		"{-# no-prelude #-}\n-- callback\nrun : (Int ->{#s} a) ->{e} a\nrun = native\n",
		"run : (Int ->{#e} a) ->{s} a\nrun f = f 1\n",
	} {
		m, errs := parseRows(t, src)
		if len(errs) != 0 {
			t.Fatalf("%q: %v", src, errs)
		}
		d := m.Decls[0].(*ast.ValueDecl)
		want := "s"
		if strings.Contains(src, "#e") {
			want = "e"
		}
		if d.ScopedRow != want || !strings.Contains(ast.Dump(m), "(scoped "+want+")") {
			t.Fatalf("lost scoped binder: %s", ast.Dump(m))
		}
	}
	// An ordinary tail is never promoted, even on a callback.
	m, errs := parseRows(t, "run : (Int ->{s} a) ->{e} a\nrun f = f 1\n")
	if len(errs) != 0 || m.Decls[0].(*ast.ValueDecl).ScopedRow != "" {
		t.Fatalf("promoted an ordinary tail: %v", errs)
	}
}

func TestEffectRowRejections(t *testing.T) {
	for _, c := range []struct{ src, want string }{
		{"run : () ->{Foo | e} a\nrun = 1\n", "separated by commas"},
		{"run : () ->{| e} a\nrun = 1\n", "separated by commas"},
		{"run : () ->{s | e} a\nrun = 1\n", "separated by commas"},
		{"run : () ->{e, Foo} a\nrun = 1\n", "`e` ends the row"},
		{"run : () ->{e, f} a\nrun = 1\n", "`e` ends the row"},
		{"run : () ->{e, #s} a\nrun = 1\n", "`e` ends the row"},
		{"run : () ->{#s, Foo} a\nrun = 1\n", "`#s` ends the row"},
		{"run : () ->{#s, e} a\nrun = 1\n", "already includes the runner's effects"},
		{"run : () ->{#s, #t} a\nrun = 1\n", "`#s` ends the row"},
		{"run : () ->{Foo,} a\nrun = 1\n", "I expect a capitalized effect name"},
		{"run : () ->{Foo #s} a\nrun = 1\n", "I expect `,` or `}`"},
		{"run : () ->{e f} a\nrun = 1\n", "I expect `}`"},
		{"run : () ->{#} a\nrun = 1\n", "`#` immediately followed by a lowercase name"},
		{"run : () ->{#S} a\nrun = 1\n", "`#` immediately followed by a lowercase name"},
		{"run : () ->{# s} a\nrun = 1\n", "`#` immediately followed by a lowercase name"},
		{"run : () ->{##s} a\nrun = 1\n", "`#` immediately followed by a lowercase name"},
		{"run : (Int ->{#s} a) -> (Int ->{#t} a) ->{e} a\nrun f g = f 1\n", "both `#s` and `#t`"},
		{"run : (Reader s ->{#s} a) ->{e} a\nrun f = f 1\n", "as `{#s}` here"},
		{"run : (Int ->{Foo, s} a) -> (Int ->{#s} a) ->{e} a\nrun f g = f 1\n", "as `{Foo, #s}` here"},
		{"run : (Reader {#e} ->{#e} a) ->{e} a\nrun f = f 1\n", "give an ordinary variable a distinct name"},
		{"run : (Int ->{#s} s) ->{e} s\nrun f = f 1\n", "written at 1:15"},
		{"{-# scoped s #-}\nrun : (Int ->{s} a) ->{e} a\nrun f = f 1\n", "I don't know the pragma `scoped s`"},
		{"x = 1\n{-# scoped s #-}\nrun : (Int ->{s} a) ->{e} a\nrun f = f 1\n", "Only the `resource` pragma may precede a declaration"},
	} {
		_, errs := parseRows(t, c.src)
		if len(errs) == 0 {
			t.Errorf("accepted %q", c.src)
			continue
		}
		if !strings.Contains(strings.Join(errs, "\n"), c.want) {
			t.Errorf("%q: got %v, want %q", c.src, errs, c.want)
		}
	}
}
