package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"

	"github.com/waj/fango/internal/apidoc"
	compilecheck "github.com/waj/fango/internal/check"
	"github.com/waj/fango/internal/libroot"
	"github.com/waj/fango/internal/modules"
	"github.com/waj/fango/internal/source"
)

type moduleList []string

func (l *moduleList) String() string { return strings.Join(*l, ",") }
func (l *moduleList) Set(name string) error {
	*l = append(*l, name)
	return nil
}

// cmdDoc writes the API reference of the standard library, or of one library
// of the library root, as JSON to stdout: each module's public interface, its
// checked signatures, and the comments above each declaration. Diagnostics go
// to stderr.
func cmdDoc(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("doc", flag.ContinueOnError)
	fs.SetOutput(stderr)
	stdlib := fs.Bool("stdlib", false, "document the bundled standard library")
	library := fs.String("lib", "", "document the library `name` of the library root")
	strict := fs.Bool("strict", false, "fail when a selected module or public declaration has no documentation")
	var selected moduleList
	fs.Var(&selected, "module", "document only this module (repeatable)")
	if fs.Parse(args) != nil || fs.NArg() != 0 {
		usage(stderr)
		return 2
	}
	if *stdlib == (*library != "") {
		fmt.Fprintln(stderr, "fango doc: exactly one of --stdlib or --lib is required; only the bundled standard library and the libraries of the library root can be documented")
		return 2
	}
	var libraries []string
	what := "standard-library"
	available, err := modules.BundledModules()
	if *library != "" {
		if errs := modules.CheckLibraries([]string{*library}); report(stderr, errs) {
			return 1
		}
		libraries, what = []string{*library}, "`"+*library+"` library"
		available, err = modules.LibraryProvider{Name: *library}.Modules()
	}
	if err != nil {
		reportInternal(stderr, err)
		return 1
	}
	if len(selected) == 0 {
		selected = available
	}
	unknown := false
	for _, name := range selected {
		if !slices.Contains(available, name) {
			fmt.Fprintf(stderr, "fango doc: unknown %s module %q\n", what, name)
			unknown = true
		}
	}
	if unknown {
		return 2
	}
	result, ok := checkLibrary(available, libraries, stderr)
	if !ok {
		return 1
	}
	doc, missing := apidoc.Extract(result, apidoc.Options{Modules: selected, Path: docPath})
	if *strict && len(missing) > 0 {
		for _, m := range missing {
			fmt.Fprintln(stderr, m)
		}
		fmt.Fprintf(stderr, "fango doc: %d missing documentation comment(s)\n", len(missing))
		return 1
	}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(doc); err != nil {
		reportInternal(stderr, err)
		return 1
	}
	if _, err := stdout.Write(out.Bytes()); err != nil {
		fmt.Fprintf(stderr, "fango doc: %v\n", err)
		return 1
	}
	return 0
}

// checkLibrary checks every module of the documented library together, so
// instance lists do not depend on which modules were selected. The entry is a
// synthetic headerless file that only imports them; it is never written
// anywhere. Libraries names the enabled library being documented, if any.
func checkLibrary(names, libraries []string, stderr io.Writer) (*compilecheck.Result, bool) {
	root, err := libroot.Root()
	if err != nil {
		reportInternal(stderr, err)
		return nil, false
	}
	var entry strings.Builder
	for _, name := range names {
		fmt.Fprintf(&entry, "import %s\n", name)
	}
	dir := filepath.Join(root, "stdlib")
	path := filepath.Join(dir, "<fango doc>.fango")
	session := &compilecheck.Session{DisableObjectCache: true, LoadOptions: modules.LoadOptions{
		Root: dir, Overlays: map[string][]byte{path: []byte(entry.String())}, BundledOnly: true, Libraries: libraries}}
	result, errs, internalErr := session.Compile(path)
	if report(stderr, errs) {
		return nil, false
	}
	if internalErr != nil {
		reportInternal(stderr, internalErr)
		return nil, false
	}
	return result, true
}

// docPath reports a library file by its repository path, the same wherever
// the library is installed.
func docPath(f *source.File) string {
	if f == nil {
		return ""
	}
	if rest, ok := strings.CutPrefix(f.Name, "<stdlib>/"); ok {
		return "stdlib/" + rest
	}
	if rest, ok := strings.CutPrefix(f.Name, modules.LibraryPrefix); ok {
		return "libs/" + rest
	}
	return filepath.ToSlash(f.Name)
}
