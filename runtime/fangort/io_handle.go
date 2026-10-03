package fangort

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"strings"
	"sync"
	"sync/atomic"
)

// IOHandle is shared by File acquisition and IO operations. Standard endpoints
// are identities only: every operation borrows its caller's current host.
type IOHandle struct {
	path               string
	file               *os.File
	reader             *bufio.Reader
	readable, writable bool
	standard           int
	closed             atomic.Bool
	readMu, writeMu    sync.Mutex
}

func NewIOHandle(path string, file *os.File, readable, writable bool) any {
	return &IOHandle{path: path, file: file, reader: bufio.NewReader(file), readable: readable, writable: writable, standard: -1}
}

func StandardIOHandle(endpoint int64) any {
	if endpoint < 0 || endpoint > 2 {
		panic("invalid standard IO endpoint")
	}
	return &IOHandle{path: []string{"stdin", "stdout", "stderr"}[endpoint], standard: int(endpoint), readable: endpoint == 0, writable: endpoint != 0}
}

func ioHandle(value any, reading bool) (*IOHandle, error) {
	h, ok := value.(*IOHandle)
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

func (h *IOHandle) failure(op string, err error) error {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		err = pathErr.Err
	}
	return &fs.PathError{Op: op, Path: h.path, Err: err}
}

func CloseIOHandle(value any) error {
	h, ok := value.(*IOHandle)
	if !ok || h == nil {
		return &fs.PathError{Op: "close", Err: errors.New("invalid handle")}
	}
	if h.standard >= 0 {
		return h.failure("close", errors.New("standard handle cannot be closed"))
	}
	if h.closed.Swap(true) {
		return h.failure("close", errors.New("closed handle"))
	}
	if err := h.file.Close(); err != nil {
		return h.failure("close", err)
	}
	return nil
}

func IOHandleHasInput(host SessionHost, value any) (bool, error) {
	h, err := ioHandle(value, true)
	if err != nil {
		return false, err
	}
	if h.standard == 0 {
		ok, err := host.HasInput()
		if err != nil {
			return false, h.failure("read", err)
		}
		return ok, nil
	}
	h.readMu.Lock()
	defer h.readMu.Unlock()
	if _, err := ioHandle(h, true); err != nil {
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

func ReadIOHandleLine(host SessionHost, value any) (string, error) {
	h, err := ioHandle(value, true)
	if err != nil {
		return "", err
	}
	var data []byte
	if h.standard == 0 {
		data, err = host.ReadInputLine()
	} else {
		h.readMu.Lock()
		defer h.readMu.Unlock()
		if _, err := ioHandle(h, true); err != nil {
			return "", err
		}
		data, err = h.reader.ReadBytes('\n')
	}
	if err != nil && err != io.EOF {
		return "", h.failure("read", err)
	}
	return strings.ToValidUTF8(string(data), "�"), nil
}

// ReadIOBytes bounds allocation and publishes an independent result array.
func ReadIOBytes(reader io.Reader, count int64) ([]byte, error) {
	if count <= 0 {
		return nil, nil
	}
	if count > 65536 {
		count = 65536
	}
	buf := make([]byte, count)
	n, err := reader.Read(buf)
	if err != nil && err != io.EOF {
		return nil, err
	}
	if n < len(buf)/2 {
		return bytes.Clone(buf[:n:n]), nil
	}
	return buf[:n:n], nil
}

func ReadIOHandleBytes(host SessionHost, value any, count int64) ([]byte, error) {
	h, err := ioHandle(value, true)
	if err != nil {
		return nil, err
	}
	var data []byte
	if h.standard == 0 {
		data, err = host.ReadInputBytes(count)
	} else {
		h.readMu.Lock()
		defer h.readMu.Unlock()
		if _, err := ioHandle(h, true); err != nil {
			return nil, err
		}
		data, err = ReadIOBytes(h.reader, count)
	}
	if err != nil && err != io.EOF {
		return nil, h.failure("read", err)
	}
	return data, nil
}

// A sink must consume all bytes. A short write is an IO failure.
func WriteIOBytes(writer io.Writer, data []byte) error {
	n, err := writer.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	return err
}

func WriteIOHandleBytes(host SessionHost, value any, data []byte) error {
	h, err := ioHandle(value, false)
	if err != nil {
		return err
	}
	switch h.standard {
	case 1:
		err = host.WriteOutput(data)
	case 2:
		err = host.WriteError(data)
	default:
		h.writeMu.Lock()
		defer h.writeMu.Unlock()
		if _, err := ioHandle(h, false); err != nil {
			return err
		}
		err = WriteIOBytes(h.file, data)
	}
	if err != nil {
		return h.failure("write", err)
	}
	return nil
}
