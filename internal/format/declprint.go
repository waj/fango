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
	case *ast.ValueDecl:
		return p.valueDeclLines(d)
	case *ast.PatternDecl:
		return p.patternDeclLine(d)
	}
	return false
}

// valueDeclLines renders an annotation line and one line per equation. It
// declines any equation whose body the author wrote across lines: the layout
// constructs are not printed yet, and declining leaves the declaration to be
// copied with its own line structure intact.
func (p *printer) valueDeclLines(d *ast.ValueDecl) bool {
	type row struct{ head, body string }
	var rows []row

	for _, eq := range equations(d) {
		if eq.Body == nil || equationBroke(eq) {
			return false
		}
		body, ok := exprInline(eq.Body)
		if !ok {
			return false
		}
		head, ok := equationHead(d.Name, eq)
		if !ok {
			return false
		}
		rows = append(rows, row{head, body})
	}
	if d.Native != nil {
		if len(rows) != 0 {
			return false
		}
		p.annotationLine(d)
		p.line(0, declName(d.Name)+" = "+nativeText(d.Native))
		return true
	}
	if len(rows) == 0 {
		return false
	}

	p.annotationLine(d)
	for _, r := range rows {
		p.line(0, r.head+" = "+r.body)
	}
	return true
}

func (p *printer) patternDeclLine(d *ast.PatternDecl) bool {
	if d.Body == nil || brokeWithin(d.Body.Span()) {
		return false
	}
	pat, ok := patternInline(d.Pattern)
	if !ok {
		return false
	}
	body, ok := exprInline(d.Body)
	if !ok {
		return false
	}
	p.line(0, pat+" = "+body)
	return true
}

func (p *printer) annotationLine(d *ast.ValueDecl) {
	if d.Ann != nil {
		p.line(0, declName(d.Name)+" : "+annotationText(d.Ann))
	}
}

// declName gives an operator back the `(op)` spelling that names it.
func declName(name string) string {
	if startsWithLetter(name) {
		return name
	}
	return "(" + name + ")"
}

// equations presents the two shapes a definition can take — a single row on
// the declaration itself, or a grouped list — as one list.
func equations(d *ast.ValueDecl) []ast.Equation {
	if len(d.Equations) > 0 {
		return d.Equations
	}
	if d.Body == nil {
		return nil
	}
	return []ast.Equation{{Params: d.Params, Body: d.Body, NameSpan: d.NameSpan}}
}

// brokeWithin reports whether the author put a newline inside a node.
func brokeWithin(sp source.Span) bool {
	if sp.File == nil {
		return false
	}
	return bytes.ContainsRune(sp.File.Content[sp.Start:sp.End], '\n')
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

// equationBroke reports whether the author put a newline anywhere between the
// name that starts an equation and the end of its body — either a body moved
// below the `=`, or a break inside the body itself. Both mean the equation has
// a line structure the printer cannot yet reproduce.
func equationBroke(eq ast.Equation) bool {
	body := eq.Body.Span()
	if body.File == nil || eq.NameSpan.File == nil || eq.NameSpan.Start > body.End {
		return false
	}
	return bytes.ContainsRune(body.File.Content[eq.NameSpan.Start:body.End], '\n')
}

// equationHead renders the left of the `=`. A Unit parameter written against
// the name keeps that spelling, the same adjacency rule application obeys.
func equationHead(name string, eq ast.Equation) (string, bool) {
	head := declName(name)
	for i, param := range eq.Params {
		if _, isUnit := param.(*ast.PUnit); isUnit && paramAdjacent(eq, i) {
			head += "()"
			continue
		}
		s, ok := patternArgInline(param)
		if !ok {
			return "", false
		}
		head += " " + s
	}
	return head, true
}

// paramAdjacent reports whether a parameter was written with no space before
// it, which for `()` is what `args()` means.
func paramAdjacent(eq ast.Equation, i int) bool {
	prev := eq.NameSpan
	if i > 0 {
		prev = eq.Params[i-1].Span()
	}
	cur := eq.Params[i].Span()
	return prev.File != nil && cur.File != nil && prev.End == cur.Start
}
