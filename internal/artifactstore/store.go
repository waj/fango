// Package artifactstore provides optional atomic byte storage beneath ordered
// cache roots. It deliberately knows nothing about modules, ASTs, inference,
// code generation, or artifact schemas.
package artifactstore

import (
	"os"
	"path/filepath"
	"strings"
)

type Store struct {
	roots []string
}

func New(roots ...string) *Store {
	return &Store{roots: append([]string(nil), roots...)}
}

func (s *Store) Load(relative string) ([]byte, bool) {
	if s == nil || !safeRelative(relative) {
		return nil, false
	}
	for _, root := range s.roots {
		if data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative))); err == nil {
			return data, true
		}
	}
	return nil, false
}

func (s *Store) Store(relative string, data []byte) {
	if s == nil || !safeRelative(relative) || len(data) == 0 {
		return
	}
	for _, root := range s.roots {
		if writeAtomic(filepath.Join(root, filepath.FromSlash(relative)), data) == nil {
			return
		}
	}
}

func safeRelative(path string) bool {
	clean := filepath.Clean(filepath.FromSlash(path))
	return clean != "." && !filepath.IsAbs(clean) && clean != ".." && !strings.HasPrefix(clean, ".."+string(filepath.Separator))
}

func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".artifact-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	closed := false
	defer func() {
		if !closed {
			_ = tmp.Close()
		}
		_ = os.Remove(name)
	}()
	if err := tmp.Chmod(0o644); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	closed = true
	return os.Rename(name, path)
}
