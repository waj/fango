package native

import (
	"strings"

	"github.com/waj/fango/runtime/fangort"
)

// CaptureRaw walks the calling goroutine's stack and keeps the Fango frames
// outside this module, so the first frame is `capture`'s caller. Under the
// interpreter the stack holds no generated code and the batch is empty.
func CaptureRaw() any {
	frames := fangort.Capture(0)
	for len(frames) > 0 && (strings.HasPrefix(frames[0].Function, "Trace.") || strings.HasPrefix(frames[0].Function, "native Trace.")) {
		frames = frames[1:]
	}
	return frames
}

func FrameCount(batch any) int64                  { return int64(len(batch.([]fangort.Frame))) }
func FrameFunction(batch any, index int64) string { return batch.([]fangort.Frame)[index].Function }
func FrameFile(batch any, index int64) string     { return batch.([]fangort.Frame)[index].File }
func FrameLine(batch any, index int64) int64      { return int64(batch.([]fangort.Frame)[index].Line) }
