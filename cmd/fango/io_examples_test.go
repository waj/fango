package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/waj/fango/internal/eval"
	"github.com/waj/fango/internal/natives"
)

func TestFileExampleFailures(t *testing.T) {
	for _, tc := range []struct {
		name, example, store, want string
		args                       []string
		symlink                    bool
	}{
		{"csv directory", "csv", "input.csv", "input.csv: is a directory\n", []string{"input.csv"}, false},
		{"todo directory", "todo", "todo.json", "todo.json: is a directory\n", []string{"list"}, false},
		{"todo write failure", "todo", "todo.json", "todo.json: no such file or directory\n", []string{"add", "unsaved"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.symlink && runtime.GOOS == "windows" {
				t.Skip("symlink fixture needs Unix")
			}
			path := filepath.Join("..", "..", "examples", tc.example+".fango")
			prog, executor := compileWithWorker(t, path)
			seed := func() string {
				dir := t.TempDir()
				var err error
				if tc.symlink {
					// Reading this store is NotFound; saving fails because its
					// target's parent does not exist, independent of permissions.
					err = os.Symlink(filepath.Join("missing", "store.json"), filepath.Join(dir, tc.store))
				} else {
					err = os.Mkdir(filepath.Join(dir, tc.store), 0o755)
				}
				if err != nil {
					t.Fatal(err)
				}
				return dir
			}
			env := eval.NewEnv()
			env.DefineProg(prog)
			var output bytes.Buffer
			ioctx := eval.NewIOContext(strings.NewReader(""), &output)
			ioctx.Dir, ioctx.Args, ioctx.Natives = seed(), tc.args, executor
			_, err := eval.ForceIO(context.Background(), prog.Entry, env, ioctx)
			var exit *natives.ExitError
			if !errors.As(err, &exit) || exit.Code != 1 || output.String() != tc.want {
				t.Fatalf("interpreter = %v, %q", err, output.String())
			}
			if testing.Short() {
				return
			}
			cmd := exec.Command(cliCompiledBinary(t, path), tc.args...)
			var compiled, errorOutput bytes.Buffer
			cmd.Dir, cmd.Stdout, cmd.Stderr = seed(), &compiled, &errorOutput
			err = cmd.Run()
			var compiledExit *exec.ExitError
			if !errors.As(err, &compiledExit) || compiledExit.ExitCode() != 1 || compiled.String() != tc.want || errorOutput.Len() != 0 {
				t.Fatalf("compiled = %v, %q, stderr %q", err, compiled.String(), errorOutput.String())
			}
		})
	}
}
