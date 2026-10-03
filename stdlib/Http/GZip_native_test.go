package native

import (
	"bytes"
	"compress/gzip"
	"io"
	"strings"
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

func gzipped(t *testing.T, parts ...string) []byte {
	t.Helper()
	var wire bytes.Buffer
	for _, part := range parts {
		w := gzip.NewWriter(&wire)
		if _, err := io.WriteString(w, part); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return wire.Bytes()
}

func decodeAll(d any, wire []byte, step int) (string, int64) {
	var plain []byte
	for len(wire) > 0 {
		n := min(step, len(wire))
		if status := DecoderPush(d, wire[:n]); status != decoderOK {
			return string(plain), status
		}
		wire = wire[n:]
		pending := DecoderPending(d)
		plain = append(plain, pending...)
		DecoderSkip(d, int64(len(pending)))
	}
	status := DecoderFinish(d)
	plain = append(plain, DecoderPending(d)...)
	return string(plain), status
}

func TestDecoderStreamsInSmallSteps(t *testing.T) {
	text := strings.Repeat("streaming gzip ", 5000)
	for _, step := range []int{1, 7, 4096, 1 << 20} {
		plain, status := decodeAll(NewDecoder(1<<30), gzipped(t, text), step)
		if status != decoderOK || plain != text {
			t.Fatalf("step %d: status %d, %d bytes", step, status, len(plain))
		}
	}
}

func TestDecoderReportsFailures(t *testing.T) {
	wire := gzipped(t, "hello world")
	if _, status := decodeAll(NewDecoder(1<<30), wire[:len(wire)-4], 3); status != decoderMalformed {
		t.Fatalf("truncated stream: status %d", status)
	}
	if _, status := decodeAll(NewDecoder(1<<30), append(append([]byte(nil), wire...), "junk"...), 3); status != decoderMalformed {
		t.Fatalf("trailing junk: status %d", status)
	}
	if _, status := decodeAll(NewDecoder(5), wire, 3); status != decoderTooLarge {
		t.Fatalf("over limit: status %d", status)
	}
	if plain, status := decodeAll(NewDecoder(1<<30), gzipped(t, "one ", "two"), 5); status != decoderOK || plain != "one two" {
		t.Fatalf("concatenated members: %q status %d", plain, status)
	}
}
