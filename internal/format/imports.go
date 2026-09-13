package format

import (
	"sort"
	"strings"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/token"
)

// importEntry is one import line together with the comments belonging to it.
// Sorting moves an import, and a comment written directly above one is about
// that import, so it travels with it.
type importEntry struct {
	imp      ast.Import
	headLine int
	leading  []token.Comment // an unbroken run of own-line comments directly above
	trailing string          // written at the end of the import's own line
}

// collectImports pairs each import with its comments, returning the entries in
// canonical order and, separately, the comments that belong to the block as a
// whole. A comment separated from the import below it by a blank line is about
// the block or about nothing in particular, so it stays at the top rather than
// being dragged somewhere else by the sort.
func collectImports(f *source.File, items []preludeItem, m *ast.Module, cs []token.Comment) (entries []importEntry, floating []token.Comment) {
	used := make([]bool, len(cs))

	for _, it := range items {
		if it.kind != kindImport {
			continue
		}
		e := importEntry{imp: m.Imports[it.index], headLine: it.span.StartPos().Line}

		// A comment after the import, on the last line the import occupies.
		endLine := it.span.EndPos().Line
		for i, cm := range cs {
			if used[i] || cm.Span.Start < it.span.Start {
				continue
			}
			if cm.Span.StartPos().Line == endLine {
				e.trailing = strings.TrimRight(cm.Text, " \t")
				used[i] = true
			}
			break
		}

		// The unbroken run of own-line comments immediately above it.
		want := it.span.StartPos().Line - 1
		for i := len(cs) - 1; i >= 0; i-- {
			if used[i] || cs[i].Span.End > it.span.Start {
				continue
			}
			if !ownLineAt(f, cs[i], want) {
				break
			}
			e.leading = append([]token.Comment{cs[i]}, e.leading...)
			used[i] = true
			want = cs[i].Span.StartPos().Line - 1
		}
		entries = append(entries, e)
	}

	// The block owns every comment from the end of the construct above it
	// through its last import, so a comment set off by a blank line above the
	// first import is kept rather than dropped when the lines are reordered.
	lo, hi := importRegion(items)
	from := 0
	for _, it := range items {
		if it.kind != kindImport && it.span.End <= lo && it.span.End > from {
			from = it.span.End
		}
	}
	for i, cm := range cs {
		if !used[i] && cm.Span.Start >= from && cm.Span.End <= hi {
			floating = append(floating, cm)
		}
	}

	sort.SliceStable(entries, func(i, j int) bool { return entries[i].imp.Module < entries[j].imp.Module })
	return entries, floating
}

// ownLineAt reports whether the comment is the whole of line want.
func ownLineAt(f *source.File, cm token.Comment, want int) bool {
	if cm.Span.StartPos().Line != want || cm.Span.EndPos().Line != want {
		return false
	}
	return strings.TrimSpace(f.Line(want)) == strings.TrimSpace(cm.Text)
}

// importRegion is the source range the import lines span, which bounds the
// comments the block owns.
func importRegion(items []preludeItem) (lo, hi int) {
	lo, hi = -1, -1
	for _, it := range items {
		if it.kind != kindImport {
			continue
		}
		if lo < 0 {
			lo = it.span.Start
		}
		if it.span.End > hi {
			hi = it.span.End
		}
	}
	return lo, hi
}

// printImportBlock writes the sorted import lines as one block. Blank lines the
// author left between imports are not reproduced: sorting has already
// dissolved whatever grouping they marked, so the block is contiguous.
func (p *printer) printImportBlock(entries []importEntry, floating []token.Comment) {
	// The blank line below a block-level comment is what distinguishes it from
	// a comment about the first import, so it is kept rather than collapsed.
	for _, cm := range floating {
		p.line(0, strings.TrimRight(cm.Text, " \t"))
	}
	if len(floating) > 0 {
		p.blank()
	}
	for _, e := range entries {
		for _, cm := range e.leading {
			p.line(0, strings.TrimRight(cm.Text, " \t"))
		}
		p.importLines(e.imp, e.headLine)
		if e.trailing != "" {
			p.appendTrailing(e.trailing)
		}
	}
}
