// Package build owns the persistent build directory: a tiny Go module
// (`module fangobuild`, zero dependencies) holding the generated main.go and
// a materialized copy of fangort. Every write goes through WriteIfChanged so
// the Go build cache and our own skip-build check stay effective.
package build

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"

	fango "github.com/waj/fango"
)

// Dir returns (creating if needed) the build directory for an entry file:
// FANGO_BUILD_DIR if set, else .fango/build beside the entry file, else a
// per-path cache directory if the source tree is unwritable.
func Dir(entry string) (string, error) {
	if d := os.Getenv("FANGO_BUILD_DIR"); d != "" {
		return d, os.MkdirAll(d, 0o755)
	}
	abs, err := filepath.Abs(entry)
	if err != nil {
		return "", err
	}
	d := filepath.Join(filepath.Dir(abs), ".fango", "build")
	if err := os.MkdirAll(d, 0o755); err == nil {
		return d, nil
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	d = filepath.Join(cache, "fango", pathHash(abs))
	return d, os.MkdirAll(d, 0o755)
}

func pathHash(p string) string {
	var h uint64 = 14695981039346656037
	for i := 0; i < len(p); i++ {
		h ^= uint64(p[i])
		h *= 1099511628211
	}
	return fmt.Sprintf("%016x", h)
}

// WriteIfChanged writes data to path only when the content differs,
// reporting whether a write happened.
func WriteIfChanged(path string, data []byte) (bool, error) {
	old, err := os.ReadFile(path)
	if err == nil && bytes.Equal(old, data) {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	return true, os.WriteFile(path, data, 0o644)
}

const goModContent = "module fangobuild\n\ngo 1.26\n"

// Materialize ensures go.mod and the embedded fangort sources exist in dir,
// reporting whether anything changed.
func Materialize(dir string) (changed bool, err error) {
	w, err := WriteIfChanged(filepath.Join(dir, "go.mod"), []byte(goModContent))
	if err != nil {
		return false, err
	}
	changed = changed || w

	entries, err := fs.ReadDir(fango.FangortFS, "runtime/fangort")
	if err != nil {
		return changed, err
	}
	for _, e := range entries {
		data, err := fs.ReadFile(fango.FangortFS, "runtime/fangort/"+e.Name())
		if err != nil {
			return changed, err
		}
		w, err := WriteIfChanged(filepath.Join(dir, "fangort", e.Name()), data)
		if err != nil {
			return changed, err
		}
		changed = changed || w
	}
	return changed, nil
}

// BinaryPath is where GoBuild leaves the compiled program.
func BinaryPath(dir string) string { return filepath.Join(dir, "bin", "main") }

// GoBuild compiles the build directory. Any failure is by definition a fango
// compiler bug: the generated code is our output, so a Go error means we
// emitted something invalid. The build directory is preserved for inspection.
func GoBuild(dir string) error {
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		return err
	}
	cmd := exec.Command("go", "build", "-o", BinaryPath(dir), ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOPROXY=off", "GOTOOLCHAIN=local")
	out, err := cmd.CombinedOutput()
	if err != nil {
		if _, lookErr := exec.LookPath("go"); lookErr != nil {
			return fmt.Errorf("cannot find the `go` tool — enter the dev shell (`nix develop` or direnv) and retry")
		}
		return fmt.Errorf(`-- INTERNAL COMPILER ERROR ------------------------------------

fango generated Go code that Go refused to compile. This is a bug in
the fango compiler, not in your program — please report it.

The build directory is preserved for inspection:

    %s

go build said:

%s`, dir, out)
	}
	return nil
}

// RunBinary executes the compiled program with inherited stdio and returns
// its exit code.
func RunBinary(dir string) (int, error) {
	cmd := exec.Command(BinaryPath(dir))
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := cmd.Run()
	if err == nil {
		return 0, nil
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode(), nil
	}
	return -1, err
}

// Clean removes the .fango directory beside the entry file.
func Clean(entry string) error {
	abs, err := filepath.Abs(entry)
	if err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(filepath.Dir(abs), ".fango"))
}
