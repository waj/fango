package parser

import (
	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/lexer"
	"github.com/waj/fango/internal/source"
	"strings"
	"testing"
)

func TestResourcePragma(t *testing.T) {
	for _, src := range []string{
		"{-# resource #-}\ntype Handle = Handle Int\n",
		"{-# no-prelude #-}\n{-# resource #-}\n-- private representation\ntype Handle a = Handle a\n",
		"module M exposing (Handle)\n{-# resource #-}\ntype Handle = { id : Int }\n",
	} {
		f := source.NewFile("resource.fango", []byte(src))
		toks, lexErr := lexer.Lex(f)
		if len(lexErr) > 0 {
			t.Fatal(lexErr)
		}
		m, errs := Parse(toks, f)
		if len(errs) > 0 {
			t.Fatal(errs)
		}
		td := m.Decls[0].(*ast.TypeDecl)
		if !td.Resource || td.Sp.Start != td.ResourceSpan.Start || !strings.Contains(ast.Dump(m), "(pragma resource)") {
			t.Fatalf("resource metadata lost: %#v", td)
		}
	}
	for _, src := range []string{
		"{-# resource #-}\nvalue = 1\n",
		"{-# resource #-}\n{-# resource #-}\ntype H = H Int\n",
		"{-# resource H #-}\ntype H = H Int\n",
		"{-# resource #-}\nmodule M exposing (H)\ntype H = H Int\n",
	} {
		f := source.NewFile("bad.fango", []byte(src))
		toks, _ := lexer.Lex(f)
		if _, errs := Parse(toks, f); len(errs) == 0 {
			t.Fatalf("accepted %q", src)
		}
	}
	f := source.NewFile("<repl>", []byte("{-# resource #-}\n"))
	toks, _ := lexer.Lex(f)
	if _, errs := Parse(toks, f); len(errs) != 1 || errs[0].Title != TitleUnexpectedEOF {
		t.Fatalf("missing REPL continuation: %v", errs)
	}
}
