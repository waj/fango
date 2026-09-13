package format

import (
	"strings"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/token"
)

// printer accumulates output lines. Indentation is applied when a line is
// written rather than carried as cursor state, so an emitted line is always
// either empty or begins at exactly its own indent.
//
// srcEnd is the source offset just past the last construct written. Blank
// lines are reproduced from the gaps between constructs rather than imposed by
// section rules, which is the same principle as preserving line breaks: the
// author decides where the air goes.
type printer struct {
	f      *source.File
	buf    strings.Builder
	srcEnd int
}

func (p *printer) line(indent int, text string) {
	if text != "" {
		p.buf.WriteString(strings.Repeat(" ", indent))
		p.buf.WriteString(text)
	}
	p.buf.WriteByte('\n')
}

// gapBefore reproduces a blank line the author left between the construct just
// written and the one about to be.
func (p *printer) gapBefore(start int) {
	if p.buf.Len() == 0 || start < p.srcEnd {
		return
	}
	if strings.Count(string(p.f.Content[p.srcEnd:start]), "\n") >= 2 {
		p.blank()
	}
}

func (p *printer) blank() {
	if s := p.buf.String(); s != "" && !strings.HasSuffix(s, "\n\n") {
		p.buf.WriteByte('\n')
	}
}

// verbatim copies a source range unchanged, trimming trailing whitespace from
// each line but touching nothing else. It is how the formatter handles every
// construct whose printer is not written yet, and how it will keep handling a
// declaration holding a comment in a position with no anchor.
func (p *printer) verbatim(sp source.Span) {
	text := string(p.f.Content[sp.Start:sp.End])
	for ln := range strings.SplitSeq(text, "\n") {
		p.line(0, strings.TrimRight(ln, " \t"))
	}
	p.srcEnd = sp.End
}

// preludeKind distinguishes the constructs above the first declaration.
type preludeKind int

const (
	kindPragma preludeKind = iota
	kindHeader
	kindImport
)

// preludeItem is one construct above the first declaration, with the exact
// source extent it occupies. The extent comes from the token stream because
// the AST records no span for a module header or an import as a whole, and an
// approximate extent would put blank lines in the wrong places.
type preludeItem struct {
	kind  preludeKind
	span  source.Span
	text  string // pragma body
	index int    // into Module.Imports
}

// scanPrelude walks the tokens above the first declaration and recovers the
// extent of each pragma, the module header, and each import.
func scanPrelude(f *source.File, toks []token.Token, bodyStart int) []preludeItem {
	var items []preludeItem
	starts := []int{}
	for i, t := range toks {
		if t.Span.Start >= bodyStart || t.Kind == token.EOF {
			break
		}
		switch t.Kind {
		case token.PRAGMA:
			items = append(items, preludeItem{kind: kindPragma, span: t.Span, text: t.Text})
		case token.KwModule:
			items = append(items, preludeItem{kind: kindHeader})
			starts = append(starts, i)
		case token.KwImport:
			items = append(items, preludeItem{kind: kindImport})
			starts = append(starts, i)
		}
	}
	// Give the header and each import the range running to the next such
	// construct, or to the first declaration.
	importIndex := 0
	at := 0
	for i := range items {
		if items[i].kind == kindPragma {
			continue
		}
		from := starts[at]
		at++
		to := bodyStart
		if at < len(starts) {
			to = toks[starts[at]].Span.Start
		}
		items[i].span = source.Span{File: f, Start: toks[from].Span.Start, End: lastTokenEnd(toks, from, to)}
		if items[i].kind == kindImport {
			items[i].index = importIndex
			importIndex++
		}
	}
	return items
}

// lastTokenEnd returns the end of the final token starting before to.
func lastTokenEnd(toks []token.Token, from, to int) int {
	end := toks[from].Span.End
	for i := from; i < len(toks); i++ {
		if toks[i].Kind == token.EOF || toks[i].Span.Start >= to {
			break
		}
		end = toks[i].Span.End
	}
	return end
}

