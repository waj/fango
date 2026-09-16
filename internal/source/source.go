// Package source tracks source files, positions, and spans, and renders
// caret-underlined excerpts for diagnostics.
package source

import (
	"fmt"
	"sort"
	"strings"
)

// File is one Fango source file with precomputed line offsets.
type File struct {
	Name        string
	Content     []byte
	lineOffsets []int // byte offset of the start of each line, 0-based line index
}

func NewFile(name string, content []byte) *File {
	offsets := []int{0}
	for i, b := range content {
		if b == '\n' {
			offsets = append(offsets, i+1)
		}
	}
	return &File{Name: name, Content: content, lineOffsets: offsets}
}

// Pos is a 1-based line/column position. Columns count bytes; tabs are
// rejected in indentation by the lexer so columns are unambiguous.
type Pos struct {
	Line, Col int
}

// Span is a half-open byte range [Start, End) in File.
type Span struct {
	File       *File
	Start, End int
}

func (s Span) StartPos() Pos { return s.File.pos(s.Start) }
func (s Span) EndPos() Pos   { return s.File.pos(s.End) }

// Merge returns the smallest span covering both s and o (same file assumed).
func (s Span) Merge(o Span) Span {
	r := s
	if o.Start < r.Start {
		r.Start = o.Start
	}
	if o.End > r.End {
		r.End = o.End
	}
	return r
}

func (f *File) pos(offset int) Pos {
	line := sort.Search(len(f.lineOffsets), func(i int) bool {
		return f.lineOffsets[i] > offset
	}) - 1
	if line < 0 {
		line = 0
	}
	return Pos{Line: line + 1, Col: offset - f.lineOffsets[line] + 1}
}

// Line returns the text of the 1-based line number, without its newline.
func (f *File) Line(n int) string {
	if n < 1 || n > len(f.lineOffsets) {
		return ""
	}
	start := f.lineOffsets[n-1]
	end := len(f.Content)
	if n < len(f.lineOffsets) {
		end = f.lineOffsets[n] - 1
	}
	return string(f.Content[start:end])
}

// Excerpt renders the span's first line with a caret underline:
//
//	3| main = 1 + True
//	            ^^^^^^
func (f *File) Excerpt(sp Span) string {
	start := sp.StartPos()
	lineText := f.Line(start.Line)
	prefix := fmt.Sprintf("%d| ", start.Line)

	end := sp.EndPos()
	underlineLen := 1
	if end.Line == start.Line && end.Col > start.Col {
		underlineLen = end.Col - start.Col
	} else if end.Line > start.Line {
		underlineLen = len(lineText) - start.Col + 1
	}
	if underlineLen < 1 {
		underlineLen = 1
	}

	var b strings.Builder
	b.WriteString(prefix)
	b.WriteString(lineText)
	b.WriteByte('\n')
	b.WriteString(strings.Repeat(" ", len(prefix)+start.Col-1))
	b.WriteString(strings.Repeat("^", underlineLen))
	return b.String()
}
