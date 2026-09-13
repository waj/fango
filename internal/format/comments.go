package format

import (
	"strings"

	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/token"
)

// commentCursor walks the comments of a file in source order, handing each one
// to the printer just before the construct it precedes.
//
// A comment is emitted on its own line when the author wrote it on its own
// line, and appended to the previous output line when the author wrote it at
// the end of one. That is the whole rule for the region above the first
// declaration, where the only anchors are pragmas, the module header and the
// import lines. A comment sitting inside a construct has no anchor here; the
// declarations are copied verbatim, so those reach the output untouched.
type commentCursor struct {
	f    *source.File
	cs   []token.Comment
	next int
}

func newCommentCursor(f *source.File, cs []token.Comment) *commentCursor {
	return &commentCursor{f: f, cs: cs}
}

// emitBefore writes every comment that starts before offset. A trailing
// comment is folded onto the line already in the buffer rather than pushed
// onto a new one.
func (c *commentCursor) emitBefore(p *printer, offset, indent int) {
	for c.next < len(c.cs) {
		cm := c.cs[c.next]
		if cm.Span.Start >= offset {
			break
		}
		c.next++
		if c.ownLine(cm) {
			p.gapBefore(cm.Span.Start)
			p.line(indent, strings.TrimRight(cm.Text, " \t"))
			p.srcEnd = cm.Span.End
			continue
		}
		p.appendTrailing(cm.Text)
		p.srcEnd = cm.Span.End
	}
}

// emitRest writes the comments left after the last anchor.
func (c *commentCursor) emitRest(p *printer, indent int) {
	c.emitBefore(p, len(c.f.Content)+1, indent)
}

// ownLine reports whether nothing but whitespace precedes the comment on its
// line, which is what separates a standalone comment from a trailing one.
func (c *commentCursor) ownLine(cm token.Comment) bool {
	for i := cm.Span.Start - 1; i >= 0; i-- {
		switch c.f.Content[i] {
		case '\n':
			return true
		case ' ', '\t', '\r':
			continue
		default:
			return false
		}
	}
	return true
}

// appendTrailing puts a comment back at the end of the line already written,
// where its author put it.
func (p *printer) appendTrailing(text string) {
	if p.open {
		p.emit(" " + strings.TrimRight(text, " \t"))
		return
	}
	s := p.buf.String()
	if !strings.HasSuffix(s, "\n") {
		p.buf.WriteByte(' ')
		p.buf.WriteString(strings.TrimRight(text, " \t"))
		return
	}
	trimmed := strings.TrimRight(s, "\n")
	if trimmed == "" {
		p.buf.Reset()
		p.line(0, strings.TrimRight(text, " \t"))
		return
	}
	p.buf.Reset()
	p.buf.WriteString(trimmed)
	p.buf.WriteByte(' ')
	p.buf.WriteString(strings.TrimRight(text, " \t"))
	p.buf.WriteByte('\n')
}

// skipTo drops the comments up to offset without emitting them, for a region
// the printer copied verbatim and which therefore already contains them.
func (c *commentCursor) skipTo(offset int) {
	for c.next < len(c.cs) && c.cs[c.next].Span.Start < offset {
		c.next++
	}
}

// holdsComment reports whether any comment falls inside the span. A
// declaration that holds one is copied verbatim rather than printed, so the
// comment keeps its place until the printer can anchor it.
func (c *commentCursor) holdsComment(sp source.Span) bool {
	for _, cm := range c.cs {
		if cm.Span.Start >= sp.Start && cm.Span.End <= sp.End {
			return true
		}
	}
	return false
}

// placeBefore emits the comments that precede offset, reporting whether every
// one of them could be placed.
//
// A comment is placeable when nothing but whitespace separates it from the
// construct it precedes, so it is genuinely that construct's comment, or when
// it trails code already written on the current line. Anything else sits
// inside a construct with no anchor — between an operator and its operand, say
// — and the answer is no, which sends the whole declaration to a verbatim
// copy rather than moving the comment somewhere it was not written.
func (p *printer) placeBefore(at, ind int) bool {
	if p.cs == nil {
		return true
	}
	for p.cs.next < len(p.cs.cs) {
		cm := p.cs.cs[p.cs.next]
		if cm.Span.Start >= at {
			return true
		}
		if !p.cs.ownLine(cm) {
			p.cs.next++
			p.appendTrailing(cm.Text)
			continue
		}
		// The run reaches the anchor only through whitespace and any comments
		// following it, so a block of comments above a construct is checked
		// comment by comment rather than as one span.
		boundary := at
		if next := p.cs.next + 1; next < len(p.cs.cs) && p.cs.cs[next].Span.Start < at {
			boundary = p.cs.cs[next].Span.Start
		}
		if !onlyWhitespace(p.f, cm.Span.End, boundary) {
			return false
		}
		p.cs.next++
		if precededByBlankLine(p.f, cm.Span.Start) {
			p.blank()
		}
		p.start(ind)
		p.emit(strings.TrimRight(cm.Text, " \t"))
	}
	return true
}

// consumedThrough reports whether every comment before offset has been placed.
func (c *commentCursor) consumedThrough(offset int) bool {
	return c.next >= len(c.cs) || c.cs[c.next].Span.Start >= offset
}

func onlyWhitespace(f *source.File, from, to int) bool {
	if f == nil || from < 0 || to > len(f.Content) || from > to {
		return false
	}
	for _, b := range f.Content[from:to] {
		switch b {
		case ' ', '\t', '\n', '\r':
		default:
			return false
		}
	}
	return true
}

func precededByBlankLine(f *source.File, at int) bool {
	newlines := 0
	for i := at - 1; i >= 0; i-- {
		switch f.Content[i] {
		case ' ', '\t', '\r':
		case '\n':
			newlines++
			if newlines > 1 {
				return true
			}
		default:
			return false
		}
	}
	return false
}
