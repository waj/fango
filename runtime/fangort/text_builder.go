package fangort

import (
	"strconv"
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

func EmptyTextBuffer(_ Unit) any   { return nil }
func TextBuilderZero(_ Unit) int64 { return 0 }

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

func TextBufferAppend(buffer any, length int64, text string) any {
	if text == "" {
		return buffer
	}
	b, tail := reserveText(buffer, length, len(text))
	copy(tail, text)
	return b
}

func TextBufferAppendChar(buffer any, length int64, char rune) any {
	b, tail := reserveText(buffer, length, utf8.RuneLen(char))
	utf8.EncodeRune(tail, char)
	return b
}

func TextBufferAppendInt(buffer any, length int64, value int64) any {
	var scratch [20]byte
	digits := strconv.AppendInt(scratch[:0], value, 10)
	b, tail := reserveText(buffer, length, len(digits))
	copy(tail, digits)
	return b
}

func TextBufferAppendFloat(buffer any, length int64, value float64) any {
	var scratch [32]byte
	digits := AppendShowFloat(scratch[:0], value)
	b, tail := reserveText(buffer, length, len(digits))
	copy(tail, digits)
	return b
}

// TextBufferLength is only called while constructing a new, nonempty Builder
// version, before it escapes. No other caller can yet possess its frontier
// length and reserve a subsequent append. Old versions keep their own length.
func TextBufferLength(buffer any) int64 {
	return buffer.(*textBuffer).frontier.Load()
}

func TextBufferText(buffer any, length int64) string {
	if length == 0 {
		return ""
	}
	return string(buffer.(*textBuffer).data[:length])
}
