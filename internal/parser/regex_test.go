package parser

import (
	"testing"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/lexer"
	"github.com/waj/fango/internal/source"
)

func TestRegexSyntaxDependencyAndQuotations(t *testing.T) {
	for _, input := range []string{"main = /abc/\n", "main = $(`/abc/`)\n", "main = f [/abc/, //]\n"} {
		file := source.NewFile("test.fango", []byte(input))
		tokens, errs := lexer.Lex(file)
		if len(errs) != 0 {
			t.Fatal(errs)
		}
		module, errs := Parse(tokens, file)
		if len(errs) != 0 || !module.UsesRegex {
			t.Fatalf("regex dependency lost: %v %v", module, errs)
		}
	}
	file := source.NewFile("test.fango", []byte(`/a\/b/`))
	tokens, _ := lexer.Lex(file)
	expression, errs := ParseExprInput(tokens, file)
	literal, ok := expression.(*ast.RegexLit)
	if len(errs) != 0 || !ok || literal.Value != "a/b" || literal.Raw != `/a\/b/` {
		t.Fatalf("literal = %#v, errors = %v", expression, errs)
	}
}
