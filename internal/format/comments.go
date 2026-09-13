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
