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
// Directories are flat. The embed patterns listed `stdlib/*.fango` and
// `runtime/<pkg>/*.go` without recursion, so a dotted module never named a
// nested file. Reading from disk would quietly start resolving them, which is
// a language change rather than a distribution one.
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

// lookup returns the index for one flat directory named relative to the root,
// for example "stdlib" or "runtime/fangort".
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

	dir := filepath.Join(root, filepath.FromSlash(rel))
	t.once.Do(func() {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.err = err
			return
		}
		t.names = make(map[string]bool, len(entries))
		for _, entry := range entries {
			if !entry.IsDir() {
				t.names[entry.Name()] = true
			}
		}
	})
	if t.err != nil {
		return nil, t.err
	}
	return t, nil
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

// split separates a slash-separated library path into its directory and file,
// rejecting anything that is not a plain name in a flat directory.
func split(rel string, depth int) (dir, name string, ok bool) {
	parts := strings.Split(rel, "/")
	if len(parts) != depth+1 {
		return "", "", false
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", "", false
		}
	}
	return strings.Join(parts[:depth], "/"), parts[depth], true
}
