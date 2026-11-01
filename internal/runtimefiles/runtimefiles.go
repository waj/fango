// Package runtimefiles reads the Go support sources shipped with the Fango
// library and adapts their imports for a self-contained generated module.
package runtimefiles

import (
	"bytes"
	"fmt"
	"go/format"
	goparser "go/parser"
	gotoken "go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/waj/fango/internal/libroot"
)

const repositoryRuntime = "github.com/waj/fango/runtime/"

type File struct {
	Path string
	Data []byte
}

// Packages returns ordinary runtime package sources at their destinations in
// the private fangobuild module.
func (a *Adapter) Packages(names ...string) ([]File, error) {
	var requests []request
	for _, name := range names {
		rels, err := libroot.RuntimePackage(name)
		if err != nil {
			return nil, err
		}
		for _, rel := range rels {
			requests = append(requests, request{
				dest:   filepath.ToSlash(rel),
				name:   "runtime/" + rel,
				source: func() ([]byte, error) { return libroot.ReadRuntime(rel) },
				adapt:  forGeneratedModule,
			})
		}
	}
	files, err := a.run(requests)
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

func WorkerSources(internalPkgs, runtimePkgs []string) ([]File, error) {
	var files []File
	for _, pkg := range internalPkgs {
		rels, err := libroot.InternalPackage(pkg)
		if err != nil {
			return nil, err
		}
		for _, rel := range rels {
			data, err := libroot.ReadInternal(rel)
			if err != nil {
				return nil, err
			}
			files = append(files, File{Path: "internal/" + filepath.ToSlash(rel), Data: data})
		}
	}
	for _, pkg := range runtimePkgs {
		rels, err := libroot.RuntimePackage(pkg)
		if err != nil {
			return nil, err
		}
		for _, rel := range rels {
			data, err := libroot.ReadRuntime(rel)
			if err != nil {
				return nil, err
			}
			files = append(files, File{Path: "runtime/" + filepath.ToSlash(rel), Data: data})
		}
	}
	stdlib, err := libroot.StdlibGoFiles()
	if err != nil {
		return nil, err
	}
	for _, name := range stdlib {
		data, err := libroot.ReadStdlib(name)
		if err != nil {
			return nil, err
		}
		files = append(files, File{Path: "stdlib/" + name, Data: data})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

// nativeHostSource is the package-local host binding compiled beside every
// native sidecar.
const nativeHostSource = "native_support.go"

// Sidecar is one native sidecar's source and the generated directory it
// occupies, which ends in a slash.
type Sidecar struct {
	Dir    string
	Path   string
	Source []byte
}

// Sidecars returns each sidecar's adapted source and host binding, in order.
// The binding is read whether or not any sidecar needs it, so a library that
// has lost it is reported by every build.
func (a *Adapter) Sidecars(sidecars []Sidecar) ([]File, error) {
	host, err := libroot.ReadStdlib(nativeHostSource)
	if err != nil {
		return nil, err
	}
	// Every sidecar's binding is the same one, so a build that has to adapt it
	// adapts it once.
	adaptHost := sync.OnceValues(func() ([]byte, error) { return forGeneratedModule("stdlib/"+nativeHostSource, host) })
	requests := make([]request, 0, 2*len(sidecars))
	for _, sidecar := range sidecars {
		requests = append(requests,
			request{
				dest:   sidecar.Dir + "native.go",
				name:   sidecar.Path,
				source: func() ([]byte, error) { return sidecar.Source, nil },
				adapt:  adaptSidecar,
			},
			request{
				dest:   sidecar.Dir + "host.go",
				source: func() ([]byte, error) { return host, nil },
				adapt:  func(string, []byte) ([]byte, error) { return adaptHost() },
			})
	}
	return a.run(requests)
}

// RepositoryNativeHost returns the same binding without rewriting its import;
// the interpreter worker is itself a temporary github.com/waj/fango module.
func RepositoryNativeHost() ([]byte, error) {
	return libroot.ReadStdlib(nativeHostSource)
}

// adaptSidecar rewrites a sidecar's repository runtime imports as
// [forGeneratedModule] does; a sidecar may import
// `github.com/waj/fango/runtime/fangort`. A sidecar is the user's source rather
// than the library's, so it is formatted once more over what that leaves.
func adaptSidecar(path string, source []byte) ([]byte, error) {
	data, err := forGeneratedModule(path, source)
	if err != nil {
		return nil, err
	}
	if formatted, err := format.Source(data); err == nil {
		data = formatted
	}
	return data, nil
}

// forGeneratedModule rewrites repository runtime imports to their paths in the
// private generated module. Formatting is part of what it produces: a rewritten
// import sorts differently, and a source need not already be formatted.
func forGeneratedModule(path string, source []byte) ([]byte, error) {
	fset := gotoken.NewFileSet()
	file, err := goparser.ParseFile(fset, path, source, goparser.ParseComments)
	if err != nil {
		return nil, err
	}
	for _, spec := range file.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			return nil, fmt.Errorf("parse import in %s: %w", path, err)
		}
		if strings.HasPrefix(importPath, repositoryRuntime) {
			spec.Path.Value = strconv.Quote("fangobuild/" + strings.TrimPrefix(importPath, repositoryRuntime))
		}
	}
	var formatted bytes.Buffer
	if err := format.Node(&formatted, fset, file); err != nil {
		return nil, fmt.Errorf("format %s: %w", path, err)
	}
	return formatted.Bytes(), nil
}
