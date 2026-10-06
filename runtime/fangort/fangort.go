// Package fangort is the Fango runtime support package. It is embedded into
// the compiler binary and materialized into every build directory; generated
// code imports it, and the interpreter and REPL import it directly — one
// shared formatting implementation across both backends, by construction.
package fangort

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Unit is the shared represented form of Fango's Unit type. Direct concrete
// worker and operation boundaries erase Unit, but package boundaries that
// carry first-class functions, polymorphic values, or ADTs need one nominal
// Go type shared by every generated package.
type Unit struct{}

var UnitValue Unit

func RequireValidString(name, s string) string {
	if !utf8.ValidString(s) {
		panic("native " + name + " returned invalid UTF-8")
	}
	return s
}

func RequireValidChar(name string, r rune) rune {
	// The unsigned comparisons are utf8.ValidRune, written so that the check
	// stays within Go's inlining budget for the small callers it guards.
	if uint32(r) > utf8.MaxRune || uint32(r)-0xD800 < 0x800 {
		panic("native " + name + " returned invalid Char")
	}
	return r
}

// ShowInt renders an Int exactly as the surface language shows it.
func ShowInt(v int64) string { return strconv.FormatInt(v, 10) }

// ShowFloat renders a Float with ECMA-262 Number::toString(10) semantics —
// what Elm's String.fromFloat produces: shortest round-trip decimal,
// integral floats without ".0", exponent notation only outside the
// [1e-6, 1e21) band with unpadded exponents, "Infinity"/"NaN" specials,
// and both zeros as "0".
func ShowFloat(f float64) string {
	var scratch [32]byte
	return string(AppendShowFloat(scratch[:0], f))
}

// AppendShowFloat shares exactly the ShowFloat spelling without allocating
// intermediate strings. The shortest-round-trip digits fit in stack storage.
func AppendShowFloat(dst []byte, f float64) []byte {
	switch {
	case math.IsNaN(f):
		return append(dst, "NaN"...)
	case math.IsInf(f, 1):
		return append(dst, "Infinity"...)
	case math.IsInf(f, -1):
		return append(dst, "-Infinity"...)
	case f == 0:
		return append(dst, '0')
	}
	if f < 0 {
		dst = append(dst, '-')
		f = -f
	}
	var scratch [32]byte
	scientific := strconv.AppendFloat(scratch[:0], f, 'e', -1, 64)
	split := 0
	for scientific[split] != 'e' {
		split++
	}
	exp := 0
	for _, c := range scientific[split+2:] {
		exp = exp*10 + int(c-'0')
	}
	if scientific[split+1] == '-' {
		exp = -exp
	}
	digits := scientific[:0]
	for _, c := range scientific[:split] {
		if c != '.' {
			digits = append(digits, c)
		}
	}
	n, k := exp+1, len(digits)
	switch {
	case k <= n && n <= 21:
		dst = append(dst, digits...)
		for i := k; i < n; i++ {
			dst = append(dst, '0')
		}
	case 0 < n && n <= 21:
		dst = append(dst, digits[:n]...)
		dst = append(dst, '.')
		dst = append(dst, digits[n:]...)
	case -6 < n && n <= 0:
		dst = append(dst, '0', '.')
		for i := 0; i < -n; i++ {
			dst = append(dst, '0')
		}
		dst = append(dst, digits...)
	default:
		dst = append(dst, digits[0])
		if k > 1 {
			dst = append(dst, '.')
			dst = append(dst, digits[1:]...)
		}
		dst = append(dst, 'e')
		if exp >= 0 {
			dst = append(dst, '+')
		}
		dst = strconv.AppendInt(dst, int64(exp), 10)
	}
	return dst
}

// ShowString is the raw rendering (what print outputs). It exists so that
// all formatting, even trivial, lives in fangort.
func ShowString(s string) string { return s }

func ShowChar(r rune) string { return string(r) }

func ShowCharLiteral(r rune) string {
	s := ShowStringLiteral(string(r))
	inside := strings.ReplaceAll(s[1:len(s)-1], `'`, `\'`)
	return "'" + inside + "'"
}

// ShowStringLiteral renders a String as a Fango source literal — the REPL's
// at-the-prompt form. Escapes: \\ \" \n \t \r \#{; other control characters as
// \u{XXXX}; everything else (including non-ASCII) passes through.
func ShowStringLiteral(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for i, r := range s {
		switch r {
		case '#':
			if i+1 < len(s) && s[i+1] == '{' {
				b.WriteByte('\\')
			}
			b.WriteRune(r)
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\u{%04X}`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

func ShowBool(v bool) string {
	if v {
		return "True"
	}
	return "False"
}

// PrintString writes a program's value-style main, already rendered through
// Display, and a newline to stdout.
func PrintString(v string) { fmt.Println(v) }
