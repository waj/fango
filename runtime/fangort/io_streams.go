package fangort

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"strings"
)

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

// A sink must consume all bytes. A short write is an IO failure.
func WriteIOBytes(writer io.Writer, data []byte) error {
	n, err := writer.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	return err
}

func ConsoleHasInput(host SessionHost) (bool, error) {
	value, err := host.HasInput()
	return value, streamError("stdin", err)
}

func ReadConsoleLine(host SessionHost) (string, error) {
	data, err := host.ReadInputLine()
	if err == io.EOF {
		err = nil
	}
	return strings.ToValidUTF8(string(data), "�"), streamError("stdin", err)
}

func ReadConsoleBytes(host SessionHost, count int64) ([]byte, error) {
	data, err := host.ReadInputBytes(count)
	if err == io.EOF {
		err = nil
	}
	return data, streamError("stdin", err)
}

func WriteConsoleBytes(host SessionHost, endpoint int64, data []byte) error {
	switch endpoint {
	case 1:
		return streamError("stdout", host.WriteOutput(data))
	case 2:
		return streamError("stderr", host.WriteError(data))
	default:
		panic("invalid console output")
	}
}

func streamError(path string, err error) error {
	if err == nil {
		return nil
	}
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		err = pathErr.Err
	}
	return &fs.PathError{Op: "stream", Path: path, Err: err}
}
