package native

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The counted reads go through the same buffered reader the line reads use, so
// the two interleave on one handle, and they answer their own array rather
// than a view into that reader — a Bytes may never alias a buffer the next
// read overwrites.
func TestHandleBytes(t *testing.T) {
	old := FangoHost
	dir := t.TempDir()
	FangoHost = &testHost{dir: dir}
	t.Cleanup(func() { FangoHost = old })

	body := "alpha\n\xff\xfe binary\x00\nomega"
	if err := os.WriteFile(filepath.Join(dir, "input.bin"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	id, err := OpenRead("input.bin")
	if err != nil {
		t.Fatal(err)
	}

	line, err := ReadHandleLine(id)
	if err != nil || line != "alpha\n" {
		t.Fatalf("line = %q, %v", line, err)
	}
	// A short count answers a short read, and the result does not alias the
	// reader: writing into it leaves the next read alone.
	first, err := ReadHandleBytes(id, 3)
	if err != nil || string(first) != "\xff\xfe " {
		t.Fatalf("counted read = %q, %v", first, err)
	}
	first[0] = 'x'
	rest, err := ReadHandleBytes(id, 1024)
	if err != nil || string(rest) != "binary\x00\nomega" {
		t.Fatalf("rest = %q, %v", rest, err)
	}
	// Appending to a result cannot reach into what follows it either.
	if grown := append(first, '!'); string(rest) != "binary\x00\nomega" {
		t.Fatalf("appending to %q disturbed the next read: %q", grown, rest)
	}
	if end, err := ReadHandleBytes(id, 1024); err != nil || len(end) != 0 {
		t.Fatalf("at end of file = %q, %v", end, err)
	}
	if none, err := ReadHandleBytes(id, 0); err != nil || len(none) != 0 {
		t.Fatalf("zero count = %q, %v", none, err)
	}
	if err := CloseHandle(id); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadHandleBytes(id, 1); err == nil || !strings.Contains(err.Error(), "closed handle") {
		t.Fatalf("stale handle = %v", err)
	}
}

// A write reports the path the program named rather than the absolute one the
// working directory produced, as every other File native does.
func TestWriteHandleBytes(t *testing.T) {
	old := FangoHost
	dir := t.TempDir()
	FangoHost = &testHost{dir: dir}
	t.Cleanup(func() { FangoHost = old })

	id, err := OpenWrite("out.bin")
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteHandleBytes(id, []byte{0xff, 0x00, 'A'}); err != nil {
		t.Fatal(err)
	}
	if err := WriteHandleBytes(id, nil); err != nil {
		t.Fatal(err)
	}
	if err := CloseHandle(id); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(filepath.Join(dir, "out.bin"))
	if err != nil || string(written) != "\xff\x00A" {
		t.Fatalf("file = %q, %v", written, err)
	}

	reading, err := OpenRead("out.bin")
	if err != nil {
		t.Fatal(err)
	}
	defer CloseHandle(reading)
	if err := WriteHandleBytes(reading, []byte("x")); err == nil || !strings.Contains(err.Error(), "out.bin") {
		t.Fatalf("write to a read-only handle = %v", err)
	}
}
