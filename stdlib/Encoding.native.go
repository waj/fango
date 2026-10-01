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
