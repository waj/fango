package parser

import (
	"testing"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/lexer"
	"github.com/waj/fango/internal/source"
)

func TestStringInterpolationSyntax(t *testing.T) {
	for _, input := range []string{
		`main = "hi #{name}: #{1 + 2}"`,
		`main = "#{if True then "yes" else "no"}"`,
		`main = "#{({ x = 1; x + 2 })()}"`,
		`main = "#{"nested #{3}"}"`,
		"main = $(`\"#{1}\"`)",
	} {
		f := source.NewFile("test.fango", []byte(input+"\n"))
		toks, errs := lexer.Lex(f)
		if len(errs) != 0 {
			t.Fatal(errs)
		}
		m, errs := Parse(toks, f)
		if len(errs) != 0 || !m.UsesInterpolation {
			t.Fatalf("%s: %v", input, errs)
		}
	}
	f := source.NewFile("test.fango", []byte(`"a #{1} b #{2}"`))
	toks, _ := lexer.Lex(f)
	e, errs := ParseExprInput(toks, f)
	i, ok := e.(*ast.StringInterpolation)
	if len(errs) != 0 || !ok || len(i.Exprs) != 2 || len(i.Segments) != 3 {
		t.Fatalf("%#v %v", e, errs)
	}
	for _, input := range []string{`main = "#{}"`, `f "#{x}" = x`, `f = native "#{x}"`} {
		f := source.NewFile("test.fango", []byte(input+"\n"))
		toks, _ := lexer.Lex(f)
		_, errs := Parse(toks, f)
		if len(errs) == 0 {
			t.Fatalf("accepted %s", input)
		}
	}
}
