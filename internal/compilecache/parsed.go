package compilecache

import (
	"encoding/hex"
	"path/filepath"

	"github.com/waj/fango/internal/artifactstore"
	"github.com/waj/fango/internal/build"
)

// ParsedStore is an opaque content-addressed byte store. The modules package
// owns parsed-unit schemas and validation, keeping this layer independent of
// parser and AST representation.
type ParsedStore struct {
	store *artifactstore.Store
}

func NewParsedStore(entry string) *ParsedStore {
	local, fallback, err := build.CacheDirs(entry)
	if err != nil {
		return &ParsedStore{}
	}
	fp, err := compilerFingerprint()
	if err != nil {
		return &ParsedStore{}
	}
	roots := []string{filepath.Join(local, "v1", fp)}
	if fallback != "" {
		roots = append(roots, filepath.Join(fallback, "v1", fp))
	}
	return &ParsedStore{store: artifactstore.New(roots...)}
}

func validContentHash(hash string) bool {
	b, err := hex.DecodeString(hash)
	return err == nil && len(b) == 32
}

func (s *ParsedStore) LoadParsed(sourceHash string) ([]byte, bool) {
	if s == nil || !validContentHash(sourceHash) {
		return nil, false
	}
	return s.store.Load(filepath.ToSlash(filepath.Join("parsed", sourceHash+".json")))
}

func (s *ParsedStore) StoreParsed(sourceHash string, data []byte) {
	if s == nil || !validContentHash(sourceHash) || len(data) == 0 {
		return
	}
	s.store.Store(filepath.ToSlash(filepath.Join("parsed", sourceHash+".json")), data)
}
