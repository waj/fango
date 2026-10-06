// Package fangort is the Fango runtime support package. It is embedded into
// the compiler binary and materialized into every build directory; generated
// code imports it, and the interpreter and REPL import it directly.
package fangort

import (
	"fmt"
	"unicode/utf8"
)

// Unit is the shared represented form of Fango's Unit type. Direct concrete
// worker and operation boundaries erase Unit, but package boundaries that
// carry first-class functions, polymorphic values, or ADTs need one nominal
// Go type shared by every generated package.
type Unit struct{}

var UnitValue Unit

func RequireValidChar(name string, r rune) rune {
	// The unsigned comparisons are utf8.ValidRune, written so that the check
	// stays within Go's inlining budget for the small callers it guards.
	if uint32(r) > utf8.MaxRune || uint32(r)-0xD800 < 0x800 {
		panic("native " + name + " returned invalid Char")
	}
	return r
}

// PrintString writes a program's value-style main, already rendered through
// Display, and a newline to stdout.
func PrintString(v string) { fmt.Println(v) }
