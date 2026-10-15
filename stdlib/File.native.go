package native

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/waj/fango/runtime/fangort"
)

// OS operations return errors rather than panicking; the compiler classifies
// them into IO.Error at the boundary. Errors are relabeled with the
// path the program supplied, because filePath joins onto the working
// directory and the absolute form would differ from run to run.
//
// Handles cross the native boundary as opaque Go values. File.Handle and
// File.Directory remain distinct nominal Fango types even though both carry
// `any`; no module-owned id table is needed to keep the Go object alive.

type handle struct {
	path    string
	entries []string // directory listings, in os.ReadDir's sorted order
	next    int
	closed  atomic.Bool
	readMu  sync.Mutex
}

func filePath(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(FangoHost.WorkingDirectory(), path)
}

func lookup(value any) (*handle, error) {
	h, ok := value.(*handle)
	if !ok || h == nil || h.closed.Load() {
		return nil, &fs.PathError{Op: "use", Path: "", Err: errors.New("closed handle")}
	}
	return h, nil
}

// relabel makes a path error name the path the program passed in.
func relabel(err error, path string) error {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return &fs.PathError{Op: pathErr.Op, Path: path, Err: pathErr.Err}
	}
	return err
}

// Providers are fangort resource providers: fresh tokens compared by
// identity. Virtual handles are Fango values naming their provider; only system
// handles carry a native file, owned by the provider that opened it.
func provider(value any) (*fangort.ResourceProvider, error) { return fangort.CheckedProvider(value) }

func FreshProvider() any  { return fangort.NewResourceProvider(false) }
func SystemProvider() any { return fangort.NewResourceProvider(true) }

func SameProvider(a, b any) bool { return a == b }

func ProviderStatus(value any) error {
	p, err := provider(value)
	if err != nil {
		return err
	}
	return p.Status("")
}

func RetireProvider(value any) {
	p, err := provider(value)
	if err != nil {
		panic(err)
	}
	_ = p.Retire()
}

func SystemRetireProvider(value any) error {
	p, err := provider(value)
	if err != nil {
		return err
	}
	return p.Retire()
}

// SystemFilePath reads an immutable label, so it needs no lock.
func SystemFilePath(value any) string { return fangort.ResourceLabel(value) }

func openWith(value any, path string, readable bool, open func(string) (*os.File, error)) (any, error) {
	p, err := provider(value)
	if err != nil {
		return nil, err
	}
	return p.Adopt(path, func() (any, func() error, error) {
		file, err := open(filePath(path))
		if err != nil {
			return nil, nil, relabel(err, path)
		}
		h := fangort.NewFileHandle(path, file, readable, !readable)
		return h, func() error { return fangort.CloseFileHandle(h) }, nil
	})
}

func SystemOpenRead(provider any, path string) (any, error) {
	return openWith(provider, path, true, os.Open)
}
func SystemOpenWrite(provider any, path string) (any, error) {
	return openWith(provider, path, false, os.Create)
}
func SystemOpenAppend(provider any, path string) (any, error) {
	return openWith(provider, path, false, func(path string) (*os.File, error) {
		return os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	})
}

// realResource is the only projection of a system handle's native file. A
// closed handle stays owned, so a repeated close reaches the idempotent close.
func realResource(value, handle any, closing bool) (any, error) {
	p, err := provider(value)
	if err != nil {
		return nil, err
	}
	r, err := p.Use(handle, "handle", closing)
	if err != nil {
		return nil, err
	}
	return r.Value, nil
}

func SystemCloseHandle(provider, value any) error {
	file, err := realResource(provider, value, true)
	if err != nil {
		return err
	}
	return fangort.CloseFileHandle(file)
}
func SystemHandleHasInput(provider, value any) (bool, error) {
	file, err := realResource(provider, value, false)
	if err != nil {
		return false, err
	}
	return fangort.FileHandleHasInput(file)
}
func SystemReadHandleLine(provider, value any) (string, error) {
	file, err := realResource(provider, value, false)
	if err != nil {
		return "", err
	}
	return fangort.ReadFileHandleLine(file)
}
func SystemReadHandleBytes(provider, value any, count int64) ([]byte, error) {
	file, err := realResource(provider, value, false)
	if err != nil {
		return nil, err
	}
	return fangort.ReadFileHandleBytes(file, count)
}
func SystemWriteHandleBytes(provider, value any, data []byte) error {
	file, err := realResource(provider, value, false)
	if err != nil {
		return err
	}
	return fangort.WriteFileHandleBytes(file, data)
}

// FileText uses the same replacement rule for real and memory whole-file reads.
func FileText(data []byte) string { return strings.ToValidUTF8(string(data), "�") }

func SystemReadFileResult(path string) (string, error) {
	data, err := os.ReadFile(filePath(path))
	if err != nil {
		return "", relabel(err, path)
	}
	return FileText(data), nil
}

func SystemWriteFileResult(path, text string) error {
	if err := os.WriteFile(filePath(path), []byte(text), 0o644); err != nil {
		return relabel(err, path)
	}
	return nil
}

// OpenDirectory reads the whole listing at open, so a listing failure is
// reported where the directory is named rather than on a later entry.
func OpenDirectory(path string) (any, error) {
	entries, err := os.ReadDir(filePath(path))
	if err != nil {
		return nil, relabel(err, path)
	}
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.Name()
	}
	return &handle{path: path, entries: names}, nil
}

// ReadDirectoryEntry answers "" after the last entry; names are never empty.
func ReadDirectoryEntry(value any) (string, error) {
	h, err := lookup(value)
	if err != nil {
		return "", err
	}
	h.readMu.Lock()
	defer h.readMu.Unlock()
	if _, err := lookup(h); err != nil {
		return "", err
	}
	if h.next >= len(h.entries) {
		return "", nil
	}
	name := h.entries[h.next]
	h.next++
	return strings.ToValidUTF8(name, "�"), nil
}

func CloseDirectory(value any) error {
	h, err := lookup(value)
	if err != nil {
		return err
	}
	if h.closed.Swap(true) {
		return &fs.PathError{Op: "close", Path: h.path, Err: errors.New("closed handle")}
	}
	h.readMu.Lock()
	defer h.readMu.Unlock()
	h.entries = nil
	return nil
}

func SystemFileSize(path string) (int64, error) {
	info, err := os.Stat(filePath(path))
	if err != nil {
		return 0, relabel(err, path)
	}
	return info.Size(), nil
}

func SystemIsDirectoryPath(path string) (bool, error) {
	info, err := os.Stat(filePath(path))
	if err != nil {
		return false, relabel(err, path)
	}
	return info.IsDir(), nil
}

func SystemFault(message string) { panic(message) }
