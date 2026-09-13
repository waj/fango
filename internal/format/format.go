// Package format implements fango's source formatter.
//
// Formatting is a pure function of one file's bytes. It needs no module graph,
// no fixity resolution and no types, so it works on a file that does not
// typecheck and on a file with no project around it. In particular it runs
// before internal/fixity, which would need the whole module graph to supply
// imported fixities; operator runs reach the printer flat, in the order they
// were written, and are printed that way rather than reassociated.
//
// The formatter preserves the author's line breaks. It normalizes indentation
// and spacing and decides nothing about where a construct should be split, so
// a long line stays long. What is not yet printed structurally is copied
// verbatim from the source, which is why no comment can be moved or lost
// before the printer learns to place it.
package format

import (
	"bytes"
	"fmt"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/lexer"
	"github.com/waj/fango/internal/parser"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/token"
)

// Indent is the width of one indentation level.
const Indent = 4

// Source formats an entire file.
//
// It returns nil bytes and the diagnostics when the file does not lex or
// parse: recovery drops a failed declaration and there is no error node to
// stand in for it, so formatting a file that does not parse would silently
// delete code. Callers must leave the input untouched in that case.
//
// Source is idempotent, and the bytes it returns re-parse to the same syntax
// tree as the bytes it was given — it verifies that itself before returning,
// because indentation carries meaning in this language and a printing bug
// could otherwise change a program rather than merely misformat it.
func Source(f *source.File) ([]byte, []diag.Error) {
	toks, comments, errs := lexer.LexWithComments(f)
	if len(errs) > 0 {
		return nil, errs
	}
	m, errs := parser.Parse(toks, f)
	if len(errs) > 0 {
		return nil, errs
	}

	out := printModule(f, m, toks, comments)

	if err := verify(f, out, comments); err != nil {
		return nil, []diag.Error{{
			Title: "INTERNAL FORMATTER ERROR",
			Span:  source.Span{File: f, Start: 0, End: 0},
			Body: "I formatted this file into something that does not mean what the\n" +
				"original meant, so I left it alone. This is a bug in the formatter:\n" +
				err.Error(),
		}}
	}
	return out, nil
}

// verify re-reads the formatter's own output and checks that it says the same
// thing as the input. The AST dump is span-free and frozen as a golden-test
// interface, which makes it the natural oracle for "the same program"; the
// comment check is separate because comments never reach the AST.
func verify(orig *source.File, out []byte, want []token.Comment) error {
	rf := source.NewFile(orig.Name, out)
	toks, gotComments, errs := lexer.LexWithComments(rf)
	if len(errs) > 0 {
		return fmt.Errorf("the formatted output does not lex: %s", errs[0].Title)
	}
	m, errs := parser.Parse(toks, rf)
	if len(errs) > 0 {
		return fmt.Errorf("the formatted output does not parse: %s", errs[0].Title)
	}

	origToks, _, _ := lexer.LexWithComments(orig)
	origModule, _ := parser.Parse(origToks, orig)
	if a, b := ast.Dump(origModule), ast.Dump(m); a != b {
		return fmt.Errorf("the syntax tree changed")
	}
	if a, b := commentTexts(want), commentTexts(gotComments); !bytes.Equal(a, b) {
		return fmt.Errorf("comments changed")
	}
	return nil
}

func commentTexts(cs []token.Comment) []byte {
	var b bytes.Buffer
	for _, c := range cs {
		b.WriteString(normalizeSpace(c.Text))
		b.WriteByte('\n')
	}
	return b.Bytes()
}
