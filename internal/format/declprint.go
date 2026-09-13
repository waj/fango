package format

import (
	"bytes"
	"strconv"
	"strings"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/source"
)

// printDecl renders one declaration, reporting whether it did. A declaration
// it does not yet know how to print is copied verbatim by the caller, and so
// is one holding a comment: a comment inside a declaration has no anchor until
// the printer learns to place it, and copying is what keeps it from being
// moved or lost in the meantime.
func (p *printer) printDecl(d ast.Decl, sp source.Span, hasComment bool) bool {
	if hasComment {
		return false
	}
	switch d := d.(type) {
	case *ast.FixityDecl:
		p.line(0, fixityText(d))
		return true
	case *ast.TypeDecl:
		p.typeDeclLines(d, sp)
		return true
	case *ast.EffectDecl:
		p.sigBlock("effect "+d.Name, paramNames(d.Params), d.Ops)
		return true
	case *ast.ClassDecl:
		p.sigBlock("class "+d.Name, []string{d.Param.Name}, d.Methods)
		return true
	}
	return false
}

func fixityText(d *ast.FixityDecl) string {
	kw := "infix"
	switch d.Assoc {
	case ast.AssocLeft:
		kw = "infixl"
	case ast.AssocRight:
		kw = "infixr"
	}
	return kw + " " + strconv.Itoa(d.Prec) + " (" + d.Op + ")"
}

// typeDeclLines renders a nominal type. The alternatives go one per line, with
// a leading `=` and `|`, exactly when the author wrote them that way.
func (p *printer) typeDeclLines(d *ast.TypeDecl, sp source.Span) {
	head := "type " + d.Name
	for _, param := range d.Params {
		head += " " + param.Name
	}
	tail := derivingText(d.Deriving)

	if d.RecordFields != nil {
		fields := make([]string, len(d.RecordFields))
		for i, f := range d.RecordFields {
			fields[i] = f.Name + " : " + typeText(f.Type)
		}
		if !brokeAfter(sp, d.NameSpan.End) {
			p.line(0, head+" = { "+strings.Join(fields, ", ")+" }"+tail)
			return
		}
		p.line(0, head+" =")
		for i, f := range fields {
			lead := ", "
			if i == 0 {
				lead = "{ "
			}
			p.line(Indent, lead+f)
		}
		p.line(Indent, "}"+tail)
		return
	}

	alts := make([]string, len(d.Ctors))
	for i, c := range d.Ctors {
		parts := []string{c.Name}
		for _, a := range c.Args {
			parts = append(parts, typeArgText(a))
		}
		alts[i] = strings.Join(parts, " ")
	}
	if !brokeAfter(sp, d.NameSpan.End) {
		p.line(0, head+" = "+strings.Join(alts, " | ")+tail)
		return
	}
	p.line(0, head)
	for i, alt := range alts {
		lead := "| "
		if i == 0 {
			lead = "= "
		}
		if i == len(alts)-1 {
			alt += tail
		}
		p.line(Indent, lead+alt)
	}
}

func derivingText(names []ast.TName) string {
	if len(names) == 0 {
		return ""
	}
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = n.Name
	}
	return " deriving (" + strings.Join(out, ", ") + ")"
}

// sigBlock renders a `class` or `effect` header and its indented signature
// lines, which is the only shape either declaration has.
func (p *printer) sigBlock(head string, params []string, sigs []ast.OpSig) {
	for _, param := range params {
		if param != "" {
			head += " " + param
		}
	}
	p.line(0, head)
	for _, s := range sigs {
		p.line(Indent, opSigText(s))
	}
}

func opSigText(s ast.OpSig) string {
	name := s.Name
	if !startsWithLetter(name) {
		name = "(" + name + ")"
	}
	if s.Abort {
		name = "abort " + name
	}
	line := name + " : " + typeText(s.Type)
	if s.Native != nil {
		line += " = " + nativeText(s.Native)
	}
	return line
}

// nativeText copies the native body from source, so a template string keeps
// the escape spelling its author chose.
func nativeText(n *ast.NativeBody) string {
	if n.Sp.File == nil {
		return "native"
	}
	return normalizeSpace(string(n.Sp.File.Content[n.Sp.Start:n.Sp.End]))
}

func paramNames(params []ast.Param) []string {
	out := make([]string, len(params))
	for i, param := range params {
		out[i] = param.Name
	}
	return out
}

// brokeAfter reports whether the author put a newline in the declaration after
// offset — the test for "this construct was written across lines".
func brokeAfter(sp source.Span, offset int) bool {
	if sp.File == nil || offset < sp.Start || offset > sp.End {
		return false
	}
	return bytes.ContainsRune(sp.File.Content[offset:sp.End], '\n')
}
