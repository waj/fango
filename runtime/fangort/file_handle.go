package fangort

import (
	"bufio"
	"errors"
	"io"
	"io/fs"
	"os"
	"strings"
	"sync"
	"sync/atomic"
)

// FileHandle owns a buffered native file. Console access goes directly to
// the active session host and never constructs a file handle.
type FileHandle struct {
	path               string
	file               *os.File
	reader             *bufio.Reader
	readable, writable bool
	closed             atomic.Bool
	readMu, writeMu    sync.Mutex
}

func NewFileHandle(path string, file *os.File, readable, writable bool) any {
	return &FileHandle{path: path, file: file, reader: bufio.NewReader(file), readable: readable, writable: writable}
}

func fileHandle(value any, reading bool) (*FileHandle, error) {
	h, ok := value.(*FileHandle)
	if !ok || h == nil {
		return nil, &fs.PathError{Op: "use", Err: errors.New("invalid handle")}
	}
	if h.closed.Load() {
		return nil, h.failure("use", errors.New("closed handle"))
	}
	if reading && !h.readable {
		return nil, h.failure("read", errors.New("handle is not readable"))
	}
	if !reading && !h.writable {
		return nil, h.failure("write", errors.New("handle is not writable"))
	}
	return h, nil
}

func (h *FileHandle) failure(op string, err error) error {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		err = pathErr.Err
	}
	return &fs.PathError{Op: op, Path: h.path, Err: err}
}

func CloseFileHandle(value any) error {
	h, ok := value.(*FileHandle)
	if !ok || h == nil {
		return &fs.PathError{Op: "close", Err: errors.New("invalid handle")}
	}
	if h.closed.Swap(true) {
		return nil
	}
	if err := h.file.Close(); err != nil {
		return h.failure("close", err)
	}
	return nil
}

func FileHandleHasInput(value any) (bool, error) {
	h, err := fileHandle(value, true)
	if err != nil {
		return false, err
	}
	h.readMu.Lock()
	defer h.readMu.Unlock()
	if _, err := fileHandle(h, true); err != nil {
		return false, err
	}
	_, err = h.reader.Peek(1)
	if err == io.EOF {
		return false, nil
	}
	if err != nil {
		return false, h.failure("read", err)
	}
	return true, nil
}

func ReadFileHandleLine(value any) (string, error) {
	h, err := fileHandle(value, true)
	if err != nil {
		return "", err
	}
	h.readMu.Lock()
	defer h.readMu.Unlock()
	if _, err := fileHandle(h, true); err != nil {
		return "", err
	}
	data, err := h.reader.ReadBytes('\n')
	if err != nil && err != io.EOF {
		return "", h.failure("read", err)
	}
	return strings.ToValidUTF8(string(data), "�"), nil
}

func ReadFileHandleBytes(value any, count int64) ([]byte, error) {
	h, err := fileHandle(value, true)
	if err != nil {
		return nil, err
	}
	h.readMu.Lock()
	defer h.readMu.Unlock()
	if _, err := fileHandle(h, true); err != nil {
		return nil, err
	}
	data, err := ReadIOBytes(h.reader, count)
	if err != nil && err != io.EOF {
		return nil, h.failure("read", err)
	}
	return data, nil
}

func WriteFileHandleBytes(value any, data []byte) error {
	h, err := fileHandle(value, false)
	if err != nil {
		return err
	}
	h.writeMu.Lock()
	defer h.writeMu.Unlock()
	if _, err := fileHandle(h, false); err != nil {
		return err
	}
	err = WriteIOBytes(h.file, data)
	if err != nil {
		return h.failure("write", err)
	}
	return nil
}
