package libroot

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Trees are read through an exact-name index rather than straight from the
// filesystem, which preserves two properties the embedded sources had.
//
// Names are case-exact. macOS and Windows resolve `basics.fango` to
// `Basics.fango`, so reading a joined path would let `import basics` reach a
// bundled module that the embedded provider, and the local one beside it
// (modules.FSProvider), both refuse.
//
// Library trees are indexed recursively, with slash-separated paths kept
// case-exact. This supports nested standard-library modules without allowing
// a case-insensitive filesystem to change module identity.
//
// Bytes are read once and retained, so every reader in a process sees the same
// library even if it is edited underneath them mid-compile. Entries are keyed
// by resolved root, so a test that repoints the root does not see the previous
// one's contents.
type tree struct {
	once  sync.Once
	names map[string]bool
	err   error

	mu    sync.Mutex
	files map[string][]byte
}

var (
	treesMu sync.Mutex
	trees   = map[string]*tree{}
)

// lookup returns an exact-name index for one directory tree relative to the
// root, for example "stdlib" or "runtime/fangort".
func lookup(rel string) (*tree, error) {
	root, err := Root()
	if err != nil {
		return nil, err
	}
	key := root + "\x00" + rel
	treesMu.Lock()
	t := trees[key]
	if t == nil {
		t = &tree{files: map[string][]byte{}}
		trees[key] = t
	}
	treesMu.Unlock()

	dir, err := exactDirectory(root, rel)
	if err != nil {
		return nil, err
	}
	t.once.Do(func() {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.err = err
			return
		}
		t.names = make(map[string]bool, len(entries))
		t.err = filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || path == dir {
				return nil
			}
			rel, err := filepath.Rel(dir, path)
			if err != nil {
				return err
			}
			t.names[filepath.ToSlash(rel)] = true
			return nil
		})
	})
	if t.err != nil {
		return nil, t.err
	}
	return t, nil
}

func exactDirectory(root, rel string) (string, error) {
	dir := root
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if part == "" || part == "." || part == ".." {
			return "", &fs.PathError{Op: "open", Path: rel, Err: fs.ErrNotExist}
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return "", err
		}
		found := false
		for _, entry := range entries {
			if entry.Name() == part && entry.IsDir() {
				found = true
				break
			}
			if strings.EqualFold(entry.Name(), part) {
				return "", &fs.PathError{Op: "open", Path: rel, Err: fs.ErrNotExist}
			}
		}
		if !found {
			return "", &fs.PathError{Op: "open", Path: rel, Err: fs.ErrNotExist}
		}
		dir = filepath.Join(dir, part)
	}
	return dir, nil
}

func (t *tree) read(dir, name string) ([]byte, error) {
	if !t.names[name] {
		return nil, &fs.PathError{Op: "open", Path: filepath.Join(dir, name), Err: fs.ErrNotExist}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if data, ok := t.files[name]; ok {
		return data, nil
	}
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return nil, err
	}
	t.files[name] = data
	return data, nil
}

// list reports the directory's names that keep accepts, sorted, so anything
// built over them has a deterministic identity.
func (t *tree) list(keep func(string) bool) []string {
	var names []string
	for name := range t.names {
		if keep(name) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// split validates a slash-separated library path. A nonnegative depth fixes
// the number of directory components; -1 allows any nonempty relative path.
func split(rel string, depth int) (dir, name string, ok bool) {
	parts := strings.Split(rel, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", "", false
		}
	}
	if len(parts) == 0 || (depth >= 0 && len(parts) != depth+1) {
		return "", "", false
	}
	if depth < 0 {
		depth = len(parts) - 1
	}
	return strings.Join(parts[:depth], "/"), parts[depth], true
}
