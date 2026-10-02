package native

import (
	"sync"
	"unicode/utf8"
)

// textBuffer is shared by successive Text.Builder versions. A version is the
// buffer plus its own byte length; bytes below any version's length are never
// rewritten, so a version's text is stable however the buffer grows. The lock
// orders appends and reads when tasks share versions.
type textBuffer struct {
	mu   sync.Mutex
	data []byte
}

func NewBuffer(text string) any {
	return &textBuffer{data: append(make([]byte, 0, max(64, len(text))), text...)}
}

// ExtendBuffer appends in place when length is the buffer's newest version,
// and otherwise copies that version's prefix into a fresh buffer.
func ExtendBuffer(buffer any, length int64, text string) any {
	b := buffer.(*textBuffer)
	b.mu.Lock()
	if int64(len(b.data)) == length {
		b.data = append(b.data, text...)
		b.mu.Unlock()
		return b
	}
	data := make([]byte, length, max(64, int(length)+len(text)))
	copy(data, b.data[:length])
	b.mu.Unlock()
	return &textBuffer{data: append(data, text...)}
}

func NewBufferChar(char rune) any {
	return &textBuffer{data: utf8.AppendRune(make([]byte, 0, 64), char)}
}

func ExtendBufferChar(buffer any, length int64, char rune) any {
	b := buffer.(*textBuffer)
	b.mu.Lock()
	if int64(len(b.data)) == length {
		b.data = utf8.AppendRune(b.data, char)
		b.mu.Unlock()
		return b
	}
	data := make([]byte, length, max(64, int(length)+utf8.UTFMax))
	copy(data, b.data[:length])
	b.mu.Unlock()
	return &textBuffer{data: utf8.AppendRune(data, char)}
}

func BufferText(buffer any, length int64) string {
	b := buffer.(*textBuffer)
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.data[:length])
}
