package native

import (
	"bufio"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Every function here returns an error rather than panicking; the compiler
// classifies it into IO.Error at the boundary. Errors are relabeled with the
// path the program supplied, because filePath joins onto the working
// directory and the absolute form would differ from run to run.
//
// Handles live in this package's globals: a compiled program owns one table
// per process, and the interpreter's native worker keeps its globals for the
// session, so both backends share the same code and the same lifetime. Ids
// are never reused, so a stale handle is an ordinary "closed handle" failure
// rather than a silent alias of a newer file. A fango program cannot reach
// that failure: File.Handle is abstract and its scope closes exactly once.

type handle struct {
	path    string
	file    *os.File
	reader  *bufio.Reader
	entries []string // directory listings, in os.ReadDir's sorted order
	next    int
}

var (
	handles          = map[int64]*handle{}
	nextHandle int64 = 1
)

func filePath(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(FangoHost.WorkingDirectory(), path)
}

func register(h *handle) int64 {
	id := nextHandle
	nextHandle++
	handles[id] = h
	return id
}

func lookup(id int64) (*handle, error) {
	h, ok := handles[id]
	if !ok {
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

func openWith(path string, open func(string) (*os.File, error)) (int64, error) {
	file, err := open(filePath(path))
	if err != nil {
		return 0, relabel(err, path)
	}
	return register(&handle{path: path, file: file, reader: bufio.NewReader(file)}), nil
}

func OpenRead(path string) (int64, error) { return openWith(path, os.Open) }

func OpenWrite(path string) (int64, error) { return openWith(path, os.Create) }

func OpenAppend(path string) (int64, error) {
	return openWith(path, func(p string) (*os.File, error) {
		return os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	})
}

func CloseHandle(id int64) error {
	h, err := lookup(id)
	if err != nil {
		return err
	}
	delete(handles, id)
	if err := h.file.Close(); err != nil {
		return relabel(err, h.path)
	}
	return nil
}

// HandleHasInput distinguishes end of file from a read failure by peeking.
func HandleHasInput(id int64) (bool, error) {
	h, err := lookup(id)
	if err != nil {
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
func ReadHandleLine(id int64) (string, error) {
	h, err := lookup(id)
	if err != nil {
		return "", err
	}
	line, err := h.reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", relabel(err, h.path)
	}
	return strings.ToValidUTF8(line, "�"), nil
}

func WriteHandle(id int64, text string) error {
	h, err := lookup(id)
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
func OpenDirectory(path string) (int64, error) {
	entries, err := os.ReadDir(filePath(path))
	if err != nil {
		return 0, relabel(err, path)
	}
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.Name()
	}
	return register(&handle{path: path, entries: names}), nil
}

// ReadDirectoryEntry answers "" after the last entry; names are never empty.
func ReadDirectoryEntry(id int64) (string, error) {
	h, err := lookup(id)
	if err != nil {
		return "", err
	}
	if h.next >= len(h.entries) {
		return "", nil
	}
	name := h.entries[h.next]
	h.next++
	return strings.ToValidUTF8(name, "�"), nil
}

func CloseDirectory(id int64) error {
	if _, err := lookup(id); err != nil {
		return err
	}
	delete(handles, id)
	return nil
}

func IsDirectoryPath(path string) (bool, error) {
	info, err := os.Stat(filePath(path))
	if err != nil {
		return false, relabel(err, path)
	}
	return info.IsDir(), nil
}
