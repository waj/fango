// Package libroot locates the Fango library tree — the standard library and
// the Go runtime support sources — on disk. Those sources were once embedded
// in the compiler executable, which made them unreachable without a rebuild;
// resolving them from a root instead is what lets an installed compiler and a
// working checkout share one mechanism.
//
// A root holds stdlib/ and runtime/ side by side. Resolution is process-wide
// and computed once: both the value and any failure to find one are stable for
// the process, so every session in it agrees on which library it is compiling
// against.
package libroot

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// EnvRoot names the library tree explicitly, overriding every search below.
const EnvRoot = "FANGO_ROOT"

// goModule is the checkout's module path, which identifies a development root.
const goModule = "github.com/waj/fango"

// probe is the file whose presence makes a directory a library root. A
// directory that merely exists is not one: a partial tree must miss cleanly
// rather than half-load a prelude.
const probe = "stdlib/Prelude.fango"

type rootResult struct {
	sync.Once
	value string
	err   error
}

var (
	resolved = &rootResult{}

	// override replaces resolution entirely. Tests that need a synthetic
	// library set it; nothing else does.
	overrideMu sync.RWMutex
	override   string
)

// ErrNotFound reports that no library tree was found. It carries the places
// that were searched, because the useful diagnostic is where the compiler
// looked rather than that it failed.
type ErrNotFound struct{ Searched []string }

func (e *ErrNotFound) Error() string {
	if len(e.Searched) == 0 {
		return fmt.Sprintf("no Fango library found; set %s to the directory holding stdlib/ and runtime/", EnvRoot)
	}
	return fmt.Sprintf("no Fango library found in %s; set %s to the directory holding stdlib/ and runtime/",
		strings.Join(e.Searched, ", "), EnvRoot)
}

// Root is the library tree every bundled source is read from. The first
// candidate holding the probe wins: an explicit FANGO_ROOT, then the install
// layout beside the executable, then the checkout the working directory is in.
// The last is what lets tests and `go run ./cmd/fango` work from a source tree
// with nothing configured.
func Root() (string, error) {
	overrideMu.RLock()
	dir := override
	overrideMu.RUnlock()
	if dir != "" {
		return dir, nil
	}
	resolved.Do(func() { resolved.value, resolved.err = search() })
	return resolved.value, resolved.err
}

func search() (string, error) {
	var searched []string
	consider := func(dir string) (string, bool) {
		if dir == "" {
			return "", false
		}
		abs, err := filepath.Abs(dir)
		if err != nil {
			return "", false
		}
		searched = append(searched, abs)
		if _, err := os.Stat(filepath.Join(abs, filepath.FromSlash(probe))); err == nil {
			return abs, true
		}
		return "", false
	}

	// An explicit root is a request, not a hint: if it does not hold the
	// probe, report it rather than silently falling through to a checkout
	// that happens to be nearby.
	if env := os.Getenv(EnvRoot); env != "" {
		if dir, ok := consider(env); ok {
			return dir, nil
		}
		return "", &ErrNotFound{Searched: searched}
	}
	if exe, err := os.Executable(); err == nil {
		if exe, err := filepath.EvalSymlinks(exe); err == nil {
			bin := filepath.Dir(exe)
			for _, dir := range []string{
				filepath.Join(bin, "..", "lib", "fango"),
				filepath.Join(bin, "lib", "fango"),
			} {
				if dir, ok := consider(dir); ok {
					return dir, nil
				}
			}
		}
	}
	if dir, ok := consider(checkout()); ok {
		return dir, nil
	}
	return "", &ErrNotFound{Searched: searched}
}

// checkout walks up from the working directory for the Fango module root. A
// test binary runs in its own package directory and `go run` in the checkout,
// so this is the development case; it deliberately matches the module path so
// an unrelated go.mod overhead does not answer.
func checkout() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		if b, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil && declaresModule(b) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func declaresModule(goMod []byte) bool {
	for _, line := range strings.Split(string(goMod), "\n") {
		line = strings.TrimSpace(line)
		rest, ok := strings.CutPrefix(line, "module")
		if !ok || (rest != "" && !isSpace(rest[0])) {
			continue
		}
		return strings.TrimSpace(rest) == goModule
	}
	return false
}

func isSpace(b byte) bool { return b == ' ' || b == '\t' }

// SetForTest pins the library root for the caller's test and restores the
// previous setting afterwards. Resolution is process-wide, so a test that
// needs a synthetic library must not run in parallel with one that does not.
func SetForTest(dir string) func() {
	overrideMu.Lock()
	previous := override
	override = dir
	overrideMu.Unlock()
	return func() {
		overrideMu.Lock()
		override = previous
		overrideMu.Unlock()
	}
}

// ReadStdlib reads one standard-library file by its plain name, for example
// "List.fango" or "IO.native.go". A name that is not exactly an entry of
// stdlib/ misses with fs.ErrNotExist, which is how a caller distinguishes a
// module the library does not have from a library it cannot find.
func ReadStdlib(name string) ([]byte, error) { return read("stdlib", name, 0) }

// ReadRuntime reads one Go runtime support file named by its package and
// plain name, for example "fangort/list.go".
func ReadRuntime(rel string) ([]byte, error) { return read("runtime", rel, 1) }

func read(root, rel string, depth int) ([]byte, error) {
	sub, name, ok := split(rel, depth)
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: rel, Err: fs.ErrNotExist}
	}
	dir := root
	if sub != "" {
		dir += "/" + sub
	}
	t, err := lookup(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, &fs.PathError{Op: "open", Path: rel, Err: fs.ErrNotExist}
		}
		return nil, err
	}
	abs, err := Root()
	if err != nil {
		return nil, err
	}
	return t.read(filepath.Join(abs, filepath.FromSlash(dir)), name)
}

// StdlibNatives lists the standard library's Go sidecars by plain name.
func StdlibNatives() ([]string, error) {
	t, err := lookup("stdlib")
	if err != nil {
		return nil, err
	}
	return t.list(func(name string) bool { return strings.HasSuffix(name, ".native.go") }), nil
}

// RuntimePackage lists one runtime package's Go sources, each as
// "<pkg>/<name>". Test files are excluded: a generated module compiles these
// sources and has no test dependencies to satisfy.
func RuntimePackage(pkg string) ([]string, error) {
	t, err := lookup("runtime/" + pkg)
	if err != nil {
		return nil, err
	}
	names := t.list(func(name string) bool {
		return strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go")
	})
	for i, name := range names {
		names[i] = pkg + "/" + name
	}
	return names, nil
}

// Missing reports whether err is a failure to find a library tree at all, as
// opposed to a failure to read one file from a tree that was found.
func Missing(err error) bool {
	var notFound *ErrNotFound
	return errors.As(err, &notFound)
}
