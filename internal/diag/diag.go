// Package diag defines the diagnostic type shared by every compiler stage
// and renders diagnostics in the Elm style: a title bar, a caret-underlined
// source excerpt, and prose.
package diag

import (
	"fmt"
	"io"
	"strings"

	"github.com/waj/fango/internal/source"
)

type Error struct {
	Title string // e.g. "TYPE MISMATCH", "NAMING ERROR", "SYNTAX PROBLEM"
	Span  source.Span
	Body  string   // prose above the excerpt
	Notes []string // prose below the excerpt
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
//	<notes>
func Render(w io.Writer, errs []Error) {
	for i, e := range errs {
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
		for _, n := range e.Notes {
			fmt.Fprintf(w, "\n%s\n", n)
		}
	}
}

func Errorf(sp source.Span, title, format string, args ...any) Error {
	return Error{Title: title, Span: sp, Body: fmt.Sprintf(format, args...)}
}
