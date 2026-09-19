package compilecache

import (
	"encoding/hex"
	"path/filepath"

	"github.com/waj/fango/internal/artifactstore"
)

// EmissionStore is the opaque byte-storage seam for per-owner generated Go.
// The backend package owns the artifact schema and its validation.
type EmissionStore struct {
	store *artifactstore.Store
}

func NewEmissionStore(entry string) *EmissionStore {
	abs, err := filepath.Abs(entry)
	if err != nil {
		return &EmissionStore{}
	}
	local, fallback, err := cacheRoots(abs)
	if err != nil {
		return &EmissionStore{}
	}
	fp, err := compilerFingerprint()
	if err != nil {
		return &EmissionStore{}
	}
	roots := []string{filepath.Join(local, "v1", fp)}
	if fallback != "" {
		roots = append(roots, filepath.Join(fallback, "v1", fp))
	}
	return &EmissionStore{store: artifactstore.New(roots...)}
}

func emissionPath(key string) (string, bool) {
	b, err := hex.DecodeString(key)
	if err != nil || len(b) != 32 {
		return "", false
	}
	return filepath.ToSlash(filepath.Join("emitted", key+".json")), true
}

func (s *EmissionStore) Load(key string) ([]byte, bool) {
	path, ok := emissionPath(key)
	if s == nil || !ok {
		return nil, false
	}
	return s.store.Load(path)
}

func (s *EmissionStore) Store(key string, data []byte) {
	path, ok := emissionPath(key)
	if s == nil || !ok || len(data) == 0 {
		return
	}
	s.store.Store(path, data)
}
