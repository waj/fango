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

	"github.com/waj/fango/internal/libroot"
)

const repositoryRuntime = "github.com/waj/fango/runtime/"

type File struct {
	Path string
	Data []byte
}

// Packages returns ordinary runtime package sources at their destinations in
// the private fangobuild module.
func Packages(names ...string) ([]File, error) {
	var files []File
	for _, name := range names {
		rels, err := libroot.RuntimePackage(name)
		if err != nil {
			return nil, err
		}
		for _, rel := range rels {
			data, err := libroot.ReadRuntime(rel)
			if err != nil {
				return nil, err
			}
			data, err = forGeneratedModule("runtime/"+rel, data)
			if err != nil {
				return nil, err
			}
			files = append(files, File{Path: filepath.ToSlash(rel), Data: data})
		}
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

// NativeHost returns the package-local host binding compiled beside every
// native sidecar.
func NativeHost() ([]byte, error) {
	const rel = "native_support.go"
	data, err := libroot.ReadStdlib(rel)
	if err != nil {
		return nil, err
	}
	return forGeneratedModule("stdlib/"+rel, data)
}

// RepositoryNativeHost returns the same binding without rewriting its import;
// the interpreter worker is itself a temporary github.com/waj/fango module.
func RepositoryNativeHost() ([]byte, error) {
	return libroot.ReadStdlib("native_support.go")
}

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
