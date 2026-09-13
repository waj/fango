package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/waj/fango/internal/codegen"
)

// A fallible native lowers to straight-line Go at its call site: the Go error
// is classified through fangort and both Result constructors are built like
// ordinary literals, while a wrapper type is projected to its scalar on the
// way in and rebuilt on the way out. No panic, defer, or recover takes part.
func TestFallibleNativeEmitsStraightLineGo(t *testing.T) {
	t.Parallel()
	files := emittedProject(t, filepath.Join("..", "..", "testdata", "run", "file_errors.fango"))
	var file *codegen.File
	for i := range files {
		if bytes.Contains(files[i].Data, []byte("n_File.OpenRead(")) {
			file = &files[i]
		}
	}
	if file == nil {
		t.Fatal("no generated package calls the File sidecar")
	}
	src := string(file.Data)
	for _, want := range []string{
		"t_payload, t_err := n_File.OpenRead(",
		"var t_failure fangort.IOFailure = fangort.ClassifyIOError(t_err)",
		"t_failure.Path, t_failure.Message",
		".(*C_File_dot_Handle).F0",
		"&C_File_dot_Handle{t_payload}",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("%s is missing %q", file.Path, want)
		}
	}
	for _, reject := range []string{"panic(", "defer ", "recover("} {
		if strings.Contains(src, reject) {
			t.Errorf("%s contains %q:\n%s", file.Path, reject, src)
		}
	}
}
