package fangort

import (
	"errors"
	"io"
	"testing"
)

type shortIOWriter struct{}

func (shortIOWriter) Write(data []byte) (int, error) { return len(data) / 2, nil }

func TestIOWriteRejectsShortSuccess(t *testing.T) {
	if err := WriteIOBytes(shortIOWriter{}, []byte("abc")); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short write = %v", err)
	}
}
