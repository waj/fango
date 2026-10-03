package native

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
)

// Every function here returns an error rather than panicking; the compiler
// classifies it into IO.Error at the boundary. Errors are relabeled with the
// path the program supplied, because filePath joins onto the working
// directory and the absolute form would differ from run to run.
//
// Handles cross the native boundary as opaque Go values. IO.Handle and
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

func openWith(path string, readable bool, open func(string) (*os.File, error)) (any, error) {
	file, err := open(filePath(path))
	if err != nil {
		return nil, relabel(err, path)
	}
	return FangoNewIOHandle(path, file, readable, !readable), nil
}

func OpenRead(path string) (any, error) { return openWith(path, true, os.Open) }

func OpenWrite(path string) (any, error) { return openWith(path, false, os.Create) }

func OpenAppend(path string) (any, error) {
	return openWith(path, false, func(p string) (*os.File, error) {
		return os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	})
}

func CloseHandle(value any) error { return FangoCloseIOHandle(value) }

func ReadFileResult(path string) (string, error) {
	data, err := os.ReadFile(filePath(path))
	if err != nil {
		return "", relabel(err, path)
	}
	return strings.ToValidUTF8(string(data), "�"), nil
}

func WriteFileResult(path, text string) error {
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

func FileSize(path string) (int64, error) {
	info, err := os.Stat(filePath(path))
	if err != nil {
		return 0, relabel(err, path)
	}
	return info.Size(), nil
}

func IsDirectoryPath(path string) (bool, error) {
	info, err := os.Stat(filePath(path))
	if err != nil {
		return false, relabel(err, path)
	}
	return info.IsDir(), nil
}
