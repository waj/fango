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

func loadUnit(cache Cache, key, path string) ([]byte, bool) {
	data, ok := cache.Load(key)
	if !ok {
		return nil, false
	}
	return decodeUnit(data, key, path)
}

func storeUnit(cache Cache, key string, file codegen.File) {
	if data := encodeUnit(key, file); data != nil {
		cache.Store(key, data)
	}
}
