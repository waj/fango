package format

import (
	"bytes"
	"strconv"
	"strings"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/source"
)

// printDecl renders one declaration, reporting whether it did. A declaration
// it does not know how to print is copied verbatim by the caller, and so is one
// whose comments it could not all place.
func (p *printer) printDecl(d ast.Decl, sp source.Span) bool {
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
		return p.valueDeclLines(d, 0)
	case *ast.PatternDecl:
		return p.patternDeclLine(d)
	case *ast.InstanceDecl:
		return p.instanceLines(d)
	case *ast.DeriverDecl:
		return p.deriverLines(d)
	}
	return false
}

// valueDeclLines renders an annotation line and one line per equation. It
// declines any equation whose body the author wrote across lines: the layout
// constructs are not printed yet, and declining leaves the declaration to be
// copied with its own line structure intact.
func (p *printer) valueDeclLines(d *ast.ValueDecl, ind int) bool {
	if d.ScopedRow != "" {
		p.line(ind, "{-# scoped "+d.ScopedRow+" #-}")
	}
	if d.Native != nil {
		p.annotationLine(d, ind)
		p.line(ind, declName(d.Name)+" = "+nativeText(d.Native))
		return true
	}
	rows := equations(d)
	if len(rows) == 0 {
		return false
	}
	p.annotationLine(d, ind)
	for _, eq := range rows {
		p.start(ind)
		if !p.renderEquationHead(d.Name, eq, ind) {
			return false
		}
		if !p.renderAssigned(eq.Body, ind) {
			return false
		}
	}
	p.flush()
	return true
}

func (p *printer) patternDeclLine(d *ast.PatternDecl) bool {
	p.start(0)
	if !p.renderPattern(d.Pattern, 0) {
		return false
	}
	if !p.renderAssigned(d.Body, 0) {
		return false
	}
	p.flush()
	return true
}

