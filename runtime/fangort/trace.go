package fangort

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
)

// Frame is one Fango-level entry of a call stack: the function executing and
// the source line it was at, as the `//line` directives of the generated Go
// report them through the Go line table. A frame outside generated Fango
// code, such as a native sidecar, keeps its Go name and file.
type Frame struct {
	Function string
	File     string
	Line     int
}

// TracebackVariable selects Go's own traceback for a crash when set to "go",
// for debugging the compiler and runtime rather than the program.
const TracebackVariable = "FANGO_TRACEBACK"

const nativePackagePrefix = "fangobuild/native/"

// Capture walks the calling goroutine's stack and keeps the frames that
// belong to Fango code, innermost first, skipping the innermost `skip` Fango
// frames. Consecutive frames with the same function and line collapse into
// one, which hides the Go closures a single Fango expression lowers to.
func Capture(skip int) []Frame {
	var pcs [256]uintptr
	n := runtime.Callers(2, pcs[:])
	if n == 0 {
		return nil
	}
	frames := runtime.CallersFrames(pcs[:n])
	var out []Frame
	for {
		frame, more := frames.Next()
		if traced, ok := fangoFrame(frame); ok {
			if len(out) == 0 || out[len(out)-1] != traced {
				out = append(out, traced)
			}
		}
		if !more {
			break
		}
	}
	if skip >= len(out) {
		return nil
	}
	return out[skip:]
}

func fangoFrame(frame runtime.Frame) (Frame, bool) {
	if symbol, ok := Demangle(frame.Function); ok {
		return Frame{Function: symbol.String(), File: frame.File, Line: frame.Line}, true
	}
	if name, ok := strings.CutPrefix(frame.Function, nativePackagePrefix); ok {
		return Frame{Function: "native " + stripInstantiation(name), File: nativeFile(frame.File), Line: frame.Line}, true
	}
	return Frame{}, false
}

// nativeFile shortens a sidecar's path to its place in the generated module,
// `native/<module>/native.go`, since the build directory is an implementation
// detail.
func nativeFile(path string) string {
	if i := strings.LastIndex(path, "/native/"); i >= 0 {
		return path[i+1:]
	}
	return path
}

// FormatTrace renders frames one per line, innermost first:
//
//	at Main.divide (Main.fango:12)
func FormatTrace(frames []Frame) string {
	var b strings.Builder
	for _, frame := range frames {
		b.WriteString("  at ")
		b.WriteString(frame.Function)
		if frame.File != "" {
			b.WriteString(" (")
			b.WriteString(frame.File)
			b.WriteByte(':')
			b.WriteString(strconv.Itoa(frame.Line))
			b.WriteByte(')')
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// goTraceback reports whether the program asked for Go's own traceback.
func goTraceback() bool { return os.Getenv(TracebackVariable) == "go" }

// RecoverMain is deferred first in every generated main. A panic reaching it
// ends the program the way an unrecovered panic does, with status 2, but
// reports the Fango stack instead of Go's goroutine dump.
func RecoverMain() {
	if goTraceback() {
		return
	}
	if recovered := recover(); recovered != nil {
		crash(recovered, Capture(0), nil)
	}
}

// recoverTask is deferred in the goroutines the runtime starts for tasks. A
// panic there is a program crash, as it is without recovery; the report adds
// where the task was spawned.
func recoverTask(spawned []Frame) {
	if goTraceback() {
		return
	}
	if recovered := recover(); recovered != nil {
		crash(recovered, Capture(0), spawned)
	}
}

// crash prints the panic and the Fango trace to stderr and exits with Go's
// panic status. A trace with no Fango frames means the panic came from
// runtime initialization, where only Go's own report can help.
func crash(recovered any, frames, spawned []Frame) {
	if len(frames) == 0 {
		panic(recovered)
	}
	var b strings.Builder
	b.WriteString("panic: ")
	b.WriteString(panicMessage(recovered))
	b.WriteString("\n\nFango trace:\n")
	b.WriteString(FormatTrace(frames))
	if len(spawned) != 0 {
		b.WriteString("spawned from:\n")
		b.WriteString(FormatTrace(spawned))
	}
	os.Stderr.WriteString(b.String())
	os.Exit(2)
}

func panicMessage(recovered any) string {
	switch v := recovered.(type) {
	case error:
		return v.Error()
	case string:
		return v
	default:
		return fmt.Sprint(v)
	}
}
