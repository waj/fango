package debugui

import (
	"fmt"
	"io"
	"strings"

	"github.com/waj/fango/internal/debugger"
	"github.com/waj/fango/internal/source"
)

type Precision uint8

const (
	LineOnly Precision = iota
	Exact
	Enclosing
)

// Location carries a snapshot and an explicit precision, not a guessed column.
type Location struct {
	File, Text               string
	Line, Column, Start, End int
	Precision                Precision
}

func (l Location) String() string {
	if l.Column > 0 {
		return fmt.Sprintf("%s:%d:%d", l.File, l.Line, l.Column)
	}
	return fmt.Sprintf("%s:%d", l.File, l.Line)
}
func Excerpt(w io.Writer, l Location) {
	if l.Precision != LineOnly && l.Start >= 0 && l.End > l.Start && l.End <= len(l.Text) {
		if l.Precision == Enclosing {
			fmt.Fprintln(w, "  (enclosing expression; multiple native sites)")
		}
		file := source.NewFile(l.File, []byte(l.Text))
		fmt.Fprintln(w, file.Excerpt(source.Span{File: file, Start: l.Start, End: l.End}))
		return
	}
	lines := strings.Split(l.Text, "\n")
	if l.Line > 0 && l.Line <= len(lines) {
		fmt.Fprintf(w, "  %s\n", lines[l.Line-1])
	}
	fmt.Fprintln(w, "  (expression span unavailable at this location)")
}
func Frame(w io.Writer, index int, name string, l Location) {
	fmt.Fprintf(w, "#%d %s at %s\n", index, name, l)
}
func Variable(w io.Writer, v debugger.Variable) {
	fmt.Fprint(w, v.Name)
	if v.Type != "" {
		fmt.Fprintf(w, " : %s", v.Type)
	}
	fmt.Fprintf(w, " = %s", v.Value)
	if v.Handle != 0 {
		fmt.Fprintf(w, " [handle %d]", v.Handle)
	}
	fmt.Fprintln(w)
}
func Breakpoint(w io.Writer, id int, file string, line int, status string) {
	fmt.Fprintf(w, "breakpoint %d: %s:%d (%s)\n", id, file, line, status)
}
func Error(w io.Writer, err error) { fmt.Fprintf(w, "debugger: %v\n", err) }
func Stopped(w io.Writer, reason string) {
	if reason == "" {
		reason = "paused"
	}
	fmt.Fprintln(w, "stopped: "+reason)
}
