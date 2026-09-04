// Package fangort is the fango runtime support package. It is embedded into
// the compiler binary and materialized into every build directory; generated
// code imports it, and the interpreter and REPL import it directly — one
// shared formatting implementation across both backends, by construction.
package fangort

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
)

// ReadLine reads one line without its line ending. EOF after data returns
// that final line; EOF before data is the empty string.
func ReadLineFrom(r *bufio.Reader) (string, error) {
	s, err := r.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	if len(s) > 0 && s[len(s)-1] == '\n' {
		s = s[:len(s)-1]
		if len(s) > 0 && s[len(s)-1] == '\r' {
			s = s[:len(s)-1]
		}
	}
	return s, nil
}

var stdin = bufio.NewReader(os.Stdin)

func ReadLine() string {
	s, err := ReadLineFrom(stdin)
	if err != nil {
		panic(err)
	}
	return s
}

// Number is the Go type-set constraint compiling fango's Number-kinded type
// variables (doc/design.md, "Type inference"): `double : number -> number` emits as
// `func v_double[A Number](x A) A` with native operators.
type Number interface{ ~int64 | ~float64 }

// ShowInt renders an Int exactly as the surface language shows it.
func ShowInt(v int64) string { return strconv.FormatInt(v, 10) }

// ShowFloat renders a Float with ECMA-262 Number::toString(10) semantics —
// what Elm's String.fromFloat produces: shortest round-trip decimal,
// integral floats without ".0", exponent notation only outside the
// [1e-6, 1e21) band with unpadded exponents, "Infinity"/"NaN" specials,
// and both zeros as "0".
func ShowFloat(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	case f == 0:
		return "0" // JS parity: String(-0) is "0"
	}
	neg := ""
	if f < 0 {
		neg = "-"
		f = -f
	}
	// Shortest round-trip digits and decimal exponent: value = 0.digits×10ⁿ.
	e := strconv.FormatFloat(f, 'e', -1, 64) // "d[.ddd]e±xx"
	mant, expStr, _ := strings.Cut(e, "e")
	digits := strings.Replace(mant, ".", "", 1)
	exp, _ := strconv.Atoi(expStr)
	n := exp + 1
	k := len(digits)

	switch {
	case k <= n && n <= 21: // integral: digits then n-k zeros
		return neg + digits + strings.Repeat("0", n-k)
	case 0 < n && n <= 21: // point inside the digits
		return neg + digits[:n] + "." + digits[n:]
	case -6 < n && n <= 0: // leading zeros after "0."
		return neg + "0." + strings.Repeat("0", -n) + digits
	default: // exponent form, unpadded, explicit sign
		s := digits[:1]
		if k > 1 {
			s += "." + digits[1:]
		}
		if n-1 >= 0 {
			return neg + s + "e+" + strconv.Itoa(n-1)
		}
		return neg + s + "e-" + strconv.Itoa(-(n - 1))
	}
}

// ShowString is the raw rendering (what print outputs). It exists so that
// all formatting, even trivial, lives in fangort.
func ShowString(s string) string { return s }

// ShowStringLiteral renders a String as a fango source literal — the REPL's
// at-the-prompt form. Escapes: \\ \" \n \t \r; other control characters as
// \u{XXXX}; everything else (including non-ASCII) passes through.
func ShowStringLiteral(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
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

func ShowUnit() string { return "()" }

// The Print* family writes ShowX(v) and a newline to stdout: the compiled
// backend's `print`, and (for non-Unit main) the test-internal print-main
// observation mode.
func PrintInt(v int64)     { fmt.Println(ShowInt(v)) }
func PrintFloat(v float64) { fmt.Println(ShowFloat(v)) }
func PrintString(v string) { fmt.Println(ShowString(v)) }
func PrintBool(v bool)     { fmt.Println(ShowBool(v)) }

// WriteStringTo writes a String verbatim without adding a line ending.
func WriteStringTo(w io.Writer, v string) error {
	_, err := io.WriteString(w, v)
	return err
}

// WriteString is the compiled backend implementation of IO.write.
func WriteString(v string) {
	if err := WriteStringTo(os.Stdout, v); err != nil {
		panic(err)
	}
}
