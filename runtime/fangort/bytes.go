package fangort

import (
	"bytes"
	"strings"
	"unicode/utf8"
)

// The runtime representation of the bundled Fango `Bytes` type (see
// doc/design/backend.md, "Bytes representation"). `Bytes` is an ordinary
// nominal type to the checker, the deriver, and reflection; only the backends
// know it is stored this way, exactly as `List` is.
//
// The alias rather than a defined type is deliberate: a native reading from a
// file or a socket hands the boundary an ordinary `[]byte`.
//
// Immutability is the invariant the whole layer rests on. Nothing writes to a
// slice's backing array after it becomes a Bytes, so BytesSlice can share
// storage and cost nothing. Every function here that builds a value allocates
// its own array and copies into it; in particular none of them appends to an
// argument, because append may write into spare capacity a shorter Bytes is
// still looking at. A native that reads into a scratch buffer owes the same
// copy on the way out.
type Bytes = []byte

func BytesEmpty() Bytes { return nil }

func BytesLength(b Bytes) int64 { return int64(len(b)) }

// BytesByteAt returns the byte at a 0-based index, or -1 when it is out of
// range, matching String.byteAt's private contract.
func BytesByteAt(i int64, b Bytes) int64 {
	if i < 0 || i >= int64(len(b)) {
		return -1
	}
	return int64(b[i])
}

// BytesSlice uses clamped half-open byte indices and shares storage. The
// three-index form caps the result, so an append to it can never write into
// the bytes that follow.
func BytesSlice(start, end int64, b Bytes) Bytes {
	size := int64(len(b))
	start = clampIndex(start, size)
	end = clampIndex(end, size)
	if end <= start {
		return nil
	}
	return b[start:end:end]
}

func clampIndex(i, size int64) int64 {
	if i < 0 {
		return 0
	}
	if i > size {
		return size
	}
	return i
}

// BytesAppend allocates exactly the joined length. It never appends to a,
// whose spare capacity may belong to a longer value sharing its array.
func BytesAppend(a, b Bytes) Bytes {
	if len(a) == 0 {
		return b
	}
	if len(b) == 0 {
		return a
	}
	out := make([]byte, len(a)+len(b))
	copy(out, a)
	copy(out[len(a):], b)
	return out
}

// BytesIndexOf answers the first index at which needle occurs in b, or -1.
// The scan is Go's, so a header block is searched without crossing the Fango
// boundary per byte.
func BytesIndexOf(needle, b Bytes) int64 { return int64(bytes.Index(b, needle)) }

// BytesIndexOfFrom resumes the scan at a clamped start, which is what keeps a
// growing-window delimiter search linear.
func BytesIndexOfFrom(from int64, needle, b Bytes) int64 {
	start := clampIndex(from, int64(len(b)))
	at := bytes.Index(b[start:], needle)
	if at < 0 {
		return -1
	}
	return start + int64(at)
}

func BytesStartsWith(prefix, b Bytes) bool { return bytes.HasPrefix(b, prefix) }

func BytesFromString(s string) Bytes { return []byte(s) }

func BytesIsUtf8(b Bytes) bool { return utf8.Valid(b) }

// BytesUnvalidatedString is reached only behind BytesIsUtf8; Bytes.toString
// pairs them and Bytes.toStringLossy substitutes instead.
func BytesUnvalidatedString(b Bytes) string { return string(b) }

// BytesToStringLossy substitutes U+FFFD for each malformed byte, the same
// replacement File.read performs.
func BytesToStringLossy(b Bytes) string {
	if utf8.Valid(b) {
		return string(b)
	}
	var out strings.Builder
	out.Grow(len(b))
	for i := 0; i < len(b); {
		r, size := utf8.DecodeRune(b[i:])
		if r == utf8.RuneError && size <= 1 {
			out.WriteRune(utf8.RuneError)
			i++
			continue
		}
		out.Write(b[i : i+size])
		i += size
	}
	return out.String()
}

