package artifactframe

import (
	"bytes"
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	payload := []byte("arbitrary \n bytes \x00 with no structure")
	framed := Wrap("module-object", 1, payload)
	got, ok := Unwrap("module-object", 1, framed)
	if !ok || !bytes.Equal(got, payload) {
		t.Fatalf("round trip failed: %q %v", got, ok)
	}
}

func TestEmptyPayloadRoundTrips(t *testing.T) {
	framed := Wrap("emitted-unit", 3, nil)
	got, ok := Unwrap("emitted-unit", 3, framed)
	if !ok || len(got) != 0 {
		t.Fatalf("empty payload: %q %v", got, ok)
	}
}

// flipDigest changes one digest character and leaves the payload intact.
func flipDigest(framed []byte) []byte {
	out := append([]byte(nil), framed...)
	i := bytes.IndexByte(out, '\n') - 1
	if out[i] == '0' {
		out[i] = '1'
	} else {
		out[i] = '0'
	}
	return out
}

func TestForeignAndDamagedFramesAreInvalid(t *testing.T) {
	payload := []byte("payload")
	framed := Wrap("module-object", 1, payload)
	cases := []struct {
		name string
		kind string
		wire []byte
	}{
		{"other kind", "emitted-unit", framed},
		{"truncated header", "module-object", framed[:10]},
		{"no header", "module-object", payload},
		{"empty", "module-object", nil},
		{"damaged payload", "module-object", append(append([]byte(nil), framed...), '!')},
		// What an unsynced write can leave behind after a crash.
		{"truncated payload", "module-object", framed[:len(framed)-1]},
		{"zeroed", "module-object", make([]byte, len(framed))},
		{"damaged digest", "module-object", flipDigest(framed)},
		{"extra header field", "module-object", []byte(strings.Replace(string(framed), "fango-artifact 1 ", "fango-artifact 1 x ", 1))},
		{"other frame version", "module-object", []byte(strings.Replace(string(framed), "fango-artifact 1 ", "fango-artifact 2 ", 1))},
	}
	for _, c := range cases {
		if _, ok := Unwrap(c.kind, 1, c.wire); ok {
			t.Errorf("%s was accepted", c.name)
		}
	}
	if _, ok := Unwrap("module-object", 2, framed); ok {
		t.Error("other schema was accepted")
	}
}

func TestUnrepresentableKindStoresNothing(t *testing.T) {
	for _, kind := range []string{"", "two words", "with\nnewline"} {
		if Wrap(kind, 1, []byte("x")) != nil {
			t.Errorf("kind %q was framed", kind)
		}
	}
	if Wrap("module-object", -1, []byte("x")) != nil {
		t.Error("negative schema was framed")
	}
}

func TestHeaderScanIsBounded(t *testing.T) {
	if _, ok := Unwrap("module-object", 1, bytes.Repeat([]byte("x"), 1<<20)); ok {
		t.Fatal("unterminated header was accepted")
	}
}
