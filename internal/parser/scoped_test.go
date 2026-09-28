package parser

import (
	"strings"
	"testing"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/lexer"
	"github.com/waj/fango/internal/source"
)

func TestScopedPragma(t *testing.T) {
	for _, src := range []string{
		"{-# scoped\ts #-}\nrun : (Int ->{s} a) ->{e} a\nrun use = use 0\n",
		"{-# no-prelude #-}\n{-# scoped s #-}\n-- callback\nrun : (Int ->{s} a) ->{e} a\nrun = native\n",
	} {
		f := source.NewFile("scoped.fango", []byte(src))
		toks, lexErr := lexer.Lex(f)
		if len(lexErr) > 0 {
			t.Fatal(lexErr)
		}
		m, errs := Parse(toks, f)
		if len(errs) > 0 {
			t.Fatal(errs)
		}
		d := m.Decls[0].(*ast.ValueDecl)
		if d.ScopedRow != "s" || !strings.Contains(ast.Dump(m), "(pragma scoped s)") {
			t.Fatalf("lost scoped binder: %#v", d)
		}
	}
	for _, src := range []string{
		"{-# scoped s #-}\nrun x = x\n",
		"{-# scoped #-}\nrun : Int -> Int\nrun x = x\n",
		"{-# scoped s extra #-}\nrun : Int -> Int\nrun x = x\n",
		"{-# scoped s #-}\n{-# scoped s #-}\nrun : Int -> Int\nrun x = x\n",
		"{-# scoped s #-}\ntype Handle = Handle Int\n",
	} {
		f := source.NewFile("bad.fango", []byte(src))
		toks, _ := lexer.Lex(f)
		if _, errs := Parse(toks, f); len(errs) == 0 {
			t.Fatalf("accepted %q", src)
		}
	}
}
