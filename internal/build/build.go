// Package build owns the persistent build directory: one private Go module
// holding a package per Fango module, a materialized copy of fangort, and a
// package per entry program beneath entries/. The directory belongs to a
// source directory rather than to one program, so the programs in it share the
// modules they have in common and keep their own entry package, binary, and
// manifest of what they generated. Every write goes through WriteIfChanged so
// the Go build cache stays effective, and each program's link is skipped from
// the stamp of what its binary was built from.
package build

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"

	"github.com/waj/fango/internal/codegen"
	"github.com/waj/fango/internal/runtimefiles"
)

var userCacheDir = os.UserCacheDir

// Dir returns (creating if needed) the build directory for an entry file:
// FANGO_BUILD_DIR if set, else .fango/build beside the entry file, else a
// cache directory keyed by the source root if the source tree is unwritable.
// Every entry in a directory resolves to the same build directory, as it does
// to the same compilation cache, because the programs there share the modules
// they have in common.
func Dir(entry string) (string, error) {
	if d := os.Getenv("FANGO_BUILD_DIR"); d != "" {
		return d, os.MkdirAll(d, 0o755)
	}
	abs, err := filepath.Abs(entry)
	if err != nil {
		return "", err
	}
	root := filepath.Dir(abs)
	d := filepath.Join(root, ".fango", "build")
	if err := os.MkdirAll(d, 0o755); err == nil {
		return d, nil
	}
	cache, err := userCacheDir()
	if err != nil {
		return "", err
	}
	d = filepath.Join(cache, "fango", "roots", pathHash(root), "build")
	return d, os.MkdirAll(d, 0o755)
}

// CacheDirs returns the preferred source-local compiler cache and its
// source-root-scoped fallback. It does not create either directory: reads must
// not mutate the source tree, and writers need to be able to try the fallback
// when an existing local cache is not writable.
func CacheDirs(entry string) (local, fallback string, err error) {
	abs, err := filepath.Abs(entry)
	if err != nil {
		return "", "", err
	}
	cache, err := userCacheDir()
	if err != nil {
		return filepath.Join(filepath.Dir(abs), ".fango", "cache"), "", nil
	}
	root := filepath.Dir(abs)
	return filepath.Join(root, ".fango", "cache"), filepath.Join(cache, "fango", "roots", pathHash(root), "cache"), nil
}

// LegacyCacheDir identifies the pre-M1 per-entry fallback so clean can remove
// it during the transition. New cache reads and writes do not use it.
func LegacyCacheDir(entry string) (string, error) {
	abs, err := filepath.Abs(entry)
	if err != nil {
		return "", err
	}
	cache, err := userCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cache, "fango", pathHash(abs), "cache"), nil
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

// generatedManifest records what the directory holds, in two parts because
// the two are pruned by different rules.
//
// Runtime is the private module's fixed sources, which every program compiles
// and every build regenerates in full, so each build replaces the list and a
// source the library root stopped shipping leaves with it. Programs is what
// each entry program generated; a build prunes only what its own program
// listed before and no sibling lists now, so the modules two programs share
// survive either one's build. Refcounting the runtime the same way would keep
// a dropped fangort file alive behind a stale sibling — and that file is in
// the package every generated module imports.
//
// Files is the pre-multi-program shape, read so that the root main.go and
// sources.json a single-program tree left behind are pruned on the first build
// after the change; nothing writes it.
type generatedManifest struct {
	Files    []string            `json:"files,omitempty"`
	Runtime  []string            `json:"runtime"`
	Programs map[string][]string `json:"programs"`
}

