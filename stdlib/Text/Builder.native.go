package native

import (
	"math"
	"strconv"
	"strings"
	"sync/atomic"
	"unicode/utf8"
)

// A textBuffer's slice header never changes. frontier reserves writable tail
// space; bytes below any published Builder length are immutable. A successful
// reservation is written before its new Builder version can escape. Other
// versions can only read/copy their already-published prefixes or branch into
// fresh storage, so advancing frontier before writing does not publish bytes.
type textBuffer struct {
	data     []byte
	frontier atomic.Int64
}

func EmptyBuffer() any { return nil }
func Zero() int64      { return 0 }

func reserveText(buffer any, length int64, extra int) (*textBuffer, []byte) {
	end := int(length) + extra
	capacity := 64
	if buffer != nil {
		b := buffer.(*textBuffer)
		if end <= len(b.data) && b.frontier.CompareAndSwap(length, int64(end)) {
			return b, b.data[int(length):end]
		}
		if end > len(b.data) {
			capacity = max(capacity, len(b.data)*2)
		}
	}
	b := &textBuffer{data: make([]byte, max(capacity, end))}
	if buffer != nil {
		copy(b.data, buffer.(*textBuffer).data[:length])
	}
	b.frontier.Store(int64(end))
	return b, b.data[int(length):end]
}

func BufferAppend(buffer any, length int64, text string) any {
	if text == "" {
		return buffer
	}
	b, tail := reserveText(buffer, length, len(text))
	copy(tail, text)
	return b
}

func BufferAppendChar(buffer any, length int64, char rune) any {
	b, tail := reserveText(buffer, length, utf8.RuneLen(char))
	utf8.EncodeRune(tail, char)
	return b
}

func BufferAppendInt(buffer any, length int64, value int64) any {
	var scratch [20]byte
	digits := strconv.AppendInt(scratch[:0], value, 10)
	b, tail := reserveText(buffer, length, len(digits))
	copy(tail, digits)
	return b
}

func BufferAppendFloat(buffer any, length int64, value float64) any {
	var scratch [32]byte
	digits := appendFloat(scratch[:0], value)
	b, tail := reserveText(buffer, length, len(digits))
	copy(tail, digits)
	return b
}

// BufferLength is only called while constructing a new, nonempty Builder
// version, before it escapes. No other caller can yet possess its frontier
// length and reserve a subsequent append. Old versions keep their own length.
func BufferLength(buffer any) int64 {
	return buffer.(*textBuffer).frontier.Load()
}

func BufferText(buffer any, length int64) string {
	if length == 0 {
		return ""
	}
	return string(buffer.(*textBuffer).data[:length])
}

func IntToString(v int64) string { return strconv.FormatInt(v, 10) }
func CharToString(r rune) string { return string(r) }

// FloatToString renders a Float with ECMA-262 Number::toString(10) semantics —
// what Elm's String.fromFloat produces: shortest round-trip decimal,
// integral floats without ".0", exponent notation only outside the
// [1e-6, 1e21) band with unpadded exponents, "Infinity"/"NaN" specials,
// and both zeros as "0".
func FloatToString(f float64) string {
	var scratch [32]byte
	return string(appendFloat(scratch[:0], f))
}

// appendFloat shares exactly the FloatToString spelling without allocating
// intermediate strings. The shortest-round-trip digits fit in stack storage.
func appendFloat(dst []byte, f float64) []byte {
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

// Literal sizing and writing share one escape routine. Appends reserve exactly
// the escaped length and write into unpublished tail storage, without a
// temporary rendered string. Char preserves the existing double-quote escape.
func appendLiteralRune(dst []byte, r rune, brace, char bool) []byte {
	switch r {
	case '#':
		if brace {
			dst = append(dst, '\\')
		}
	case '\\', '"':
		dst = append(dst, '\\')
	case '\'':
		if char {
			dst = append(dst, '\\')
		}
	case '\n':
		return append(dst, '\\', 'n')
	case '\t':
		return append(dst, '\\', 't')
	case '\r':
		return append(dst, '\\', 'r')
	default:
		if r < 0x20 {
			const hex = "0123456789ABCDEF"
			return append(dst, '\\', 'u', '{', '0', '0', hex[r>>4], hex[r&15], '}')
		}
	}
	return utf8.AppendRune(dst, r)
}

func literalSize(s string, char bool) int {
	size := 2
	var scratch [10]byte
	for i, r := range s {
		size += len(appendLiteralRune(scratch[:0], r, i+1 < len(s) && s[i+1] == '{', char))
	}
	return size
}

func appendLiteral(dst []byte, s string, char bool) []byte {
	quote := byte('"')
	if char {
		quote = '\''
	}
	dst = append(dst, quote)
	for i, r := range s {
		dst = appendLiteralRune(dst, r, i+1 < len(s) && s[i+1] == '{', char)
	}
	return append(dst, quote)
}

func StringLiteral(s string) string {
	// A direct show needs only the final string allocation. Go's builder
	// transfers its storage to the string, unlike converting a byte slice.
	var out strings.Builder
	out.Grow(literalSize(s, false))
	out.WriteByte('"')
	var scratch [10]byte
	for i, r := range s {
		out.Write(appendLiteralRune(scratch[:0], r, i+1 < len(s) && s[i+1] == '{', false))
	}
	out.WriteByte('"')
	return out.String()
}

func CharLiteral(r rune) string {
	var scratch [10]byte
	return string(appendLiteral(scratch[:0], string(r), true))
}

func BufferAppendStringLiteral(buffer any, length int64, s string) any {
	b, tail := reserveText(buffer, length, literalSize(s, false))
	appendLiteral(tail[:0], s, false)
	return b
}

func BufferAppendCharLiteral(buffer any, length int64, r rune) any {
	s := string(r)
	b, tail := reserveText(buffer, length, literalSize(s, true))
	appendLiteral(tail[:0], s, true)
	return b
}
