// Package runtimefiles reads the Go support sources shipped inside the fango
// binary and adapts their imports for a self-contained generated module.
package runtimefiles

import (
	"bytes"
	"fmt"
	"go/format"
	goparser "go/parser"
	gotoken "go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	fango "github.com/waj/fango"
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
		entries, err := fs.ReadDir(fango.RuntimeFS, "runtime/"+name)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if entry.IsDir() || strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}
			path := "runtime/" + name + "/" + entry.Name()
			data, err := fs.ReadFile(fango.RuntimeFS, path)
			if err != nil {
				return nil, err
			}
			data, err = forGeneratedModule(path, data)
			if err != nil {
				return nil, err
			}
			files = append(files, File{Path: filepath.ToSlash(filepath.Join(name, entry.Name())), Data: data})
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

// NativeHost returns the package-local host binding compiled beside every
// native sidecar.
func NativeHost() ([]byte, error) {
	const path = "stdlib/native_support.go"
	data, err := fs.ReadFile(fango.StdlibFS, path)
	if err != nil {
		return nil, err
	}
	return forGeneratedModule(path, data)
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