func (p *printer) annotationLine(d *ast.ValueDecl, ind int) {
	if d.Ann != nil {
		p.line(ind, declName(d.Name)+" : "+annotationText(d.Ann))
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
// a leading `=` and `|`, exactly when the author wrote them that way. A broken
// right-hand side always gives its deriving clause a line of its own.
func (p *printer) typeDeclLines(d *ast.TypeDecl, sp source.Span) {
	for _, group := range d.Attributes {
		p.attributeGroupLine(group, 0, "")
	}

	if d.Resource {
		p.line(0, "{-# resource #-}")
	}
	head := "type " + d.Name
	for _, param := range d.Params {
		head += " " + param.Name
	}
	tail := derivingText(d.Deriving)
	bodyEnd, derivingStart := typeRHSBounds(d, sp)
	broken := brokeBetween(sp.File, d.NameSpan.End, bodyEnd)
	separateDeriving := len(d.Deriving) > 0 && !broken && brokeBetween(sp.File, bodyEnd, derivingStart)

	if d.RecordFields != nil {
		attributed := false
		for _, f := range d.RecordFields {
			attributed = attributed || len(f.Attributes) > 0
		}
		if attributed {
			p.attributedRecord(d, head, tail)
			return
		}
		fields := make([]string, len(d.RecordFields))
		for i, f := range d.RecordFields {
			fields[i] = f.Name + " : " + typeText(f.Type)
		}
		if !broken {
			p.line(0, head+" = { "+strings.Join(fields, ", ")+" }"+inlineDeriving(tail, separateDeriving))
			if separateDeriving {
				p.line(Indent, strings.TrimSpace(tail))
			}
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
		p.line(Indent, "}")
		if tail != "" {
			p.line(Indent, strings.TrimSpace(tail))
		}
		return
	}

	alts := make([]string, len(d.Ctors))
	for i, c := range d.Ctors {
		parts := []string{}
		for _, g := range c.Attributes {
			parts = append(parts, attributeGroupText(g))
		}
		parts = append(parts, c.Name)
		for j, a := range c.Args {
			if j < len(c.FieldAttributes) {
				for _, g := range c.FieldAttributes[j] {
					parts = append(parts, attributeGroupText(g))
				}
			}
			parts = append(parts, typeArgText(a))
		}
		alts[i] = strings.Join(parts, " ")
	}
	if !broken {
		p.line(0, head+" = "+strings.Join(alts, " | ")+inlineDeriving(tail, separateDeriving))
		if separateDeriving {
			p.line(Indent, strings.TrimSpace(tail))
		}
		return
	}
	p.line(0, head)
	for i, alt := range alts {
		lead := "| "
		if i == 0 {
			lead = "= "
		}
		p.line(Indent, lead+alt)
	}
	if tail != "" {
		p.line(Indent, strings.TrimSpace(tail))
	}
}

func inlineDeriving(tail string, separate bool) string {
	if separate {
		return ""
	}
	return tail
}

// typeRHSBounds identifies the source portion that determines whether the
// type body is broken. A separately written deriving clause does not make an
// otherwise inline right-hand side multiline.
func typeRHSBounds(d *ast.TypeDecl, sp source.Span) (end, derivingStart int) {
	end = sp.End
	derivingStart = sp.End
	if len(d.Deriving) == 0 || sp.File == nil {
		return end, derivingStart
	}
	derivingStart = d.Deriving[0].Sp.Start
	from := d.NameSpan.End
	if from < sp.Start || derivingStart < from || derivingStart > sp.End {
		return end, derivingStart
	}
	if i := bytes.LastIndex(sp.File.Content[from:derivingStart], []byte("deriving")); i >= 0 {
		end = trimSpaceBefore(sp.File.Content, from+i)
	}
	return end, derivingStart
}

func trimSpaceBefore(src []byte, end int) int {
	for end > 0 {
		switch src[end-1] {
		case ' ', '\t', '\r', '\n':
			end--
		default:
			return end
		}
	}
	return end
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

// renderEquationHead writes the left of an `=`. A Unit parameter written
// against the name keeps that spelling, the same adjacency rule application
// obeys. Patterns may themselves have a block form.
func (p *printer) renderEquationHead(name string, eq ast.Equation, ind int) bool {
	p.emit(declName(name))
	var prev ast.Pattern
	for i, param := range eq.Params {
		if _, isUnit := param.(*ast.PUnit); isUnit && paramAdjacent(eq, i) {
			p.emit("()")
			continue
		}
		p.emit(" ")
		if !p.renderPatternParam(param, prev, ind) {
			return false
		}
		prev = param
	}
	return true
}

// paramAdjacent reports whether a parameter was written with no space before
// it, which for `()` is what `args()` means.
func paramAdjacent(eq ast.Equation, i int) bool {
	sp := eq.Params[i].Span()
	return writtenAgainst(sp.File, sp.Start)
}

// instanceLines renders `instance Preds => Head` and its method definitions,
// which are ordinary value declarations one level in.
func (p *printer) instanceLines(d *ast.InstanceDecl) bool {
	head := "instance "
	if len(d.Preds) > 0 {
		head += predsText(d.Preds) + " => "
	}
	head += predText(d.Head)
	p.line(0, head)
	return p.methodLines(d.Methods)
}

// deriverLines renders `deriver Class` and its method generators.
func (p *printer) deriverLines(d *ast.DeriverDecl) bool {
	p.line(0, "deriver "+d.Class)
	return p.methodLines(d.Methods)
}

func (p *printer) methodLines(methods []*ast.ValueDecl) bool {
	for _, m := range methods {
		if !p.valueDeclLines(m, Indent) {
			return false
		}
	}
	p.flush()
	return true
}

func predText(p ast.PredExpr) string {
	return p.Class + " " + typeArgText(p.Ty)
}

// predsText renders a constraint context, parenthesized only when there is
// more than one constraint.
func predsText(preds []ast.PredExpr) string {
	parts := make([]string, len(preds))
	for i, pr := range preds {
		parts[i] = predText(pr)
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

func attributeGroupText(g ast.AttributeGroup) string {
	parts := make([]string, len(g.Exprs))
	for i, e := range g.Exprs {
		value, ok := exprInline(e)
		if !ok {
			return raw(g.Sp)
		}
		parts[i] = value
	}
	return "#[" + strings.Join(parts, ", ") + "]"
}

// leadingFieldAttributes derives presentation from spans; consumers keep one
// ordered collection regardless of which side of the field a group occupies.
func leadingFieldAttributes(f ast.RecordFieldDef) int {
	for i, group := range f.Attributes {
		if group.Sp.Start > f.NameSpan.Start {
			return i
		}
	}
	return len(f.Attributes)
}

func (p *printer) inlineFieldAttribute(from int, group ast.AttributeGroup) bool {
	return !brokeBetween(p.f, from, group.Sp.Start) &&
		!p.cs.holdsComment(source.Span{File: p.f, Start: from, End: group.Sp.Start})
}

func (p *printer) attributedRecord(d *ast.TypeDecl, head, tail string) {
	width := 0
	for _, f := range d.RecordFields {
		n := leadingFieldAttributes(f)
		if n < len(f.Attributes) && p.inlineFieldAttribute(f.Type.Span().End, f.Attributes[n]) {
			width = max(width, len(f.Name+" : "+typeText(f.Type)))
		}
	}
	tagColumn := Indent + 2 + width + 2
	p.line(0, head+" =")
	for i, f := range d.RecordFields {
		lead := ", "
		if i == 0 {
			lead = "{ "
		}
		n := leadingFieldAttributes(f)
		for j, group := range f.Attributes[:n] {
			if j == 0 {
				p.attributeGroupLine(group, Indent, lead)
			} else {
				p.attributeGroupLine(group, Indent+2, "")
			}
		}
		fieldIndent := Indent
		if n > 0 {
			fieldIndent += 2
			lead = ""
		}
		p.cs.emitBefore(p, f.NameSpan.Start, fieldIndent)
		p.start(fieldIndent)
		p.emit(lead + f.Name + " : " + typeText(f.Type))
		from := f.Type.Span().End
		for j, group := range f.Attributes[n:] {
			if p.inlineFieldAttribute(from, group) {
				padding := 1
				if j == 0 {
					padding = tagColumn - p.column()
				}
				p.emit(strings.Repeat(" ", padding))
			} else {
				p.cs.emitBefore(p, group.Sp.Start, Indent+2+Indent)
				p.start(Indent + 2 + Indent)
			}
			p.attributeGroup(group)
			from = group.Sp.End
		}
		p.flush()
	}
	p.line(Indent, "}")
	if tail != "" {
		p.line(Indent, strings.TrimSpace(tail))
	}
}

func (p *printer) attributeGroupLine(g ast.AttributeGroup, ind int, lead string) {
	p.cs.emitBefore(p, g.Sp.Start, ind+len(lead))
	p.start(ind)
	p.emit(lead)
	p.attributeGroup(g)
	p.flush()
}

// attributeGroup appends at the current cursor, retaining multiline contents
// relative to the tag's new starting column. It leaves its last line open so
// a following inline group or comment can share that line.
func (p *printer) attributeGroup(g ast.AttributeGroup) {
	text := attributeGroupText(g)
	if bytes.ContainsRune(g.Sp.File.Content[g.Sp.Start:g.Sp.End], '\n') || p.cs.holdsComment(g.Sp) {
		text = raw(g.Sp)
	}
	lines := strings.Split(text, "\n")
	delta := p.column() - (g.Sp.StartPos().Col - 1)
	p.emit(lines[0])
	for _, line := range lines[1:] {
		whitespace := len(line) - len(strings.TrimLeft(line, " "))
		indent := max(0, whitespace+delta)
		p.start(indent)
		p.emit(strings.TrimLeft(line, " "))
	}
	p.cs.skipTo(g.Sp.End)
}
