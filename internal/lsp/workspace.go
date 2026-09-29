package lsp

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/waj/fango/internal/check"
	"github.com/waj/fango/internal/libroot"
	"github.com/waj/fango/internal/modules"
)

type initializeParams struct {
	RootURI          string `json:"rootUri"`
	RootPath         string `json:"rootPath"`
	WorkspaceFolders []struct {
		URI string `json:"uri"`
	} `json:"workspaceFolders"`
}

type workspaceFoldersChangeParams struct {
	Event struct {
		Added []struct {
			URI string `json:"uri"`
		} `json:"added"`
		Removed []struct {
			URI string `json:"uri"`
		} `json:"removed"`
	} `json:"event"`
}

func initializeRoots(p initializeParams) []string {
	var roots []string
	for _, folder := range p.WorkspaceFolders {
		if path, ok := uriPath(folder.URI); ok {
			roots = append(roots, path)
		}
	}
	if len(roots) == 0 {
		if path, ok := uriPath(p.RootURI); ok {
			roots = append(roots, path)
		} else if p.RootPath != "" && filepath.IsAbs(p.RootPath) {
			roots = append(roots, filepath.Clean(p.RootPath))
		}
	}
	return uniqueRoots(roots)
}

func changedRoots(roots []string, p workspaceFoldersChangeParams) []string {
	set := map[string]bool{}
	for _, root := range roots {
		set[root] = true
	}
	for _, folder := range p.Event.Removed {
		if path, ok := uriPath(folder.URI); ok {
			delete(set, path)
		}
	}
	for _, folder := range p.Event.Added {
		if path, ok := uriPath(folder.URI); ok {
			set[path] = true
		}
	}
	updated := make([]string, 0, len(set))
	for root := range set {
		updated = append(updated, root)
	}
	return uniqueRoots(updated)
}

func uniqueRoots(roots []string) []string {
	sort.Strings(roots)
	out := roots[:0]
	for _, root := range roots {
		if len(out) == 0 || root != out[len(out)-1] {
			out = append(out, root)
		}
	}
	return out
}

func (s *server) workspaceIndexes(ctx context.Context, queryPath string, spelling []byte) (map[string]*index, error) {
	s.workspaceMu.Lock()
	defer s.workspaceMu.Unlock()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		s.mu.Lock()
		gen := s.generation
		roots := append([]string(nil), s.roots...)
		open := copyOpen(s.open)
		prior := s.workspace
		s.mu.Unlock()
		if len(roots) == 0 {
			content, ok := open[queryPath]
			if !ok {
				content, _ = os.ReadFile(queryPath)
			}
			roots = []string{sourceRoot(queryPath, content)}
		}
		key := strings.Join(roots, "\x00") + "\x00" + string(spelling)
		s.mu.Lock()
		if s.workspaceReady && s.workspaceGen == gen && s.workspaceKey == key {
			indexes := s.workspace
			s.mu.Unlock()
			return indexes, nil
		}
		s.mu.Unlock()
		indexes, err := scanWorkspace(ctx, roots, open, prior, spelling)
		if err != nil {
			return nil, err
		}
		s.mu.Lock()
		if gen == s.generation {
			s.workspace = indexes
			s.workspaceGen = gen
			s.workspaceKey = key
			s.workspaceReady = true
			s.mu.Unlock()
			return indexes, nil
		}
		s.mu.Unlock()
	}
}

func scanWorkspace(ctx context.Context, roots []string, open map[string][]byte, prior map[string]*index, spelling []byte) (map[string]*index, error) {
	paths := map[string]bool{}
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if d.IsDir() {
				if path != root && (d.Name() == ".git" || d.Name() == ".fango" || d.Name() == "node_modules") {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(d.Name(), ".fango") {
				paths[filepath.Clean(path)] = true
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	ordered := make([]string, 0, len(paths))
	for path := range paths {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)
	indexes := make(map[string]*index, len(ordered))
	for _, path := range ordered {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		content, ok := open[path]
		if !ok {
			var err error
			content, err = os.ReadFile(path)
			if err != nil {
				continue
			}
		}
		if !bytes.Contains(content, spelling) {
			continue
		}
		root := sourceRoot(path, content)
		fresh := freshSources(root, path, open)
		result, errs, internal := (&check.Session{LoadOptions: modules.LoadOptions{Root: root, Overlays: open, AllowBundledEntry: true}, FreshSources: fresh}).Compile(path)
		if result != nil && len(errs) == 0 && internal == nil {
			indexes[path] = newIndex(root, result)
		} else if old := prior[path]; old != nil && old.documents[path] != nil && string(old.documents[path].file.Content) == string(content) {
			indexes[path] = old
		}
	}
	return indexes, nil
}

func freshSources(root, entry string, open map[string][]byte) map[string]bool {
	fresh := map[string]bool{}
	paths := make([]string, 0, len(open)+1)
	paths = append(paths, entry)
	for path := range open {
		paths = append(paths, path)
	}
	lib, libErr := libroot.Root()
	for _, path := range paths {
		if libErr == nil {
			stdlib := filepath.Join(lib, "stdlib") + string(filepath.Separator)
			if strings.HasPrefix(path, stdlib) {
				fresh["<stdlib>/"+filepath.ToSlash(strings.TrimPrefix(path, stdlib))] = true
			}
		}
		if rel, err := filepath.Rel(root, path); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			fresh[filepath.ToSlash(rel)] = true
		}
	}
	return fresh
}
