package fangort

import (
	"runtime"
	"strings"
	"testing"
)

//go:noinline
func retainedSpawnSite() ([]uintptr, int) {
	_, _, line, _ := runtime.Caller(0)
	return captureStack(), line + 1
}

func TestSpawnStackSurvivesParentReturn(t *testing.T) {
	pcs, line := retainedSpawnSite()
	// Another capture and a collection must not overwrite or invalidate the
	// retained PCs. Symbolization happens after the capturing frame returns.
	_ = captureStack()
	runtime.GC()
	frame, _ := runtime.CallersFrames(pcs).Next()
	if !strings.HasSuffix(frame.Function, ".retainedSpawnSite") || frame.Line != line {
		t.Fatalf("spawn site changed: %s:%d, want retainedSpawnSite:%d", frame.Function, frame.Line, line)
	}
}
