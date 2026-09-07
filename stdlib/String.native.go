package native

import "github.com/waj/fango/runtime/fangort"

// The interpreter implementations of the String templates delegate to
// fangort, the same functions the compiled templates call.

func StringLength(s string) int64 { return fangort.StringLength(s) }

func StringByteLength(s string) int64 { return fangort.StringByteLength(s) }

func ByteAt(i int64, s string) int64 { return fangort.ByteAt(i, s) }

func StringSlice(start, end int64, s string) string { return fangort.StringSlice(start, end, s) }

func StringByteSlice(start, end int64, s string) string {
	return fangort.StringByteSlice(start, end, s)
}

func StringFirst(s string) rune { return fangort.StringFirst(s) }

func StringRest(s string) string { return fangort.StringRest(s) }

func StringFromChar(r rune) string { return fangort.StringFromChar(r) }
