package native

import "unicode/utf8"

// Utf8CodeAt distinguishes a valid U+FFFD from malformed input and rejects
// invalid prefixes immediately, even when a later refill could add bytes.
func Utf8CodeAt(bytes []byte, offset int64) int64 {
	if offset < 0 || offset >= int64(len(bytes)) {
		return -1
	}
	if first := bytes[offset]; first < utf8.RuneSelf {
		return int64(first)
	}
	remaining := bytes[offset:]
	if !utf8.FullRune(remaining) {
		return -3
	}
	char, size := utf8.DecodeRune(remaining)
	if char == utf8.RuneError && size == 1 {
		return -2
	}
	return int64(char)
}

// Utf8MatchAt reports whether text's UTF-8 bytes occur at offset. The
// comparison converts nothing, so it allocates nothing.
func Utf8MatchAt(bytes []byte, offset int64, text string) bool {
	if offset < 0 || offset > int64(len(bytes)) || int64(len(text)) > int64(len(bytes))-offset {
		return false
	}
	return string(bytes[offset:offset+int64(len(text))]) == text
}

// Utf8SpanUntil scans complete valid scalars from offset, stopping at an
// ASCII byte whose table entry is nonzero, a malformed or incomplete
// sequence, or the end. One pass both finds the run and validates it.
func Utf8SpanUntil(table []byte, bytes []byte, offset int64) int64 {
	if offset < 0 || len(table) < utf8.RuneSelf {
		return offset
	}
	i := int(offset)
	for i < len(bytes) {
		if first := bytes[i]; first < utf8.RuneSelf {
			if table[first] != 0 {
				break
			}
			i++
			continue
		}
		char, size := utf8.DecodeRune(bytes[i:])
		if char == utf8.RuneError && size == 1 {
			break
		}
		i += size
	}
	return int64(i)
}