// stale is what a program's sync must remove: the runtime files that are gone,
// plus the paths it generated last time that it does not generate now and no
// sibling claims. The legacy flat list is attributed to no program, so what
// survives in it is pruned in full.
func (m generatedManifest) stale(program string, wanted map[string]bool) []string {
	claimed := make(map[string]bool)
	for name, paths := range m.Programs {
		if name == program {
			continue
		}
		for _, path := range paths {
			claimed[path] = true
		}
	}
	removing := make(map[string]bool)
	var out []string
	// Runtime paths are in no program's list, so a runtime source the library
	// root dropped is never held alive by a sibling that has not rebuilt.
	for _, path := range slices.Concat(m.Runtime, m.Programs[program], m.Files) {
		if wanted[path] || claimed[path] || removing[path] {
			continue
		}
		removing[path] = true
		out = append(out, path)
	}
	return out
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

// SyncGenerated writes every file one program in the directory is made of —
// its entry package, the modules and native sidecars it reaches, and the fixed
// [RuntimeFiles] — and removes only the stale paths the manifest attributes to
// that program alone. It never recursively removes an unmanaged path.
func SyncGenerated(dir, program string, files []codegen.File) (changed bool, err error) {
	manifestPath := filepath.Join(dir, generatedManifestName)
	var old generatedManifest
	if data, readErr := os.ReadFile(manifestPath); readErr == nil {
		if err := json.Unmarshal(data, &old); err != nil {
			return false, fmt.Errorf("read generated manifest: %w", err)
		}
	}

	wanted := make(map[string]bool, len(files))
	var owned, fixed []string
	for _, file := range files {
		rel := filepath.Clean(filepath.FromSlash(file.Path))
		if !validGeneratedSourcePath(rel) {
			return false, fmt.Errorf("invalid generated path %q", file.Path)
		}
		slash := filepath.ToSlash(rel)
		wanted[slash] = true
		if sharedRuntimePath(slash) {
			fixed = append(fixed, slash)
		} else {
			owned = append(owned, slash)
		}
		wrote, writeErr := WriteIfChanged(filepath.Join(dir, rel), file.Data)
		if writeErr != nil {
			return changed, writeErr
		}
		changed = changed || wrote
	}
	sort.Strings(owned)
	sort.Strings(fixed)
	for _, oldPath := range old.stale(program, wanted) {
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
	next := generatedManifest{Runtime: fixed, Programs: map[string][]string{}}
	for name, listed := range old.Programs {
		if name != program {
			next.Programs[name] = listed
		}
	}
	next.Programs[program] = owned
	data, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return changed, err
	}
	data = append(data, '\n')
	wrote, err := WriteIfChanged(manifestPath, data)
	return changed || wrote, err
}

// sharedRuntimePath reports whether a generated path is part of the private
// module itself rather than of one program: every build regenerates these in
// full, so they are pruned against the current build alone.
func sharedRuntimePath(slash string) bool {
	return slash == "go.mod" || strings.HasPrefix(slash, "fangort/")
}

// validGeneratedSourcePath is the shape of a path the driver may write and
// prune. It still admits a root main.go, which nothing emits any more, so that
// the one a single-program tree left behind can be pruned rather than
// rejected.
func validGeneratedSourcePath(rel string) bool {
	if rel == "." || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	slash := filepath.ToSlash(rel)
	return slash == "main.go" || slash == "go.mod" || slash == "sources.json" ||
		strings.HasPrefix(slash, "fangort/") && strings.HasSuffix(slash, ".go") ||
		strings.HasPrefix(slash, "entries/") && (strings.HasSuffix(slash, "/main.go") || strings.HasSuffix(slash, "/sources.json")) ||
		strings.HasPrefix(slash, "modules/") && strings.HasSuffix(slash, "/module.go") ||
		strings.HasPrefix(slash, "native/") && (strings.HasSuffix(slash, "/native.go") || strings.HasSuffix(slash, "/host.go"))
}

// RuntimeFiles are the private module's fixed sources: its go.mod and the
// fangort package read from the library root. They are generated output like
// every other file the driver writes and go through the same manifest, so a
// runtime source the library root stops shipping leaves the build directory
// rather than staying in the package every generated module imports.
func RuntimeFiles() ([]codegen.File, error) {
	sources, err := runtimefiles.Packages("fangort")
	if err != nil {
		return nil, err
	}
	files := make([]codegen.File, 0, len(sources)+1)
	files = append(files, codegen.File{Path: "go.mod", Data: []byte(goModContent)})
	for _, source := range sources {
		files = append(files, codegen.File{Path: source.Path, Data: source.Data})
	}
	return files, nil
}

// EntryDir is the directory one program owns, matching the path
// [codegen.UnitPath] gives its entry unit. Everything about a program that is
// not shared belongs here, so it is spelled once.
func EntryDir(program string) string { return "entries/" + codegen.EntryLinkName(program) }

// EntryPackage is the package pattern one program's entry occupies.
func EntryPackage(program string) string { return "./" + EntryDir(program) }

// BinaryPath is where GoBuild leaves one program. Each program in a directory
// has its own, so building a sibling does not overwrite it; the name is the
// one its package answers to, because a stem Go refuses as a path component
// has already been spelled another way there.
func BinaryPath(dir, program string) string {
	return filepath.Join(dir, "bin", codegen.EntryLinkName(program))
}

// GoBuild compiles one program in the build directory. Any failure is by
// definition a Fango compiler bug: the generated code is our output, so a Go
// error means we emitted something invalid. The build directory is preserved
// for inspection.
func GoBuild(dir, program string) error {
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		return err
	}
	return GoBuildPackages(dir, BinaryPath(dir, program), EntryPackage(program))
}

