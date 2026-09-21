package fangort

import (
	"strings"
	"testing"
)

// The one rule a reviewer of native code has to check: a Bytes never aliases
// storage that anything will write again. These pin the two halves of it —
// what the helpers here promise, and what they therefore let slicing do.

func TestSliceSharesStorageAndCannotBeExtendedIntoIt(t *testing.T) {
	whole := BytesFromString("hello, world")
	prefix := BytesSlice(0, 5, whole)
	if &prefix[0] != &whole[0] {
		t.Fatal("slice copied rather than shared its backing array")
	}
	// The slice's capacity stops at its length, so even Go's own append
	// cannot reach the bytes that follow it.
	if cap(prefix) != len(prefix) {
		t.Fatalf("slice capacity = %d, want %d", cap(prefix), len(prefix))
	}
	if got := string(BytesAppend(prefix, BytesFromString("XX"))); got != "helloXX" {
		t.Fatalf("append = %q", got)
	}
	if got := string(whole); got != "hello, world" {
		t.Fatalf("appending to a slice disturbed the value it shares with: %q", got)
	}
}

func TestAppendDoesNotWriteIntoEitherArgument(t *testing.T) {
	left := BytesFromList(ListCons[int64](1, ListCons[int64](2, ListNil[int64]())))
	right := BytesFromString("ab")
	joined := BytesAppend(left, right)
	if len(joined) != 4 || joined[0] != 1 || joined[2] != 'a' {
		t.Fatalf("append = %v", joined)
	}
	joined[0] = 9
	if left[0] != 1 {
		t.Fatal("append shared storage with its left argument")
	}
	if right[0] != 'a' {
		t.Fatal("append shared storage with its right argument")
	}
}

// Scanning is where an HTTP parser spends its time, so it must not allocate
// per match: the search runs inside Go's bytes.Index over the shared array.
func TestScanningALargeInputAllocatesNothing(t *testing.T) {
	haystack := BytesFromString(strings.Repeat("header: value\r\n", 4000))
	needle := BytesFromString("\r\n")
	var at int64
	allocs := testing.AllocsPerRun(100, func() {
		at = 0
		for i := 0; i < 50; i++ {
			at = BytesIndexOfFrom(at+1, needle, haystack)
		}
	})
	if allocs != 0 {
		t.Fatalf("allocations per scan = %v, want 0", allocs)
	}
	if at <= 0 {
		t.Fatalf("scan found nothing (at = %d)", at)
	}
}

// Concatenation sizes the result once rather than growing it per part.
func TestConcatAllocatesOncePerCall(t *testing.T) {
	parts := ListNil[Bytes]()
	for i := 0; i < 64; i++ {
		parts = ListCons(BytesFromString("chunk"), parts)
	}
	var joined Bytes
	if allocs := testing.AllocsPerRun(100, func() { joined = BytesConcat(parts) }); allocs != 1 {
		t.Fatalf("allocations per concat = %v, want 1", allocs)
	}
	if len(joined) != 64*len("chunk") {
		t.Fatalf("concat length = %d", len(joined))
	}
}

func TestShowEscapesEverythingThatIsNotPrintableAscii(t *testing.T) {
	for _, tt := range []struct {
		in   Bytes
		want string
	}{
		{nil, `""`},
		{BytesFromString("plain"), `"plain"`},
		{Bytes{'"', '\\', '\n', '\t', '\r'}, `"\"\\\n\t\r"`},
		{Bytes{0x00, 0x1F, 0x7F, 0xFF}, `"\x00\x1F\x7F\xFF"`},
		{BytesFromString("año"), `"a\xC3\xB1o"`},
	} {
		if got := BytesShow(tt.in); got != tt.want {
			t.Errorf("BytesShow(%v) = %s, want %s", tt.in, got, tt.want)
		}
	}
}

// toStringLossy replaces each malformed byte and leaves valid text alone,
// which is what File.read already promises for text.
func TestToStringLossyReplacesOnlyMalformedBytes(t *testing.T) {
	if got := BytesToStringLossy(BytesFromString("año")); got != "año" {
		t.Fatalf("valid text changed: %q", got)
	}
	if got := BytesToStringLossy(Bytes{'h', 'i', 0xFF, '!'}); got != "hi�!" {
		t.Fatalf("lossy = %q", got)
	}
	// A scalar cut in half is one replacement, not one per byte.
	if got := BytesToStringLossy(Bytes{0xC3}); got != "�" {
		t.Fatalf("truncated scalar = %q", got)
	}
}
