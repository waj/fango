package parser

import (
	"strings"
	"testing"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/lexer"
	"github.com/waj/fango/internal/source"
)

func TestBacktickExpressions(t *testing.T) {
	for _, input := range []string{
		"`6 * 7`",
		"`f x + 1`",
		"`(1, 2)`",
		"`[1, 2]`",
		"`{ field = 1 }`",
		"`{ record | field = 1 }`",
		"`{ x -> x + 1 }`",
		"`{ x = 6; x * 7 }()`",
		"`$(`6`) * $(c)`",
		"`if True then\n    1\nelse\n    2\n`",
		"`case True of\n    True -> 1\n    False -> 2\n`",
		"`case True of\n    True ->\n        value = 1\n        value\n    False -> 2\n`",
		"`\n-- ` is a comment\n\"`\"\n`",
		"`'`'`",
	} {
		t.Run(input, func(t *testing.T) {
			f := source.NewFile("<test>", []byte(input))
			toks, errs := lexer.Lex(f)
			if len(errs) != 0 {
				t.Fatalf("lex: %v", errs)
			}
			e, errs := ParseExprInput(toks, f)
			if len(errs) != 0 {
				t.Fatalf("parse: %v", errs)
			}
			q, ok := e.(*ast.Quote)
			if !ok || q.Sp.Start != 0 || q.Sp.End != len(input) {
				t.Fatalf("quotation must retain both delimiters: %#v", e)
			}
		})
	}
}

func TestQuoteAtomsAndFollowingDeclarations(t *testing.T) {
	input := "quote x = x\ncode = build `1` `2`\nmulti = `case True of\n    True -> 1\n    False -> 2\n`\nfollowing = 3\n"
	f := source.NewFile("<test>", []byte(input))
	toks, errs := lexer.Lex(f)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	m, errs := Parse(toks, f)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	if len(m.Decls) != 4 || !m.UsesStaging {
		t.Fatalf("quotation swallowed a declaration or missed staging: %s", ast.Dump(m))
	}
	if !strings.Contains(ast.Dump(m), "(app (app (var build) (quote (int 1))) (quote (int 2)))") {
		t.Fatalf("quotations must be independent application atoms: %s", ast.Dump(m))
	}
}

func TestInvalidBacktickExpressions(t *testing.T) {
	for _, tc := range []struct{ input, title, message string }{
		{"``", "SYNTAX PROBLEM", "needs an expression"},
		{"`1", TitleUnexpectedEOF, "backtick that closes"},
		{"`", TitleUnexpectedEOF, ""},
		{"`x = 1; x`", "SYNTAX PROBLEM", "not a binding block"},
		{"`\nx = 1\nx\n`", "SYNTAX PROBLEM", "not a binding block"},
		{"`1; 2`", "SYNTAX PROBLEM", "not a binding block"},
		{"`1, 2`", "SYNTAX PROBLEM", "backtick that closes"},
		{"`1 +`", "SYNTAX PROBLEM", ""},
	} {
		t.Run(tc.input, func(t *testing.T) {
			f := source.NewFile("<test>", []byte(tc.input))
			toks, errs := lexer.Lex(f)
			if len(errs) != 0 {
				t.Fatal(errs)
			}
			_, errs = ParseExprInput(toks, f)
			if len(errs) == 0 || errs[0].Title != tc.title || !strings.Contains(errs[0].Body, tc.message) {
				t.Fatalf("errors = %v, want %s containing %q", errs, tc.title, tc.message)
			}
		})
	}
}
