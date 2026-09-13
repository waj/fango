package format

import (
	"strings"

	"github.com/waj/fango/internal/ast"
)

// renderPattern writes p continuing the current line. List and tuple patterns
// are the only patterns with a block form; a constructor containing one is
// rendered structurally as well so nested bracket syntax does not force its
// declaration back to verbatim text.
func (p *printer) renderPattern(pat ast.Pattern, ind int) bool {
	if !brokeWithin(pat.Span()) {
		s, ok := patternInline(pat)
		if ok {
			p.emit(s)
		}
		return ok
	}
	c, isCtor := pat.(*ast.PCtor)
	if !isCtor {
		return false
	}
	if elems, tail, ok := asListPattern(c); ok {
		return p.renderPatternList(elems, tail, ind)
	}
	if elems, ok := asTuplePattern(c); ok {
		return p.renderPatternTuple(elems, ind)
	}
	return p.renderCtorPattern(c, ind)
}

func (p *printer) renderPatternArgs(ps []ast.Pattern, ind int) bool {
	for _, pat := range ps {
		p.emit(" ")
		if !p.renderPatternArg(pat, ind) {
			return false
		}
	}
	return true
}

func (p *printer) renderPatternArg(pat ast.Pattern, ind int) bool {
	if !brokeWithin(pat.Span()) {
		s, ok := patternArgInline(pat)
		if ok {
			p.emit(s)
		}
		return ok
	}
	if c, ok := pat.(*ast.PCtor); ok {
		if _, _, list := asListPattern(c); list {
			return p.renderPattern(pat, ind)
		}
		if _, tuple := asTuplePattern(c); tuple {
			return p.renderPattern(pat, ind)
		}
	}
	p.emit("(")
	if !p.renderPattern(pat, ind) {
		return false
	}
	p.emit(")")
	return true
}

func (p *printer) renderCtorPattern(c *ast.PCtor, ind int) bool {
	p.emit(c.Name)
	prevEnd := c.NameSpan.End
	base := p.lineIndent(ind)
	for _, arg := range c.Args {
		if brokeBetween(arg.Span().File, prevEnd, arg.Span().Start) {
			p.start(base + Indent)
		} else {
			p.emit(" ")
		}
		if !p.renderPatternArg(arg, base) {
			return false
		}
		prevEnd = arg.Span().End
	}
	return true
}

func (p *printer) renderPatternList(elems []ast.Pattern, tail ast.Pattern, ind int) bool {
	if len(elems) == 0 {
		p.emit("[]")
		return true
	}
	base := p.lineIndent(ind)
	p.emit("[ ")
	prevEnd := elems[0].Span().Start
	for i, elem := range elems {
		if i > 0 {
			if brokeBetween(elem.Span().File, prevEnd, elem.Span().Start) {
				p.start(base)
			}
			p.emit(", ")
		}
		if !p.renderPattern(elem, base) {
			return false
		}
		prevEnd = elem.Span().End
	}
	if tail != nil {
		if brokeBetween(tail.Span().File, prevEnd, tail.Span().Start) {
			p.start(base)
			p.emit("| ")
		} else {
			p.emit(" | ")
		}
		if !p.renderPattern(tail, base) {
			return false
		}
	}
	p.start(base)
	p.emit("]")
	return true
}

func (p *printer) renderPatternTuple(elems []ast.Pattern, ind int) bool {
	base := p.lineIndent(ind)
	rowInd := base + Indent
	p.emit("( ")
	prevEnd := elems[0].Span().Start
	for i, elem := range elems {
		if i > 0 {
			if brokeBetween(elem.Span().File, prevEnd, elem.Span().Start) {
				p.start(rowInd)
			}
			p.emit(", ")
		}
		if !p.renderPattern(elem, base) {
			return false
		}
		prevEnd = elem.Span().End
	}
	p.start(rowInd)
	p.emit(")")
	return true
}

