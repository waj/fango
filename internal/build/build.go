// Package build owns the persistent build directory: a private Go module
// containing one package per Fango module and a materialized copy of fangort.
// Every write goes through WriteIfChanged so the Go build cache and our own
// skip-build check stay effective.
package build

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	fango "github.com/waj/fango"
	"github.com/waj/fango/internal/codegen"
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
const generatedManifestName = ".fango-generated.json"

type generatedManifest struct {
	Files []string `json:"files"`
}

// ValidateExportDir accepts a missing, empty, or previously Fango-managed
// directory. It refuses to adopt a non-empty arbitrary directory because
// project synchronization removes stale generated files recorded in its
// manifest.
func ValidateExportDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil
	}
	if _, err := os.Stat(filepath.Join(dir, generatedManifestName)); err == nil {
		return nil
	}
	return fmt.Errorf("refusing to replace non-empty directory %s because it is not a Fango-generated project", dir)
}

// SyncGenerated writes package sources and removes only stale files listed by
// an earlier generated manifest. It never recursively removes an unmanaged
// path.
func SyncGenerated(dir string, files []codegen.File) (changed bool, err error) {
	manifestPath := filepath.Join(dir, generatedManifestName)
	var old generatedManifest
	if data, readErr := os.ReadFile(manifestPath); readErr == nil {
		if err := json.Unmarshal(data, &old); err != nil {
			return false, fmt.Errorf("read generated manifest: %w", err)
		}
	}

	wanted := make(map[string]bool, len(files))
	paths := make([]string, 0, len(files))
	for _, file := range files {
		rel := filepath.Clean(filepath.FromSlash(file.Path))
		if !validGeneratedSourcePath(rel) {
			return false, fmt.Errorf("invalid generated path %q", file.Path)
		}
		wanted[filepath.ToSlash(rel)] = true
		paths = append(paths, filepath.ToSlash(rel))
		wrote, writeErr := WriteIfChanged(filepath.Join(dir, rel), file.Data)
		if writeErr != nil {
			return changed, writeErr
		}
		changed = changed || wrote
	}
	sort.Strings(paths)
	for _, oldPath := range old.Files {
		if wanted[oldPath] {
			continue
		}
		rel := filepath.Clean(filepath.FromSlash(oldPath))
		if !validGeneratedSourcePath(rel) {
			return changed, fmt.Errorf("invalid path %q in generated manifest", oldPath)
		}
		full := filepath.Join(dir, rel)
		if removeErr := os.Remove(full); removeErr != nil && !os.IsNotExist(removeErr) {
			return changed, removeErr
		}
		changed = true
		for parent := filepath.Dir(full); parent != dir; parent = filepath.Dir(parent) {
			if removeErr := os.Remove(parent); removeErr != nil {
				break
			}
		}
	}
	data, err := json.MarshalIndent(generatedManifest{Files: paths}, "", "  ")
	if err != nil {
		return changed, err
	}
	data = append(data, '\n')
	wrote, err := WriteIfChanged(manifestPath, data)
	return changed || wrote, err
}

func validGeneratedSourcePath(rel string) bool {
	if rel == "." || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	slash := filepath.ToSlash(rel)
	return slash == "main.go" || strings.HasPrefix(slash, "modules/") && strings.HasSuffix(slash, "/module.go") ||
		strings.HasPrefix(slash, "native/") && strings.HasSuffix(slash, "/native.go")
}

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
		if _, nativeErr := os.Stat(filepath.Join(dir, "native")); nativeErr == nil {
			return fmt.Errorf(`-- NATIVE GO BUILD ERROR ---------------------------------------

Go rejected the generated project containing a user native sidecar. Check the
sidecar body and its standard-library imports. The build directory is preserved:

    %s

go build said:

%s`, dir, out)
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
