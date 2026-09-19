// Package artifactframe tags a cache artifact with its kind, schema, and
// payload digest without embedding the payload in a parsed container. A reader
// locates and verifies the payload without tokenizing it, and the digest covers
// the bytes exactly as stored rather than a re-encoded copy of them. It
// deliberately knows nothing about modules, inference, or code generation.
package artifactframe

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
)

const (
	magic      = "fango-artifact"
	version    = "1"
	fields     = 5
	maxHeader  = 128
	digestSize = sha256.Size * 2
)

// Wrap frames payload. It returns nil for a kind or schema that could not be
// read back unambiguously; callers store nothing in that case.
func Wrap(kind string, schema int, payload []byte) []byte {
	if kind == "" || strings.ContainsAny(kind, " \n") || schema < 0 {
		return nil
	}
	sum := sha256.Sum256(payload)
	header := magic + " " + version + " " + kind + " " + strconv.Itoa(schema) + " " + hex.EncodeToString(sum[:]) + "\n"
	if len(header) > maxHeader {
		return nil
	}
	out := make([]byte, 0, len(header)+len(payload))
	return append(append(out, header...), payload...)
}

// Unwrap returns the payload of a frame of exactly this kind and schema whose
// digest agrees with it. Foreign, malformed, and damaged frames are reported as
// invalid rather than repaired.
func Unwrap(kind string, schema int, data []byte) ([]byte, bool) {
	limit := min(len(data), maxHeader)
	end := strings.IndexByte(string(data[:limit]), '\n')
	if end < 0 {
		return nil, false
	}
	parts := strings.Split(string(data[:end]), " ")
	if len(parts) != fields || parts[0] != magic || parts[1] != version ||
		parts[2] != kind || parts[3] != strconv.Itoa(schema) || len(parts[4]) != digestSize {
		return nil, false
	}
	payload := data[end+1:]
	sum := sha256.Sum256(payload)
	if parts[4] != hex.EncodeToString(sum[:]) {
		return nil, false
	}
	return payload, true
}
