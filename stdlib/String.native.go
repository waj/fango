package native

import "github.com/waj/fango/runtime/fangort"

// The interpreter implementations of the String templates delegate to
// fangort, the same functions the compiled templates call.

func StringLength(s string) int64 { return fangort.StringLength(s) }

func ByteAt(i int64, s string) int64 { return fangort.ByteAt(i, s) }