// Linked reports whether the binary for a program was produced from exactly
// the files it would compile now.
//
// A program cannot decide this from whether its own synchronization wrote
// anything, because the modules in a directory are shared: a sibling's build
// can update one and leave this program's binary stale while this program
// writes nothing. So the stamp records what the binary was linked from, and
// only the compiled inputs go into it — a source edit that produces the same
// Go produces the same binary and needs no link.
func Linked(dir, program string, files []codegen.File) bool {
	if _, err := os.Stat(BinaryPath(dir, program)); err != nil {
		return false
	}
	recorded, err := os.ReadFile(stampPath(dir, program))
	return err == nil && bytes.Equal(recorded, stamp(files))
}

// RecordLink stores the stamp [Linked] compares against. A stamp that cannot
// be written costs a needless link next time and nothing else.
func RecordLink(dir, program string, files []codegen.File) {
	_, _ = WriteIfChanged(stampPath(dir, program), stamp(files))
}

func stampPath(dir, program string) string {
	return filepath.Join(dir, "bin", codegen.EntryLinkName(program)+".stamp")
}

// stamp digests what the binary is a function of: the toolchain that links it
// and the files that toolchain reads, in path order. Generated files Go does
// not compile — sources.json — are left out, so recording what a build was
// made from never forces a link on its own.
func stamp(files []codegen.File) []byte {
	compiled := make([]codegen.File, 0, len(files))
	for _, file := range files {
		if strings.HasSuffix(file.Path, ".go") || file.Path == "go.mod" {
			compiled = append(compiled, file)
		}
	}
	sort.Slice(compiled, func(i, j int) bool { return compiled[i].Path < compiled[j].Path })
	h := sha256.New()
	fmt.Fprintf(h, "%s %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
	for _, file := range compiled {
		fmt.Fprintf(h, "%s %d\n", file.Path, len(file.Data))
		h.Write(file.Data)
	}
	return []byte(hex.EncodeToString(h.Sum(nil)) + "\n")
}

// GoBuildPackages runs one `go build` in dir for the given package patterns,
// writing to dest — a file for a single package, or an existing directory
// (named with a trailing separator) that receives one executable per package.
// The differential test harness uses it to build every fixture in a single
// invocation.
func GoBuildPackages(dir, dest string, patterns ...string) error {
	cmd := exec.Command("go", append([]string{"build", "-o", dest}, patterns...)...)
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

Fango generated Go code that Go refused to compile. This is a bug in the
Fango compiler, not in your program — please report it, unless you have
edited the library FANGO_ROOT points at, whose runtime support is compiled
into this project.

The build directory is preserved for inspection:

    %s

go build said:

%s`, dir, out)
	}
	return nil
}

// RunBinary executes one compiled program with inherited stdio and returns
// its exit code.
func RunBinary(dir, program string, args ...string) (int, error) {
	cmd := exec.Command(BinaryPath(dir, program), args...)
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

// Clean removes the source-local artifacts and the precisely identified
// source-root fallback. It also removes the legacy per-entry fallback.
func Clean(entry string) error {
	abs, err := filepath.Abs(entry)
	if err != nil {
		return err
	}
	local, fallback, err := CacheDirs(abs)
	if err != nil {
		return err
	}
	paths := []string{filepath.Dir(local)}
	if fallback != "" {
		paths = append(paths, fallback)
	}
	if legacy, legacyErr := LegacyCacheDir(abs); legacyErr == nil {
		paths = append(paths, filepath.Dir(legacy))
	}
	for _, path := range paths {
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	}
	return nil
}
