package native

import (
	"bufio"
	"errors"
	"io"
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
// Handles cross the native boundary as opaque Go values. File.Handle and
// File.Directory remain distinct nominal Fango types even though both carry
// `any`; no module-owned id table is needed to keep the Go object alive.

type handle struct {
	path    string
	file    *os.File
	reader  *bufio.Reader
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

func openWith(path string, open func(string) (*os.File, error)) (any, error) {
	file, err := open(filePath(path))
	if err != nil {
		return nil, relabel(err, path)
	}
	return &handle{path: path, file: file, reader: bufio.NewReader(file)}, nil
}

func OpenRead(path string) (any, error) { return openWith(path, os.Open) }

func OpenWrite(path string) (any, error) { return openWith(path, os.Create) }

func OpenAppend(path string) (any, error) {
	return openWith(path, func(p string) (*os.File, error) {
		return os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	})
}

func CloseHandle(value any) error {
	h, err := lookup(value)
	if err != nil {
		return err
	}
	if h.closed.Swap(true) {
		return &fs.PathError{Op: "close", Path: h.path, Err: errors.New("closed handle")}
	}
	if err := h.file.Close(); err != nil {
		return relabel(err, h.path)
	}
	return nil
}

// HandleHasInput distinguishes end of file from a read failure by peeking.
func HandleHasInput(value any) (bool, error) {
	h, err := lookup(value)
	if err != nil {
		return false, err
	}
	h.readMu.Lock()
	defer h.readMu.Unlock()
	if _, err := lookup(h); err != nil {
		return false, err
	}
	if _, err := h.reader.Peek(1); err != nil {
		if err == io.EOF {
			return false, nil
		}
		return false, relabel(err, h.path)
	}
	return true, nil
}

// ReadHandleLine has IO.readRawLine's contract: the line with its terminator,
// or the unterminated remainder at end of file, with invalid UTF-8 replaced.
func ReadHandleLine(value any) (string, error) {
	h, err := lookup(value)
	if err != nil {
		return "", err
	}
	h.readMu.Lock()
	defer h.readMu.Unlock()
	if _, err := lookup(h); err != nil {
		return "", err
	}
	line, err := h.reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", relabel(err, h.path)
	}
	return strings.ToValidUTF8(line, "�"), nil
}

// maxByteRead bounds one counted read's allocation, so a program naming an
// absurd count gets its answer in pieces rather than an allocation the size of
// the count. A short read is part of the contract either way.
const maxByteRead = 1 << 16

// ReadHandleBytes answers at most max bytes, fewer when fewer are available,
// and empty at end of file, which HandleHasInput distinguishes as it does for
// ReadHandleLine. It reads through the handle's buffered reader, so counted
// reads and line reads interleave on one handle.
//
// The result is its own array. A Bytes may never alias a buffer something will
// write again (doc/design/backend.md, "Bytes representation"), which is why
// this allocates and copies rather than handing out a Peek into the reader.
func ReadHandleBytes(value any, max int64) ([]byte, error) {
	h, err := lookup(value)
	if err != nil {
		return nil, err
	}
	h.readMu.Lock()
	defer h.readMu.Unlock()
	if _, err := lookup(h); err != nil {
		return nil, err
	}
	if max <= 0 {
		return nil, nil
	}
	if max > maxByteRead {
		max = maxByteRead
	}
	buf := make([]byte, max)
	n, err := h.reader.Read(buf)
	if err != nil && err != io.EOF {
		return nil, relabel(err, h.path)
	}
	return buf[:n:n], nil
}

func WriteHandleBytes(value any, data []byte) error {
	h, err := lookup(value)
	if err != nil {
		return err
	}
	if _, err := h.file.Write(data); err != nil {
		return relabel(err, h.path)
	}
	return nil
}

func WriteHandle(value any, text string) error {
	h, err := lookup(value)
	if err != nil {
		return err
	}
	if _, err := h.file.WriteString(text); err != nil {
		return relabel(err, h.path)
	}
	return nil
}

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

func IsDirectoryPath(path string) (bool, error) {
	info, err := os.Stat(filePath(path))
	if err != nil {
		return false, relabel(err, path)
	}
	return info.IsDir(), nil
}
