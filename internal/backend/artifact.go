package backend

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	"github.com/waj/fango/internal/artifactframe"
	"github.com/waj/fango/internal/codegen"
)

const (
	emissionSchema = 1
	emissionKind   = "emitted-unit"
)

func digest(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		_, _ = h.Write([]byte(strconv.Itoa(len(part))))
		_, _ = h.Write([]byte{':'})
		_, _ = h.Write([]byte(part))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// An emitted unit is its key and unit path on their own lines followed by the
// generated Go source verbatim. The artifact frame carries the digest, so the
// payload needs no container of its own and the source needs no re-encoding.
func encodeUnit(key string, file codegen.File) []byte {
	if strings.ContainsAny(key, "\n") || strings.ContainsAny(file.Path, "\n") {
		return nil
	}
	payload := make([]byte, 0, len(key)+len(file.Path)+2+len(file.Data))
	payload = append(payload, key...)
	payload = append(payload, '\n')
	payload = append(payload, file.Path...)
	payload = append(payload, '\n')
	payload = append(payload, file.Data...)
	return artifactframe.Wrap(emissionKind, emissionSchema, payload)
}

func decodeUnit(data []byte, key, path string) ([]byte, bool) {
	payload, ok := artifactframe.Unwrap(emissionKind, emissionSchema, data)
	if !ok {
		return nil, false
	}
	storedKey, rest, ok := bytes.Cut(payload, []byte{'\n'})
	if !ok || string(storedKey) != key {
		return nil, false
	}
	storedPath, source, ok := bytes.Cut(rest, []byte{'\n'})
	if !ok || string(storedPath) != path || len(source) == 0 {
		return nil, false
	}
	return source, true
}

// loadUnit also reports the artifact bytes it read, which a structurally
// invalid artifact still costs even though it is a miss.
func loadUnit(cache Cache, slot, key, path string) ([]byte, int, bool) {
	data, ok := cache.Load(slot)
	if !ok {
		return nil, 0, false
	}
	source, ok := decodeUnit(data, key, path)
	return source, len(data), ok
}

// storeUnit reports the bytes it wrote, or zero when the unit could not be
// encoded.
func storeUnit(cache Cache, slot, key string, file codegen.File) int {
	data := encodeUnit(key, file)
	if data == nil {
		return 0
	}
	cache.Store(slot, data)
	return len(data)
}