func BytesEq(a, b Bytes) bool { return bytes.Equal(a, b) }

// Ordering is lexicographic on unsigned byte values, with a prefix ordering
// before its extensions.
func BytesLt(a, b Bytes) bool { return bytes.Compare(a, b) < 0 }
func BytesGt(a, b Bytes) bool { return bytes.Compare(a, b) > 0 }
func BytesLe(a, b Bytes) bool { return bytes.Compare(a, b) <= 0 }
func BytesGe(a, b Bytes) bool { return bytes.Compare(a, b) >= 0 }

// BytesShow renders a Bytes as a quoted escaped form. Printable ASCII passes
// through, the five escapes String literals use are spelled the same way, and
// every other byte is `\xNN` with uppercase hex — a byte is not a scalar, so
// String's `\u{XXXX}` would be a lie. There is no Bytes literal syntax, so
// this is a display form rather than something the lexer reads back.
func BytesShow(b Bytes) string {
	var out strings.Builder
	out.WriteByte('"')
	for _, c := range b {
		switch c {
		case '\\':
			out.WriteString(`\\`)
		case '"':
			out.WriteString(`\"`)
		case '\n':
			out.WriteString(`\n`)
		case '\t':
			out.WriteString(`\t`)
		case '\r':
			out.WriteString(`\r`)
		default:
			if c < 0x20 || c > 0x7E {
				out.WriteString(`\x`)
				out.WriteByte(hexDigits[c>>4])
				out.WriteByte(hexDigits[c&0x0F])
			} else {
				out.WriteByte(c)
			}
		}
	}
	out.WriteByte('"')
	return out.String()
}

const hexDigits = "0123456789ABCDEF"

// The list-crossing operations come in pairs: generated code holds a
// `List[int64]` or a `List[Bytes]`, and the interpreter holds the same list
// with its elements erased to `any`. Both spellings delegate to one
// implementation so the two backends cannot drift.

func BytesFromList(l List[int64]) Bytes {
	return bytesFromList(l, func(v int64) byte { return byte(v) })
}

func BytesFromValueList(l List[any]) Bytes {
	return bytesFromList(l, func(v any) byte { return byte(v.(int64)) })
}

// bytesFromList truncates each element to its low eight bits, which is what
// makes Bytes.fromList total over Int.
func bytesFromList[T any](l List[T], code func(T) byte) Bytes {
	var out []byte
	for ; !l.IsEmpty(); l = l.Tail() {
		out = append(out, code(l.Head()))
	}
	return out
}

func BytesToList(b Bytes) List[int64] {
	return bytesToList(b, func(v int64) int64 { return v })
}

func BytesToValueList(b Bytes) List[any] {
	return bytesToList(b, func(v int64) any { return v })
}

func bytesToList[T any](b Bytes, code func(int64) T) List[T] {
	out := ListNil[T]()
	for i := len(b) - 1; i >= 0; i-- {
		out = ListCons(code(int64(b[i])), out)
	}
	return out
}

func BytesConcat(parts List[Bytes]) Bytes {
	return bytesConcat(parts, func(v Bytes) Bytes { return v })
}

func BytesConcatValues(parts List[any]) Bytes {
	return bytesConcat(parts, func(v any) Bytes { return v.(Bytes) })
}

// bytesConcat sizes the result once and copies each part in, so joining n
// parts allocates once rather than n times.
func bytesConcat[T any](parts List[T], code func(T) Bytes) Bytes {
	size := 0
	for rest := parts; !rest.IsEmpty(); rest = rest.Tail() {
		size += len(code(rest.Head()))
	}
	if size == 0 {
		return nil
	}
	out := make([]byte, size)
	at := 0
	for rest := parts; !rest.IsEmpty(); rest = rest.Tail() {
		at += copy(out[at:], code(rest.Head()))
	}
	return out
}
