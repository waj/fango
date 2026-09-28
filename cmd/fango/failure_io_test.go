package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/waj/fango/internal/build"
	"github.com/waj/fango/internal/eval"
	"github.com/waj/fango/internal/nativehost"
)

// Inject failures into the bundled File sidecar copies used by this test.
// Both backends retain the real File wrappers, classification, resource
// scopes, and native boundary. Production sources have no fault switch.
func TestFileWriteAndCloseFailureReport(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "Main.fango")
	source := `import Fail
import Failure
import File
import IO
import Result exposing (Result(..))

main() =
    case Fail.attemptReport ({ _ -> File.withOutput "output.txt" ({ file -> File.write file "data" }) }) of
        Err report ->
            print (IO.describeError report.primary)
            List.each ({ failure ->
                error : Maybe IO.Error
                error = Failure.argument 0 failure
                case error of
                    Just value -> print (IO.describeError value)
                    Nothing -> print "missing IO.Error" }) report.suppressed
        Ok _ -> print "unexpected success"
`
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	inject := func(content []byte) []byte {
		t.Helper()
		text := string(content)
		for before, after := range map[string]string{
			"h.file.WriteString(text)": "func() (int, error) { return 0, &fs.PathError{Op: \"write\", Path: h.path, Err: errors.New(\"injected write failure\")} }()",
			"h.file.Close()":           "func() error { if err := h.file.Close(); err != nil { return err }; return &fs.PathError{Op: \"close\", Path: h.path, Err: errors.New(\"injected close failure\")} }()",
		} {
			if strings.Count(text, before) != 1 {
				t.Fatalf("File injection point %q changed", before)
			}
			text = strings.Replace(text, before, after, 1)
		}
		return []byte(text)
	}
	var diagnostics bytes.Buffer
	p, _, _, _, sources, ok := compileFileGraph(path, &diagnostics)
	if !ok {
		t.Fatal(diagnostics.String())
	}
	var workerSources []nativehost.Source
	for _, source := range sources {
		content := source.Content
		if source.Module == "File" {
			content = inject(content)
		}
		workerSources = append(workerSources, nativehost.Source{Module: source.Module, Content: content})
	}
	executor, err := nativehost.New(workerSources)
	if err != nil {
		t.Fatal(err)
	}
	defer executor.Close()
	env := eval.NewEnv()
	env.DefineProg(p)
	var interpreted bytes.Buffer
	ioctx := eval.NewIOContext(strings.NewReader(""), &interpreted)
	ioctx.Dir, ioctx.Natives = t.TempDir(), executor
	if _, err := eval.ForceIO(context.Background(), "main", env, ioctx); err != nil {
		t.Fatal(err)
	}
	want := "output.txt: injected write failure\noutput.txt: injected close failure\n"
	if interpreted.String() != want {
		t.Fatalf("interpreter: %q, want %q", interpreted.String(), want)
	}
	if testing.Short() {
		return
	}
	files := emittedProject(t, path)
	for i := range files {
		if files[i].Path == "native/File/native.go" {
			files[i].Data = inject(files[i].Data)
		}
	}
	dir := t.TempDir()
	fixed, err := build.RuntimeFiles()
	if err != nil {
		t.Fatal(err)
	}
	program := strings.TrimSuffix(filepath.Base(path), ".fango")
	if _, err := build.SyncGenerated(dir, program, append(files, fixed...)); err != nil {
		t.Fatal(err)
	}
	if err := build.GoBuild(dir, program); err != nil {
		t.Fatal(err)
	}
	compiled, status := runCompiled(t, exec.Command(build.BinaryPath(dir, program)), "", t.TempDir())
	if status != 0 || compiled != want {
		t.Fatalf("compiled: status %d, %q, want %q", status, compiled, want)
	}
}
