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
    case Fail.attemptReport ({ _ -> File.withOutput "output.txt" ({ file -> IO.write file "data" }) }) of
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
	inject := func(module string, content []byte) []byte {
		t.Helper()
		text := string(content)
		var before, after string
		switch module {
		case "IO":
			text = strings.Replace(text, "import \"strings\"", "import (\"strings\"; \"errors\"; \"io/fs\")", 1)
			before = "return FangoWriteIOHandleBytes(FangoHost, value, []byte(text))"
			after = "if text == \"data\" { return &fs.PathError{Op: \"write\", Path: \"output.txt\", Err: errors.New(\"injected write failure\")} }; return FangoWriteIOHandleBytes(FangoHost, value, []byte(text))"
		case "File":
			before = "return FangoCloseIOHandle(value)"
			after = "if err := FangoCloseIOHandle(value); err != nil { return err }; return &fs.PathError{Op: \"close\", Path: \"output.txt\", Err: errors.New(\"injected close failure\")}"
		default:
			return content
		}
		if strings.Count(text, before) != 1 {
			t.Fatalf("%s injection point %q changed", module, before)
		}
		return []byte(strings.Replace(text, before, after, 1))
	}
	var diagnostics bytes.Buffer
	p, _, _, _, sources, ok := compileFileGraph(path, &diagnostics)
	if !ok {
		t.Fatal(diagnostics.String())
	}
	var workerSources []nativehost.Source
	for _, source := range sources {
		content := source.Content
		content = inject(source.Module, content)
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
		for _, module := range []string{"IO", "File"} {
			if files[i].Path == "native/"+module+"/native.go" {
				files[i].Data = inject(module, files[i].Data)
			}
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
