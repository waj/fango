package native

import (
	"bytes"
	"compress/gzip"
	"io"
	"testing"
)

func TestStreamingGZip(t *testing.T) {
	state := NewCompressor()
	var wire []byte
	wire = append(wire, Push(state, []byte("hello "))...)
	wire = append(wire, Flush(state)...)
	wire = append(wire, Push(state, []byte("world"))...)
	wire = append(wire, Finish(state)...)
	reader, err := gzip.NewReader(bytes.NewReader(wire))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if string(plain) != "hello world" {
		t.Fatalf("decoded %q", plain)
	}
}