// patternInline renders a pattern on one line, reporting whether it could.
// Patterns have no layout of their own, so the only failure is a shape the
// printer does not handle.
func patternInline(p ast.Pattern) (string, bool) {
	switch p := p.(type) {
	case *ast.PVar:
		return p.Name, true
	case *ast.PWildcard:
		return "_", true
	case *ast.PUnit:
		return "()", true
	case *ast.PInt, *ast.PFloat, *ast.PString, *ast.PChar:
		return raw(p.Span()), true
	case *ast.PPin:
		return "^" + p.Name, true
	case *ast.PRecord:
		fields := make([]string, len(p.Fields))
		for i, f := range p.Fields {
			v, ok := patternInline(f.Pattern)
			if !ok {
				return "", false
			}
			fields[i] = f.Name + " = " + v
		}
		body := "{ " + strings.Join(fields, ", ") + " }"
		if p.Name != "" {
			return p.Name + " " + body, true
		}
		return body, true
	case *ast.PCtor:
		return ctorPatternInline(p)
	}
	return "", false
}

// patternArgInline renders a pattern in argument position, parenthesizing a
// constructor pattern that carries arguments.
func patternArgInline(p ast.Pattern) (string, bool) {
	s, ok := patternInline(p)
	if !ok {
		return "", false
	}
	if c, isCtor := p.(*ast.PCtor); isCtor && len(c.Args) > 0 {
		if _, _, isList := asListPattern(c); isList {
			return s, true
		}
		if _, isTuple := asTuplePattern(c); isTuple {
			return s, true
		}
		return "(" + s + ")", true
	}
	// `{ x = a }` brackets itself, but `Point { x = a }` is two tokens. Either
	// spelling parses, and the parenthesized one is chosen so that patterns and
	// expressions treat a named record the same way.
	if r, isRecord := p.(*ast.PRecord); isRecord && r.Name != "" {
		return "(" + s + ")", true
	}
	return s, true
}

func ctorPatternInline(p *ast.PCtor) (string, bool) {
	if p.Sugared && p.Name == "List.Nil" && len(p.Args) == 0 {
		return "[]", true
	}
	if elems, tail, ok := asListPattern(p); ok {
		parts := make([]string, len(elems))
		for i, el := range elems {
			s, elemOK := patternInline(el)
			if !elemOK {
				return "", false
			}
			parts[i] = s
		}
		body := strings.Join(parts, ", ")
		if tail == nil {
			return "[" + body + "]", true
		}
		t, tailOK := patternInline(tail)
		if !tailOK {
			return "", false
		}
		return "[" + body + " | " + t + "]", true
	}
	if elems, ok := asTuplePattern(p); ok {
		parts := make([]string, len(elems))
		for i, el := range elems {
			s, elemOK := patternInline(el)
			if !elemOK {
				return "", false
			}
			parts[i] = s
		}
		return "(" + strings.Join(parts, ", ") + ")", true
	}
	parts := []string{p.Name}
	for _, a := range p.Args {
		s, ok := patternArgInline(a)
		if !ok {
			return "", false
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " "), true
}

// asListPattern mirrors asList for patterns.
func asListPattern(p ast.Pattern) (elems []ast.Pattern, tail ast.Pattern, ok bool) {
	cur := p
	for {
		c, isCtor := cur.(*ast.PCtor)
		if !isCtor || !c.Sugared {
			if len(elems) == 0 {
				return nil, nil, false
			}
			return elems, cur, true
		}
		switch {
		case c.Name == "List.Nil" && len(c.Args) == 0:
			return elems, nil, len(elems) > 0
		case c.Name == "List.Cons" && len(c.Args) == 2:
			elems = append(elems, c.Args[0])
			cur = c.Args[1]
		default:
			if len(elems) == 0 {
				return nil, nil, false
			}
			return elems, cur, true
		}
	}
}

func asTuplePattern(p ast.Pattern) ([]ast.Pattern, bool) {
	c, ok := p.(*ast.PCtor)
	if !ok || !c.Sugared {
		return nil, false
	}
	if (c.Name == "Tuple.Pair" && len(c.Args) == 2) || (c.Name == "Tuple.Triple" && len(c.Args) == 3) {
		return c.Args, true
	}
	return nil, false
}

// patternsInline renders a parameter list, space separated.
func patternsInline(ps []ast.Pattern, render func(ast.Pattern) (string, bool)) (string, bool) {
	parts := make([]string, len(ps))
	for i, p := range ps {
		s, ok := render(p)
		if !ok {
			return "", false
		}
		parts[i] = s
	}
	return strings.Join(parts, " "), true
}
