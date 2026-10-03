// Package apidoc attaches source comments to declarations and renders their
// checked signatures. The language server's hover and `fango doc` share it, so
// a comment written for one reads the same in the other.
package apidoc

import (
	"strings"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/token"
)

// Leading returns the documentation written immediately above anchor: a
// contiguous run of comments, each alone on its lines, read upward until a
// blank or code line. Declaration pragmas and the declaration's own leading
// attribute tags may sit between the comments and the anchor. comments are
// the anchor file's comments in source order.
func Leading(comments []token.Comment, anchor source.Span, attributes ...ast.AttributeGroup) string {
	if anchor.File == nil {
		return ""
	}
	f := anchor.File
	line := anchor.StartPos().Line - 1
	var chunks []string
	for line > 0 {
		// Skip only this declaration's leading tags, using parsed spans so
		// nested brackets and comments inside multiline payloads stay opaque.
		// Trailing field tags cannot bridge documentation from another field.
		skipped := false
		for _, group := range attributes {
			if group.Sp.Start >= anchor.Start {
				continue
			}
			start, end := group.Sp.StartPos().Line, group.Sp.EndPos().Line
			if start <= line && line <= end {
				line = start - 1
				skipped = true
				break
			}
		}
		if skipped {
			continue
		}
		trim := strings.TrimSpace(f.Line(line))
		if strings.HasPrefix(trim, "{-#") && strings.HasSuffix(trim, "#-}") {
			line--
			continue
		}
		var found *token.Comment
		for n := range comments {
			if comments[n].Span.EndPos().Line == line {
				found = &comments[n]
				break
			}
		}
		if found == nil {
			break
		}
		start := found.Span.StartPos()
		lineStart := found.Span.Start - start.Col + 1
		if strings.TrimSpace(string(f.Content[lineStart:found.Span.Start])) != "" {
			break
		}
		chunks = append(chunks, commentText(found.Text, found.Block))
		line = found.Span.StartPos().Line - 1
	}
	for a, b := 0, len(chunks)-1; a < b; a, b = a+1, b-1 {
		chunks[a], chunks[b] = chunks[b], chunks[a]
	}
	return strings.TrimSpace(strings.Join(chunks, "\n"))
}

// StartsLine reports whether sp is the first code on its line, ignoring a
// leading separator. A constructor or field written on its type's own line
// shares that line's comments with the type, so it has none of its own.
func StartsLine(sp source.Span, attributes ...ast.AttributeGroup) bool {
	if sp.File == nil {
		return false
	}
	start := sp.Start
	for _, group := range attributes {
		if group.Sp.File != nil && group.Sp.Start < start {
			start = group.Sp.Start
		}
	}
	pos := source.Span{File: sp.File, Start: start, End: start}.StartPos()
	before := strings.TrimSpace(string(sp.File.Content[start-pos.Col+1 : start]))
	switch before {
	case "", "=", "|", ",", "{":
		return true
	}
	return false
}

// commentText strips comment delimiters but keeps the relative indentation
// of the lines inside, which Markdown code blocks and lists depend on. A line
// comment loses its `--` and one following space; a block comment's later
// lines lose the indentation they share.
func commentText(s string, block bool) string {
	if !block {
		s = strings.TrimPrefix(s, "--")
		s = strings.TrimPrefix(s, " ")
		return strings.TrimRight(s, " \t\r")
	}
	s = strings.TrimSuffix(strings.TrimPrefix(s, "{-"), "-}")
	lines := strings.Split(strings.TrimSpace(s), "\n")
	indent := -1
	for _, l := range lines[1:] {
		if strings.TrimSpace(l) == "" {
			continue
		}
		n := len(l) - len(strings.TrimLeft(l, " \t"))
		if indent < 0 || n < indent {
			indent = n
		}
	}
	for i := range lines {
		l := strings.TrimRight(lines[i], " \t\r")
		if i > 0 && indent > 0 && len(l) >= indent {
			l = l[indent:]
		}
		lines[i] = l
	}
	return strings.Join(lines, "\n")
}
