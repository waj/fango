package compilecache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"sort"

	"github.com/waj/fango/internal/artifactstore"
)

// ModuleStore is the opaque byte-storage seam for checked module objects.
// The check package owns candidate/object schemas and validation.
type ModuleStore struct {
	store *artifactstore.Store
}

func NewModuleStore(entry string) *ModuleStore {
	abs, err := filepath.Abs(entry)
	if err != nil {
		return &ModuleStore{}
	}
	local, fallback, err := cacheRoots(abs)
	if err != nil {
		return &ModuleStore{}
	}
	fp, err := compilerFingerprint()
	if err != nil {
		return &ModuleStore{}
	}
	roots := []string{filepath.Join(local, "v1", fp)}
	if fallback != "" {
		roots = append(roots, filepath.Join(fallback, "v1", fp))
	}
	return &ModuleStore{store: artifactstore.New(roots...)}
}

func validKey(key string) bool {
	b, err := hex.DecodeString(key)
	return err == nil && len(b) == 32
}

func (s *ModuleStore) LoadCandidates(baseKey string) [][]byte {
	if s == nil || !validKey(baseKey) {
		return nil
	}
	data, ok := s.store.Load(filepath.ToSlash(filepath.Join("checked-candidate-index", baseKey+".json")))
	if !ok {
		return nil
	}
	var keys []string
	if json.Unmarshal(data, &keys) != nil {
		return nil
	}
	out := make([][]byte, 0, len(keys))
	for _, key := range keys {
		if !validKey(key) {
			return nil
		}
		if candidate, found := s.store.Load(filepath.ToSlash(filepath.Join("checked-candidates", key+".json"))); found {
			out = append(out, candidate)
		}
	}
	return out
}

func (s *ModuleStore) StoreCandidate(baseKey string, data []byte) {
	if s == nil || !validKey(baseKey) || len(data) == 0 {
		return
	}
	h := sha256.Sum256(data)
	key := hex.EncodeToString(h[:])
	s.store.Store(filepath.ToSlash(filepath.Join("checked-candidates", key+".json")), data)
	indexPath := filepath.ToSlash(filepath.Join("checked-candidate-index", baseKey+".json"))
	var keys []string
	if old, ok := s.store.Load(indexPath); ok {
		_ = json.Unmarshal(old, &keys)
	}
	for _, old := range keys {
		if old == key {
			return
		}
	}
	keys = append(keys, key)
	sort.Strings(keys)
	if encoded, err := json.Marshal(keys); err == nil {
		s.store.Store(indexPath, encoded)
	}
}

func (s *ModuleStore) LoadObject(objectKey string) ([]byte, bool) {
	if s == nil || !validKey(objectKey) {
		return nil, false
	}
	return s.store.Load(filepath.ToSlash(filepath.Join("checked", objectKey+".json")))
}

func (s *ModuleStore) StoreObject(objectKey string, data []byte) {
	if s == nil || !validKey(objectKey) || len(data) == 0 {
		return
	}
	s.store.Store(filepath.ToSlash(filepath.Join("checked", objectKey+".json")), data)
}