// printModule renders the module header and import block, then copies the
// declarations verbatim. Expressions, types and the rest of the declaration
// grammar still reach the output through their own source text.
func printModule(f *source.File, m *ast.Module, toks []token.Token, comments []token.Comment) []byte {
	p := &printer{f: f}
	cs := newCommentCursor(f, comments)

	bodyStart := len(f.Content)
	if len(m.Decls) > 0 {
		bodyStart = ast.DeclSpan(m.Decls[0]).Start
	}

	for _, it := range scanPrelude(f, toks, bodyStart) {
		cs.emitBefore(p, it.span.Start, 0)
		p.gapBefore(it.span.Start)
		switch it.kind {
		case kindPragma:
			p.line(0, "{-# "+strings.TrimSpace(it.text)+" #-}")
		case kindHeader:
			p.headerLines(m.Header, it.span.StartPos().Line)
		case kindImport:
			p.importLines(m.Imports[it.index], it.span.StartPos().Line)
		}
		p.srcEnd = it.span.End
	}

	if len(m.Decls) > 0 {
		cs.emitBefore(p, bodyStart, 0)
		p.gapBefore(bodyStart)
		p.verbatim(source.Span{File: f, Start: bodyStart, End: len(f.Content)})
		// The copied region already contains its own comments.
		cs.skipTo(len(f.Content) + 1)
	} else {
		cs.emitRest(p, 0)
	}

	return []byte(tidy(p.buf.String()))
}

func (p *printer) headerLines(h *ast.ModuleHeader, line int) {
	p.exposingLines("module "+h.Name+" exposing", &h.Exposing, line)
}

func (p *printer) importLines(im ast.Import, line int) {
	head := "import " + im.Module
	if im.Alias != "" {
		head += " as " + im.Alias
	}
	if im.Exposing == nil {
		p.line(0, head)
		return
	}
	p.exposingLines(head+" exposing", im.Exposing, line)
}

// exposingLines renders `head (a, b, c)`, or the leading-comma block form when
// the author broke the list onto its own lines. Author breaks are preserved, so
// items keep the lines they were written on and the list is never reflowed or
// reordered; what the formatter decides is only how a broken list is spelled.
//
// headLine is the source line the construct starts on, which is what separates
// a list the author moved below `exposing` from one left inline.
func (p *printer) exposingLines(head string, e *ast.Exposing, headLine int) {
	if e.All || len(e.Items) == 0 {
		suffix := " ()"
		if e.All {
			suffix = " (..)"
		}
		p.line(0, head+suffix)
		return
	}
	groups := groupByLine(e.Items)
	if len(groups) == 1 && groups[0][0].Sp.StartPos().Line == headLine {
		p.line(0, head+" ("+strings.Join(itemTexts(e.Items), ", ")+")")
		return
	}
	p.line(0, head)
	for i, g := range groups {
		lead := ", "
		if i == 0 {
			lead = "( "
		}
		p.line(Indent, lead+strings.Join(itemTexts(g), ", "))
	}
	p.line(Indent, ")")
}

// groupByLine splits items into the lines the author wrote them on.
func groupByLine(items []ast.ExposeItem) [][]ast.ExposeItem {
	var groups [][]ast.ExposeItem
	line := -1
	for _, it := range items {
		at := it.Sp.StartPos().Line
		if at != line {
			groups = append(groups, nil)
			line = at
		}
		groups[len(groups)-1] = append(groups[len(groups)-1], it)
	}
	return groups
}

// itemTexts renders exposed names. An operator is stored by its bare spelling
// but is exposed by its `(op)` form, and it is exactly the item whose name does
// not begin with a letter — value and type names always do.
func itemTexts(items []ast.ExposeItem) []string {
	out := make([]string, len(items))
	for i, it := range items {
		name := it.Name
		if !startsWithLetter(name) {
			name = "(" + name + ")"
		}
		if it.All {
			name += "(..)"
		}
		out[i] = name
	}
	return out
}

func startsWithLetter(s string) bool {
	if s == "" {
		return false
	}
	r := rune(s[0])
	return r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}

// tidy collapses runs of blank lines to one, drops leading blank lines, and
// ends the file with exactly one newline.
func tidy(s string) string {
	var out []string
	blank := 0
	for ln := range strings.SplitSeq(s, "\n") {
		ln = strings.TrimRight(ln, " \t")
		if ln == "" {
			blank++
			continue
		}
		if blank > 0 && len(out) > 0 {
			out = append(out, "")
		}
		blank = 0
		out = append(out, ln)
	}
	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, "\n") + "\n"
}

func normalizeSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
