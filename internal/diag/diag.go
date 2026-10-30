// Package diag defines the diagnostic type shared by every compiler stage
// and renders diagnostics in the Elm style: a title bar, a caret-underlined
// source excerpt, and prose.
package diag

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/waj/fango/internal/source"
)

type Error struct {
	Title   string // e.g. "TYPE MISMATCH", "NAMING ERROR", "SYNTAX PROBLEM"
	Span    source.Span
	Body    string    // prose above the excerpt
	Related []Related // further source locations, below the excerpt
	Notes   []string  // prose below the excerpt
}

// Related is a second location that explains an error, such as the call that
// performs an effect an annotation omits.
type Related struct {
	Label string
	Span  source.Span
}

const barWidth = 64

// Render writes each error in the Elm style:
//
//	-- TYPE MISMATCH ------------------------------------ main.fango
//
//	<body>
//
//	3| main = 1 + True
//	            ^^^^^^
//	<related>
//	<notes>
func Render(w io.Writer, errs []Error) {
	for i, e := range Dedup(errs) {
		if i > 0 {
			fmt.Fprintln(w)
		}
		name := ""
		if e.Span.File != nil {
			name = e.Span.File.Name
		}
		header := "-- " + e.Title + " "
		dashes := barWidth - len(header) - len(name) - 1
		if dashes < 4 {
			dashes = 4
		}
		fmt.Fprintf(w, "%s%s %s\n\n", header, strings.Repeat("-", dashes), name)
		if e.Body != "" {
			fmt.Fprintf(w, "%s\n\n", e.Body)
		}
		if e.Span.File != nil {
			fmt.Fprintf(w, "%s\n", e.Span.File.Excerpt(e.Span))
		}
		for _, r := range e.Related {
			fmt.Fprintf(w, "\n%s\n", r.Label)
			if r.Span.File != nil {
				fmt.Fprintf(w, "\n%s\n", r.Span.File.Excerpt(r.Span))
			}
		}
		for _, n := range e.Notes {
			fmt.Fprintf(w, "\n%s\n", n)
		}
	}
}

func Errorf(sp source.Span, title, format string, args ...any) Error {
	return Error{Title: title, Span: sp, Body: fmt.Sprintf(format, args...)}
}

// Dedup drops errors that repeat an earlier one exactly, which happens when
// generated code or a shared obligation reaches the same check twice.
func Dedup(errs []Error) []Error {
	type key struct {
		title, body string
		file        *source.File
		start, end  int
	}
	seen := make(map[key]bool, len(errs))
	out := make([]Error, 0, len(errs))
	for _, e := range errs {
		k := key{e.Title, e.Body, e.Span.File, e.Span.Start, e.Span.End}
		if !seen[k] {
			seen[k] = true
			out = append(out, e)
		}
	}
	return out
}

// SortBySource orders errors by where they occur, so one declaration's reports
// read top to bottom whatever order its checks ran in. Errors in different
// files, or without a location, keep their relative order.
func SortBySource(errs []Error) {
	sort.SliceStable(errs, func(i, j int) bool {
		a, b := errs[i].Span, errs[j].Span
		return a.File != nil && a.File == b.File && a.Start < b.Start
	})
}

// Suggest returns the candidate closest to name when one is close enough to
// be a likely typo, or "". Near-equal candidates resolve to the first in
// sorted order, so the suggestion is deterministic.
func Suggest(name string, candidates []string) string {
	// A one- or two-letter name is close to too much to mean anything.
	if len(name) < 3 {
		return ""
	}
	limit := 2
	if len(name) == 3 {
		limit = 1
	}
	sorted := append([]string(nil), candidates...)
	sort.Strings(sorted)
	best, bestDistance := "", limit+1
	for _, c := range sorted {
		if c == name {
			continue
		}
		if d := editDistance(name, c); d < bestDistance {
			best, bestDistance = c, d
		}
	}
	return best
}

// DidYouMean is the note Suggest's answer becomes, or "" when there is none.
func DidYouMean(name string, candidates []string) string {
	if s := Suggest(name, candidates); s != "" {
		return "Did you mean `" + s + "`?"
	}
	return ""
}

// editDistance is the Levenshtein distance, counting an adjacent swap as one
// edit because transposed letters are the commonest typo.
func editDistance(a, b string) int {
	ar, br := []rune(a), []rune(b)
	prev2 := make([]int, len(br)+1)
	prev := make([]int, len(br)+1)
	cur := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		cur[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
			if i > 1 && j > 1 && ar[i-1] == br[j-2] && ar[i-2] == br[j-1] {
				cur[j] = min(cur[j], prev2[j-2]+1)
			}
		}
		prev2, prev, cur = prev, cur, prev2
	}
	return prev[len(br)]
}
