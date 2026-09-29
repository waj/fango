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
			attributed = attributed || f.JSONKeySet || f.JSONSkip || f.JSONDefault != nil
		}
		if attributed {
			p.line(0, head+" =")
			for i, f := range d.RecordFields {
				lead := ", "
				if i == 0 {
					lead = "{ "
				}
				attrs := ""
				if f.JSONKeySet {
					attrs += "{-# json key " + strconv.Quote(f.JSONKey) + " #-} "
				}
				if f.JSONSkip {
					attrs += "{-# json skip #-} "
				}
				if f.JSONDefault != nil {
					attrs += "{-# json default " + strings.TrimSpace(raw(f.JSONDefault.Span())) + " #-} "
				}
				p.line(Indent, lead+attrs+f.Name+" : "+typeText(f.Type))
			}
			closing := "}"
			if tail != "" {
				closing += " " + strings.TrimSpace(tail)
			}
			p.line(Indent, closing)
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
		parts := []string{c.Name}
		for _, a := range c.Args {
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
