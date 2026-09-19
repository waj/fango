// Package modules discovers, validates, and resolves a local source-module
// graph before the existing single-program checker sees it.
package modules

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	goast "go/ast"
	goparser "go/parser"
	gotoken "go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	fango "github.com/waj/fango"
	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/fixity"
	"github.com/waj/fango/internal/lexer"
	"github.com/waj/fango/internal/natives"
	"github.com/waj/fango/internal/parser"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/types"
)

// Provider is the package-resolution seam shared by local and compiler-bundled
// modules.
type Provider interface {
	Source(module string) (path string, content []byte, err error)
	Native(module string) (path string, content []byte, err error)
}

// StageObserver is the test instrumentation seam for discovery work. A nil
// observer has no cost or user-visible output.
type StageObserver func(stage, owner string)

type FSProvider struct{ Root string }

type pathCaseError struct{ want, found string }

func (e pathCaseError) Error() string {
	return fmt.Sprintf("path casing mismatch: expected %s, found %s", e.want, e.found)
}

func (p FSProvider) Source(module string) (string, []byte, error) {
	rel := filepath.FromSlash(strings.ReplaceAll(module, ".", "/") + ".fango")
	b, err := readExact(p.Root, rel)
	return filepath.ToSlash(rel), b, err
}

func readExact(root, rel string) ([]byte, error) {
	path := filepath.Join(root, rel)
	absRoot, _ := filepath.Abs(root)
	absPath, _ := filepath.Abs(path)
	if absPath != absRoot && !strings.HasPrefix(absPath, absRoot+string(filepath.Separator)) {
		return nil, fmt.Errorf("module path escapes the source root")
	}
	cur := root
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		entries, readErr := os.ReadDir(cur)
		if readErr != nil {
			return nil, readErr
		}
		found := ""
		for _, e := range entries {
			if e.Name() == part {
				found = part
				break
			}
			if strings.EqualFold(e.Name(), part) {
				found = e.Name()
			}
		}
		if found != "" && found != part {
			return nil, pathCaseError{want: part, found: found}
		}
		cur = filepath.Join(cur, part)
	}
	return os.ReadFile(path)
}

func (p FSProvider) Native(module string) (string, []byte, error) {
	rel := filepath.FromSlash(strings.ReplaceAll(module, ".", "/") + ".native.go")
	b, err := readExact(p.Root, rel)
	return filepath.ToSlash(rel), b, err
}

type BundledProvider struct{}

func (BundledProvider) Source(module string) (string, []byte, error) {
	rel := strings.ReplaceAll(module, ".", "/") + ".fango"
	b, err := fs.ReadFile(fango.StdlibFS, "stdlib/"+rel)
	return "<stdlib>/" + rel, b, err
}
func (BundledProvider) Native(module string) (string, []byte, error) {
	rel := strings.ReplaceAll(module, ".", "/") + ".native.go"
	b, err := fs.ReadFile(fango.StdlibFS, "stdlib/"+rel)
	return "<stdlib>/" + rel, b, err
}

type ManifestEntry struct {
	Module string `json:"module"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type Result struct {
	Module     *ast.Module
	Entry      string
	FixityHash string
	Manifest   []ManifestEntry
	Units      []Unit
	Fixity     fixity.Table
	Natives    []NativeSource
}

type NativeSource struct {
	Module, Path string
	Content      []byte
}

// ValidateManifest rechecks the discovery-sensitive local filesystem facts in
// a cached manifest without parsing source. It deliberately uses the same
// exact-path provider rules as graph loading. Any malformed entry or I/O error
// is a miss.
func ValidateManifest(entry string, entries []ManifestEntry) bool {
	abs, err := filepath.Abs(entry)
	if err != nil {
		return false
	}
	root, entryPath := filepath.Dir(abs), filepath.Base(abs)
	local := FSProvider{Root: root}
	known := make(map[string]ManifestEntry, len(entries))
	entrySeen := false
	for _, in := range entries {
		if in.Path == "" || len(in.SHA256) != sha256.Size*2 {
			return false
		}
		if _, err := hex.DecodeString(in.SHA256); err != nil {
			return false
		}
		if _, exists := known[in.Path]; exists {
			return false
		}
		known[in.Path] = in
		if in.Path == filepath.ToSlash(entryPath) {
			entrySeen = true
		}
		if strings.HasPrefix(in.Path, "<stdlib>/") {
			if strings.HasSuffix(in.Path, ".fango") {
				_, _, localErr := local.Source(in.Module)
				if !errors.Is(localErr, fs.ErrNotExist) {
					return false
				}
			}
			continue
		}
		rel := filepath.Clean(filepath.FromSlash(in.Path))
		if filepath.IsAbs(rel) || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return false
		}
		b, readErr := readExact(root, rel)
		if readErr != nil {
			return false
		}
		h := sha256.Sum256(b)
		if hex.EncodeToString(h[:]) != in.SHA256 {
			return false
		}
	}
	if !entrySeen {
		return false
	}
	for path := range known {
		if strings.HasPrefix(path, "<stdlib>/") || !strings.HasSuffix(path, ".fango") {
			continue
		}
		native := strings.TrimSuffix(path, ".fango") + ".native.go"
		_, expected := known[native]
		_, readErr := readExact(root, filepath.FromSlash(native))
		switch {
		case readErr == nil && !expected:
			return false
		case errors.Is(readErr, fs.ErrNotExist) && expected:
			return false
		case readErr != nil && !errors.Is(readErr, fs.ErrNotExist):
			return false
		}
	}
	return true
}

// Unit is one source module in dependency-first build order. Name is empty
// for a headerless entry file; imports always contain logical named modules.
type Unit struct {
	Name    string
	Imports []string
	Entry   bool
}

type node struct {
	name, path   string
	content      []byte
	mod          *ast.Module
	parsed       *ParsedUnit
	sourceHash   string
	iface        *iface
	private      bool
	bundled      bool
	deps         []string
	nativePath   string
	native       []byte
	nativeModule string
	// resolved is the module's declaration list after name resolution, in
	// source order, as the checker consumes it.
	resolved []ast.Decl
}

type iface struct {
	values, types, ctors, ops, records map[string]string
	typeMembers, effectMembers         map[string][]string
	recordFields                       map[string][]string
	openTypes, openEffects             map[string]bool
}

func newIface() *iface {
	return &iface{values: map[string]string{}, types: map[string]string{}, ctors: map[string]string{}, ops: map[string]string{}, records: map[string]string{},
		typeMembers: map[string][]string{}, effectMembers: map[string][]string{}, recordFields: map[string][]string{}, openTypes: map[string]bool{}, openEffects: map[string]bool{}}
}

// Load uses the entry file's directory as the sole source root and returns a
// dependency-first merged surface program with canonical top-level names.
func Load(entry string) (*Result, []diag.Error) {
	return LoadWithOptions(entry, LoadOptions{})
}

// LoadObserved is Load with per-module parse and resolve notifications.
func LoadObserved(entry string, observe StageObserver) (*Result, []diag.Error) {
	return LoadWithOptions(entry, LoadOptions{Observe: observe})
}

type LoadOptions struct {
	Observe StageObserver
	Parsed  ParsedCache
}

// LoadWithOptions loads a batch graph with optional parsed-unit persistence
// and test instrumentation.
func LoadWithOptions(entry string, options LoadOptions) (*Result, []diag.Error) {
	abs, err := filepath.Abs(entry)
	if err != nil {
		return nil, []diag.Error{{Title: "SOURCE ERROR", Body: err.Error()}}
	}
	content, err := os.ReadFile(abs)
	if err != nil {
		return nil, []diag.Error{{Title: "SOURCE ERROR", Body: err.Error()}}
	}
	root := filepath.Dir(abs)
	f := source.NewFile(filepath.Base(abs), content)
	parsed, hit, errs := parseUnit(f, options.Parsed)
	if len(errs) > 0 {
		return nil, errs
	}
	m := parsed.mod
	entryName, private := "<entry>", !parsed.hasHeader
	if !private {
		entryName = parsed.header
	}
	if options.Observe != nil && !hit {
		options.Observe("parse", entryName)
	}
	g := newGraph(FSProvider{Root: root})
	g.observe = options.Observe
	g.parsed = options.Parsed
	if !private {
		if path, _, bundleErr := g.bundled.Source(entryName); bundleErr == nil {
			return nil, []diag.Error{diag.Errorf(m.Header.NameSpan, "RESERVED MODULE", "Module `%s` is bundled with Fango as `%s`; local modules cannot use bundled names.", entryName, path)}
		}
	}
	wantEntry := strings.TrimSuffix(filepath.Base(abs), filepath.Ext(abs))
	if !private && m.Header.Name != wantEntry {
		return nil, []diag.Error{diag.Errorf(m.Header.NameSpan, "MODULE/PATH MISMATCH", "The entry file `%s` must declare module `%s`, but declares `%s`.", filepath.Base(abs), wantEntry, m.Header.Name)}
	}
	rootNode := &node{name: entryName, path: filepath.Base(abs), content: content, mod: m, parsed: parsed, sourceHash: hashBytes(content), private: private, deps: parsedDependencies(parsed, entryName), nativeModule: wantEntry}
	rootNativePath := wantEntry + ".native.go"
	if nb, ne := os.ReadFile(filepath.Join(root, rootNativePath)); ne == nil {
		rootNode.nativePath, rootNode.native = rootNativePath, nb
	}
	pending := map[string]*node{entryName: rootNode}
	if errs := g.loadDeps(pending, rootNode, source.Span{}); len(errs) > 0 {
		return nil, errs
	}
	order, errs := g.complete(pending)
	if len(errs) > 0 {
		return nil, errs
	}
	merged := &ast.Module{InstanceImports: map[string]map[string]bool{}}
	for _, name := range order {
		owner := name
		if g.nodes[name].private {
			owner = ""
		}
		merged.InstanceImports[owner] = g.visible[name]
		merged.Decls = append(merged.Decls, g.nodes[name].resolved...)
	}
	manifest := make([]ManifestEntry, 0, len(order))
	units := make([]Unit, 0, len(order))
	var natives []NativeSource
	for _, name := range order {
		n := g.nodes[name]
		manifest = append(manifest, ManifestEntry{Module: name, Path: n.path, SHA256: n.sourceHash})
		if n.native != nil {
			nh := sha256.Sum256(n.native)
			manifest = append(manifest, ManifestEntry{Module: name, Path: n.nativePath, SHA256: hex.EncodeToString(nh[:])})
			natives = append(natives, NativeSource{Module: n.nativeModule, Path: n.nativePath, Content: n.native})
		}
		if name == PreludeModule {
			// The prelude declares nothing, so it has no code to emit. It
			// stays in the manifest, where its hash invalidates a build when
			// the default scope changes.
			continue
		}
		unitName := name
		if n.private {
			unitName = ""
		}
		units = append(units, Unit{Name: unitName, Imports: explicitDependencyNames(n), Entry: name == entryName})
	}
	entrySymbol := "main"
	if !private {
		entrySymbol = canonical(entryName, "main")
	}
	return &Result{Module: merged, Entry: entrySymbol, FixityHash: fixity.Hash(g.fixities), Manifest: manifest, Units: units, Fixity: g.fixities, Natives: natives}, nil
}

func parseUnit(f *source.File, cache ParsedCache) (*ParsedUnit, bool, []diag.Error) {
	hash := hashBytes(f.Content)
	if cache != nil {
		if data, ok := cache.LoadParsed(hash); ok {
			if unit, err := decodeParsed(data, hash, f); err == nil {
				return unit, true, nil
			}
		}
	}
	m, errs := parse(f)
	if len(errs) > 0 {
		return nil, false, errs
	}
	unit := newParsedUnit(m)
	if cache != nil {
		if data, err := encodeParsed(unit, hash); err == nil {
			cache.StoreParsed(hash, data)
		}
	}
	return unit, false, nil
}

func hashBytes(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// PreludeModule declares the default scope. It holds nothing but imports,
// and a module that does not carry `{-# no-prelude #-}` resolves as though
// those imports stood at the top of its own file — qualified access
// included, since they are ordinary imports. Keeping the list in Fango
// rather than in this package is what stops the batch resolver and the
// REPL's scope from drifting apart.
const PreludeModule = "Prelude"

// MetaModule is the bundled module that owns the abstract compile-time code
// type. A file using `quote` or `$(…)` needs it in the graph to have a type
// for its quotes, so the loader adds the dependency where the syntax appears
// rather than taxing every program with it.
const MetaModule = "Meta"

// DeriveModule supplies the derivers for the standard classes. A file that
// writes `deriving` needs them in the graph for the same reason a file that
// writes `quote` needs Meta: the loader adds the edge where the syntax
// appears rather than taxing every program with it.
const DeriveModule = "Derive"

// ListModule owns the List.Nil/List.Cons constructors used by bracket syntax.
const ListModule = "List"

// TupleModule owns the Pair/Triple types and constructors used by `(a, b)`.
const TupleModule = "Tuple"

func parsedDependencies(u *ParsedUnit, self string) []string {
	var deps []string
	if !u.noPrelude && self != PreludeModule {
		deps = append(deps, PreludeModule)
	}
	if u.staging && self != MetaModule {
		deps = addDep(deps, MetaModule)
	}
	if self != DeriveModule && u.deriving {
		deps = addDep(deps, DeriveModule)
	}
	if u.lists && self != ListModule {
		deps = addDep(deps, ListModule)
	}
	if u.tuples && self != TupleModule {
		deps = addDep(deps, TupleModule)
	}
	return deps
}

func usesDeriving(m *ast.Module) bool {
	for _, d := range m.Decls {
		if td, ok := d.(*ast.TypeDecl); ok && len(td.Deriving) > 0 {
			return true
		}
	}
	return false
}

func addDep(deps []string, name string) []string {
	for _, d := range deps {
		if d == name {
			return deps
		}
	}
	return append(deps, name)
}

func parse(f *source.File) (*ast.Module, []diag.Error) {
	toks, errs := lexer.Lex(f)
	if len(errs) > 0 {
		return nil, errs
	}
	return parser.Parse(toks, f)
}

var nativePlaceholder = regexp.MustCompile(`\$([0-9]+)`)

// validateModuleDecls checks the declarations whose rules are per-module
// rather than graph-wide.
//
// The Go boundary stays deliberately small: bundled modules may use inline
// templates, while both bundled and ordinary modules may use sidecar call
// form with a closed scalar ABI that can be checked without running Go
// tooling. Fixity is checked here too, because a fixity must accompany the
// operator its own module declares.
func validateModuleDecls(n *node) []diag.Error {
	var errs []diag.Error
	if n.name == PreludeModule && len(n.mod.Decls) > 0 {
		// Prelude is a scope directive, not a library: it declares the
		// default imports and nothing else. Holding it to that is what lets
		// every build skip emitting a unit for it.
		errs = append(errs, diag.Error{Title: "INVALID EMBEDDED PRELUDE", Body: n.path + " may contain only imports."})
	}
	callDecls := map[string]*ast.ValueDecl{}
	// opDecls marks the call-form natives that are effect operations; only
	// those may use the fallible result shape (doc/design.md, "Go backend and
	// runtime").
	opDecls := map[string]bool{}
	// declared is every value name this module introduces — the names its
	// own fixity declarations may refer to.
	declared := map[string]bool{}
	fixities := map[string]source.Span{}
	for _, d := range n.mod.Decls {
		switch d := d.(type) {
		case *ast.ClassDecl:
			for _, m := range d.Methods {
				declared[m.Name] = true
			}
		case *ast.ValueDecl:
			declared[d.Name] = true
			if d.Native == nil {
				continue
			}
			if types.Intrinsic(canonical(n.name, d.Name)) {
				// A compiler intrinsic is neither a template nor a sidecar call:
				// elaboration gives it a Core body, so it needs no Go function and
				// no entry in the interpreter native registry.
				if !n.bundled {
					errs = append(errs, diag.Errorf(d.Native.Sp, "RESERVED NATIVE IDENTIFIER", "`%s.%s` names a compiler intrinsic and cannot be declared here.", n.name, d.Name))
				}
				if d.Native.Template != nil {
					errs = append(errs, diag.Errorf(d.Native.Sp, "NATIVE TEMPLATE NOT ALLOWED", "The compiler intrinsic `%s.%s` has no Go template.", n.name, d.Name))
				}
				continue
			}
			if d.Native.Template != nil {
				if !n.bundled {
					errs = append(errs, diag.Errorf(d.Native.Sp, "NATIVE TEMPLATE NOT ALLOWED", "Inline native templates are reserved for compiler-bundled modules; use `native` with a sidecar function."))
					continue
				}
				errs = append(errs, validateTemplate(*d.Native.Template, nativeArity(d.Ann), d.Native.Sp)...)
				if spec, ok := natives.Lookup(canonical(n.name, d.Name)); !ok || spec.Arity != nativeArity(d.Ann) || spec.Effect {
					errs = append(errs, diag.Errorf(d.Native.Sp, "INVALID BUNDLED NATIVE", "Bundled native `%s.%s` does not match the interpreter registry.", n.name, d.Name))
				}
			} else {
				callDecls[d.Name] = d
				if n.bundled {
					if spec, ok := natives.Lookup(canonical(n.name, d.Name)); !ok || spec.Arity != nativeArity(d.Ann) || spec.Effect {
						errs = append(errs, diag.Errorf(d.Native.Sp, "INVALID BUNDLED NATIVE", "Bundled native `%s.%s` does not match the interpreter registry.", n.name, d.Name))
					}
				}
			}
		case *ast.EffectDecl:
			if name := canonical(n.name, d.Name); n.bundled && (name == types.StreamYieldEffectName || name == types.IteratorTraversalEffectName) {
				d.CompilerSuspension = true
			}
			for _, op := range d.Ops {
				declared[op.Name] = true
				if op.Native == nil {
					continue
				}
				if op.Native.Template == nil {
					callDecls[op.Name] = &ast.ValueDecl{Name: op.Name, NameSpan: op.NameSpan,
						Ann: &ast.TypeAnn{Type: op.Type}, Native: op.Native}
					opDecls[op.Name] = true
				} else {
					if !n.bundled {
						errs = append(errs, diag.Errorf(op.Native.Sp, "NATIVE TEMPLATE NOT ALLOWED", "Inline native templates are reserved for compiler-bundled modules; use `native` with a sidecar function."))
						continue
					}
					errs = append(errs, validateTemplate(*op.Native.Template, typeArity(op.Type), op.Native.Sp)...)
					if spec, ok := natives.Lookup(canonical(n.name, op.Name)); !ok || spec.Arity != typeArity(op.Type) || !spec.Effect {
						errs = append(errs, diag.Errorf(op.Native.Sp, "INVALID BUNDLED NATIVE", "Bundled native `%s.%s` does not match the interpreter registry.", n.name, op.Name))
					}
				}
			}
		case *ast.FixityDecl:
			if _, exists := fixities[d.Op]; exists {
				errs = append(errs, diag.Errorf(d.OpSpan, "DUPLICATE FIXITY", "Operator `(%s)` already has a fixity in this module.", d.Op))
			}
			fixities[d.Op] = d.OpSpan
		}
	}
	// A fixity must sit with the operator's own declaration. That keeps one
	// declarer per operator, which is what makes the graph-wide table
	// unambiguous, and it lets the declaration appear above or below the
	// definition without a visibility question.
	for _, d := range n.mod.Decls {
		fd, ok := d.(*ast.FixityDecl)
		if !ok || declared[fd.Op] {
			continue
		}
		errs = append(errs, diag.Errorf(fd.OpSpan, "FIXITY WITHOUT DEFINITION",
			"Module `%s` does not declare `(%s)`, so it cannot declare its fixity.\nFixity belongs with the operator's own declaration.", n.name, fd.Op))
	}
	if len(callDecls) == 0 {
		if n.native != nil {
			errs = append(errs, diag.Errorf(source.Span{}, "ORPHAN NATIVE SIDECAR", "Module `%s` has `%s`, but declares no call-form natives.", n.name, n.nativePath))
		}
		return errs
	}
	if n.native == nil {
		for _, d := range callDecls {
			errs = append(errs, diag.Errorf(d.Native.Sp, "MISSING NATIVE SIDECAR", "Native `%s` requires `%s.native.go` beside the module source.", d.Name, n.name))
		}
		return errs
	}
	return append(errs, validateSidecar(n, callDecls, opDecls)...)
}

func nativeArity(ann *ast.TypeAnn) int {
	if ann == nil {
		return 0
	}
	return typeArity(ann.Type)
}

func typeArity(t ast.TypeExpr) int {
	n := 0
	for {
		f, ok := t.(*ast.TFunExpr)
		if !ok {
			return n
		}
		n++
		t = f.Ret
	}
}

func validateTemplate(template string, arity int, sp source.Span) []diag.Error {
	var errs []diag.Error
	counts := make([]int, arity)
	for _, m := range nativePlaceholder.FindAllStringSubmatch(template, -1) {
		i, _ := strconv.Atoi(m[1])
		if i < 1 || i > arity {
			errs = append(errs, diag.Errorf(sp, "NATIVE TEMPLATE PLACEHOLDER", "Template placeholder `$%d` is outside this native's arity %d.", i, arity))
		} else {
			counts[i-1]++
		}
	}
	for i, count := range counts {
		if count != 1 {
			errs = append(errs, diag.Errorf(sp, "NATIVE TEMPLATE PLACEHOLDER", "Template must use `$%d` exactly once; found %d uses.", i+1, count))
		}
	}
	s := strings.ReplaceAll(strings.ReplaceAll(template, "$eq", "__fango_eq"), "$show", "__fango_show")
	for i := arity; i >= 1; i-- {
		s = strings.ReplaceAll(s, fmt.Sprintf("$%d", i), fmt.Sprintf("__fango_p%d", i))
	}
	x, err := goparser.ParseExpr(s)
	if err != nil {
		return append(errs, diag.Errorf(sp, "INVALID NATIVE TEMPLATE", "The native template is not a Go expression: %v.", err))
	}
	selectorNames := map[*goast.Ident]bool{}
	goast.Inspect(x, func(node goast.Node) bool {
		if s, ok := node.(*goast.SelectorExpr); ok {
			selectorNames[s.Sel] = true
			if id, ok := s.X.(*goast.Ident); !ok || id.Name != "fangort" {
				errs = append(errs, diag.Errorf(sp, "NATIVE TEMPLATE IDENTIFIER", "Only the `fangort` qualifier is allowed in a native template."))
			}
		}
		if call, ok := node.(*goast.CallExpr); ok {
			if id, ok := call.Fun.(*goast.Ident); ok {
				want := -1
				if id.Name == "__fango_eq" {
					want = 2
				} else if id.Name == "__fango_show" {
					want = 1
				}
				if want >= 0 && len(call.Args) != want {
					errs = append(errs, diag.Errorf(sp, "NATIVE TEMPLATE INTRINSIC", "Template intrinsic requires %d argument(s).", want))
				} else if want >= 0 {
					for _, arg := range call.Args {
						placeholder, ok := arg.(*goast.Ident)
						if !ok || !strings.HasPrefix(placeholder.Name, "__fango_p") {
							errs = append(errs, diag.Errorf(sp, "NATIVE TEMPLATE INTRINSIC", "Template intrinsics accept positional placeholders directly."))
							break
						}
					}
				}
			}
		}
		return true
	})
	allowed := map[string]bool{
		"fangort": true, "__fango_eq": true, "__fango_show": true,
		"true": true, "false": true, "nil": true,
		"append": true, "cap": true, "clear": true, "close": true, "complex": true,
		"copy": true, "delete": true, "imag": true, "len": true, "make": true,
		"max": true, "min": true, "new": true, "panic": true, "print": true,
		"println": true, "real": true, "recover": true, "float64": true,
	}
	goast.Inspect(x, func(node goast.Node) bool {
		id, ok := node.(*goast.Ident)
		if !ok || selectorNames[id] || allowed[id.Name] || strings.HasPrefix(id.Name, "__fango_p") {
			return true
		}
		errs = append(errs, diag.Errorf(sp, "NATIVE TEMPLATE IDENTIFIER", "Identifier `%s` is not allowed in a native template.", id.Name))
		return true
	})
	return errs
}

func validateSidecar(n *node, decls map[string]*ast.ValueDecl, opDecls map[string]bool) []diag.Error {
	f, err := goparser.ParseFile(gotoken.NewFileSet(), n.nativePath, n.native, 0)
	if err != nil {
		return []diag.Error{diag.Errorf(source.Span{}, "INVALID NATIVE SIDECAR", "%s does not parse as Go: %v.", n.nativePath, err)}
	}
	var errs []diag.Error
	if f.Name.Name != "native" {
		errs = append(errs, diag.Errorf(source.Span{}, "NATIVE PACKAGE NAME", "%s must declare `package native`.", n.nativePath))
	}
	for _, im := range f.Imports {
		path, _ := strconv.Unquote(im.Path.Value)
		first := strings.Split(path, "/")[0]
		if strings.Contains(first, ".") {
			errs = append(errs, diag.Errorf(source.Span{}, "NATIVE IMPORT NOT ALLOWED", "Native sidecar %s may import only Go standard-library packages; `%s` is external.", n.nativePath, path))
		}
	}
	funcs := map[string]*goast.FuncDecl{}
	for _, d := range f.Decls {
		if gd, ok := d.(*goast.GenDecl); ok {
			for _, raw := range gd.Specs {
				switch spec := raw.(type) {
				case *goast.TypeSpec:
					if spec.Name.Name == "FangoHost" || spec.Name.Name == "FangoNativeHost" {
						errs = append(errs, diag.Errorf(source.Span{}, "RESERVED NATIVE IDENTIFIER", "%s declares generated identifier `%s`.", n.nativePath, spec.Name.Name))
					}
				case *goast.ValueSpec:
					for _, name := range spec.Names {
						if name.Name == "FangoHost" || name.Name == "FangoNativeHost" {
							errs = append(errs, diag.Errorf(source.Span{}, "RESERVED NATIVE IDENTIFIER", "%s declares generated identifier `%s`.", n.nativePath, name.Name))
						}
					}
				}
			}
		}
		if fn, ok := d.(*goast.FuncDecl); ok && fn.Recv == nil && goast.IsExported(fn.Name.Name) {
			if fn.Name.Name == "FangoHost" || fn.Name.Name == "FangoNativeHost" {
				errs = append(errs, diag.Errorf(source.Span{}, "RESERVED NATIVE IDENTIFIER", "%s declares generated identifier `%s`.", n.nativePath, fn.Name.Name))
				continue
			}
			funcs[fn.Name.Name] = fn
		}
	}
	used := map[string]bool{}
	boundary := nativeBoundary{wrappers: localWrapperTypes(n.mod.Decls), fallibleAllowed: n.bundled && n.name == fallibleNativeModule}
	for name, d := range decls {
		goName := exportNativeName(name)
		fn := funcs[goName]
		if fn == nil {
			errs = append(errs, diag.Errorf(d.Native.Sp, "MISSING NATIVE FUNCTION", "Native `%s` requires exported function `%s` in %s.", name, goName, n.nativePath))
			continue
		}
		used[goName] = true
		errs = append(errs, boundary.validateNativeShape(d, fn, opDecls[name])...)
	}
	for name := range funcs {
		if !used[name] {
			errs = append(errs, diag.Errorf(source.Span{}, "ORPHAN NATIVE FUNCTION", "Exported function `%s` in %s has no call-form native declaration.", name, n.nativePath))
		}
	}
	return errs
}

func exportNativeName(name string) string {
	if name == "" {
		return ""
	}
	return strings.ToUpper(name[:1]) + name[1:]
}

// nativeBoundary is what module validation knows about a sidecar's boundary
// before name resolution: the module's own single-scalar wrapper types, and
// whether it may declare fallible results. Recognition is by spelling, which
// is safe here because a wrapper must be declared in this very file and the
// fallible shape is admitted only in the module the compiler controls; type
// checking re-establishes both shapes on resolved types.
type nativeBoundary struct {
	wrappers        map[string]string // local wrapper type name -> Go scalar type
	fallibleAllowed bool
}

// localWrapperTypes finds the module's `type T = T Scalar` declarations: one
// constructor, one boundary-scalar field, no parameters, not a record.
func localWrapperTypes(decls []ast.Decl) map[string]string {
	out := map[string]string{}
	for _, d := range decls {
		td, ok := d.(*ast.TypeDecl)
		if !ok || len(td.Params) != 0 || td.RecordFields != nil || len(td.Ctors) != 1 || len(td.Ctors[0].Args) != 1 {
			continue
		}
		if goType := scalarGoType(td.Ctors[0].Args[0]); goType != "" {
			out[td.Name] = goType
		}
	}
	return out
}

// fallibleNativeModule is the one bundled module whose sidecar may return Go
// errors: its natives are the file operations behind the scoped File API.
const fallibleNativeModule = "File"

// fallibleResult recognizes the spelling `Result IO.Error T` (or `Result
// Error T` once IO's Error is imported unqualified), answering T.
func fallibleResult(t ast.TypeExpr) (ast.TypeExpr, bool) {
	app, ok := t.(*ast.TApp)
	if !ok || app.Name != "Result" || len(app.Args) != 2 {
		return nil, false
	}
	if errTy, ok := app.Args[0].(*ast.TName); !ok || errTy.Name != "Error" && errTy.Name != "IO.Error" {
		return nil, false
	}
	return app.Args[1], true
}

func (b nativeBoundary) validateNativeShape(d *ast.ValueDecl, fn *goast.FuncDecl, fromOp bool) []diag.Error {
	if d.Ann == nil {
		return nil // parser already diagnoses the missing annotation
	}
	var params []ast.TypeExpr
	t := d.Ann.Type
	for {
		f, ok := t.(*ast.TFunExpr)
		if !ok {
			break
		}
		if !isUnitType(f.Arg) {
			params = append(params, f.Arg)
		}
		t = f.Ret
	}
	got := fieldCount(fn.Type.Params)
	if got != len(params) {
		return []diag.Error{diag.Errorf(d.Native.Sp, "NATIVE ABI", "Function `%s` has %d parameter(s); native `%s` requires %d after Unit erasure.", fn.Name.Name, got, d.Name, len(params))}
	}
	var errs []diag.Error
	i := 0
	for _, field := range fn.Type.Params.List {
		count := len(field.Names)
		if count == 0 {
			count = 1
		}
		for range count {
			if want := b.nativeGoType(params[i]); want == "" || goTypeName(field.Type) != want {
				errs = append(errs, diag.Errorf(d.Native.Sp, "NATIVE ABI", "Parameter %d of `%s` must use the scalar Go type for its Fango annotation, or a type this module declares as a single-constructor wrapper around one.", i+1, fn.Name.Name))
			}
			i++
		}
	}
	if payload, fallible := fallibleResult(t); fallible {
		if !b.fallibleAllowed || fromOp {
			errs = append(errs, diag.Errorf(d.Native.Sp, "FALLIBLE NATIVE NOT ALLOWED", "Only value natives of the bundled `File` module may declare a `Result IO.Error` result; return a scalar and build the `Result` in Fango."))
			return errs
		}
		results := resultTypeNames(fn.Type.Results)
		if isUnitType(payload) {
			if len(results) != 1 || results[0] != "error" {
				errs = append(errs, diag.Errorf(d.Native.Sp, "NATIVE ABI", "Function `%s` must return exactly `error` for native `%s`'s `Result Error ()` annotation.", fn.Name.Name, d.Name))
			}
		} else if want := b.nativeGoType(payload); want == "" || len(results) != 2 || results[0] != want || results[1] != "error" {
			errs = append(errs, diag.Errorf(d.Native.Sp, "NATIVE ABI", "Function `%s` must return `(%s, error)` for native `%s`'s fallible annotation.", fn.Name.Name, orGoType(want), d.Name))
		}
	} else if isUnitType(t) {
		if fieldCount(fn.Type.Results) != 0 {
			errs = append(errs, diag.Errorf(d.Native.Sp, "NATIVE ABI", "Function `%s` must return no value for Fango Unit.", fn.Name.Name))
		}
	} else if fieldCount(fn.Type.Results) != 1 || len(fn.Type.Results.List) != 1 || goTypeName(fn.Type.Results.List[0].Type) != b.nativeGoType(t) {
		errs = append(errs, diag.Errorf(d.Native.Sp, "NATIVE ABI", "Function `%s` must return exactly the scalar Go type in native `%s`'s annotation, or a type this module declares as a single-constructor wrapper around one.", fn.Name.Name, d.Name))
	}
	if fn.Type.TypeParams != nil {
		errs = append(errs, diag.Errorf(d.Native.Sp, "NATIVE ABI", "Function `%s` cannot declare Go type parameters.", fn.Name.Name))
	}
	return errs
}

func fieldCount(fs *goast.FieldList) int {
	if fs == nil {
		return 0
	}
	n := 0
	for _, f := range fs.List {
		if len(f.Names) == 0 {
			n++
		} else {
			n += len(f.Names)
		}
	}
	return n
}

func isUnitType(t ast.TypeExpr) bool {
	n, ok := t.(*ast.TName)
	return ok && n.Name == "()"
}

// nativeGoType is the Go type a Fango boundary type crosses as: a scalar's
// own Go type, or the field type of one of this module's wrapper types.
func (b nativeBoundary) nativeGoType(t ast.TypeExpr) string {
	if goType := scalarGoType(t); goType != "" {
		return goType
	}
	n, ok := t.(*ast.TName)
	if !ok {
		return ""
	}
	return b.wrappers[n.Name]
}

func scalarGoType(t ast.TypeExpr) string {
	n, ok := t.(*ast.TName)
	if !ok {
		return ""
	}
	return map[string]string{"Int": "int64", "Float": "float64", "String": "string", "Char": "rune", "Bool": "bool"}[n.Name]
}

func orGoType(goType string) string {
	if goType == "" {
		return "scalar"
	}
	return goType
}

// resultTypeNames lists a Go result list's type spellings, one per result.
func resultTypeNames(fs *goast.FieldList) []string {
	if fs == nil {
		return nil
	}
	var out []string
	for _, f := range fs.List {
		count := len(f.Names)
		if count == 0 {
			count = 1
		}
		for range count {
			out = append(out, goTypeName(f.Type))
		}
	}
	return out
}

func goTypeName(e goast.Expr) string {
	if id, ok := e.(*goast.Ident); ok {
		return id.Name
	}
	return ""
}

func dependencyNames(n *node) []string {
	seen := map[string]bool{}
	var out []string
	for _, dep := range n.deps {
		if dep != n.name && !seen[dep] {
			seen[dep] = true
			out = append(out, dep)
		}
	}
	for _, im := range n.mod.Imports {
		if !seen[im.Module] {
			seen[im.Module] = true
			out = append(out, im.Module)
		}
	}
	return out
}

func explicitDependencyNames(n *node) []string {
	seen := map[string]bool{}
	var out []string
	for _, im := range n.mod.Imports {
		if im.Module != n.name && !seen[im.Module] {
			seen[im.Module] = true
			out = append(out, im.Module)
		}
	}
	return out
}

func topo(nodes map[string]*node) []string {
	indegree, users := map[string]int{}, map[string][]string{}
	for name, n := range nodes {
		indegree[name] = 0
		for _, dep := range dependencyNames(n) {
			// A dependency outside this map is already ordered elsewhere
			// (the REPL orders each import's new modules against a graph it
			// has already committed), so it never blocks a node here.
			if nodes[dep] == nil {
				continue
			}
			indegree[name]++
			users[dep] = append(users[dep], name)
		}
	}
	var ready []string
	for name, n := range indegree {
		if n == 0 {
			ready = append(ready, name)
		}
	}
	var out []string
	for len(ready) > 0 {
		sort.Strings(ready)
		name := ready[0]
		ready = ready[1:]
		out = append(out, name)
		for _, user := range users[name] {
			indegree[user]--
			if indegree[user] == 0 {
				ready = append(ready, user)
			}
		}
	}
	return out
}

func canonical(module, name string) string { return module + "." + name }

func buildInterface(n *node, errs []diag.Error) (*iface, []diag.Error) {
	if n.private {
		return newIface(), errs
	}
	all := newIface()
	for _, d := range n.mod.Decls {
		switch d := d.(type) {
		case *ast.ClassDecl:
			all.types[d.Name] = canonical(n.name, d.Name)
			for _, m := range d.Methods {
				all.effectMembers[d.Name] = append(all.effectMembers[d.Name], m.Name)
				all.values[m.Name] = canonical(n.name, m.Name)
			}
		case *ast.ValueDecl:
			all.values[d.Name] = canonical(n.name, d.Name)
		case *ast.PatternDecl:
			for _, b := range patternBinders(d.Pattern) {
				all.values[b.Name] = canonical(n.name, b.Name)
			}
		case *ast.TypeDecl:
			all.types[d.Name] = canonical(n.name, d.Name)
			if d.RecordFields != nil {
				all.records[d.Name] = canonical(n.name, d.Name)
				for _, f := range d.RecordFields {
					all.recordFields[d.Name] = append(all.recordFields[d.Name], f.Name)
				}
			}
			for _, c := range d.Ctors {
				all.typeMembers[d.Name] = append(all.typeMembers[d.Name], c.Name)
				all.ctors[c.Name] = canonical(n.name, c.Name)
			}
		case *ast.EffectDecl:
			all.types[d.Name] = canonical(n.name, d.Name)
			for _, op := range d.Ops {
				all.effectMembers[d.Name] = append(all.effectMembers[d.Name], op.Name)
				all.ops[op.Name] = canonical(n.name, op.Name)
				all.values[op.Name] = canonical(n.name, op.Name)
			}
		}
	}
	pub := newIface()
	ex := n.mod.Header.Exposing
	for _, decl := range n.mod.Decls {
		td, ok := decl.(*ast.TypeDecl)
		if !ok || !td.Resource {
			continue
		}
		if ex.All {
			errs = append(errs, diag.Errorf(td.ResourceSpan, "RESOURCE REPRESENTATION EXPOSED", "Resource `%s` must be exported opaquely; replace `exposing (..)` with an explicit export list.", td.Name))
		}
		for _, item := range ex.Items {
			if item.Name == td.Name && item.All {
				errs = append(errs, diag.Errorf(item.Sp, "RESOURCE REPRESENTATION EXPOSED", "Expose resource `%s` without `(..)` so its representation stays private.", td.Name))
			}
		}
	}
	if ex.All {
		for k, v := range all.values {
			pub.values[k] = v
		}
		for k, v := range all.types {
			pub.types[k] = v
		}
		for k, v := range all.ctors {
			pub.ctors[k] = v
		}
		for k, v := range all.ops {
			pub.ops[k] = v
		}
		for k, v := range all.records {
			pub.records[k] = v
			pub.recordFields[k] = append([]string(nil), all.recordFields[k]...)
		}
		for k, v := range all.typeMembers {
			pub.typeMembers[k] = v
			pub.openTypes[k] = true
		}
		for k, v := range all.effectMembers {
			pub.effectMembers[k] = v
			pub.openEffects[k] = true
		}
		return pub, errs
	}
	seen := map[string]bool{}
	for _, item := range ex.Items {
		if seen[item.Name] {
			errs = append(errs, diag.Errorf(item.Sp, "DUPLICATE EXPORT", "`%s` appears more than once in the exposing list.", item.Name))
			continue
		}
		seen[item.Name] = true
		if v, ok := all.values[item.Name]; ok && !item.All {
			pub.values[item.Name] = v
			if op := all.ops[item.Name]; op != "" {
				pub.ops[item.Name] = op
			}
			continue
		}
		if v, ok := all.types[item.Name]; ok {
			pub.types[item.Name] = v
			if item.All {
				if rv, ok := all.records[item.Name]; ok {
					pub.records[item.Name] = rv
					pub.recordFields[item.Name] = append([]string(nil), all.recordFields[item.Name]...)
					pub.openTypes[item.Name] = true
					continue
				}
				if ms, ok := all.typeMembers[item.Name]; ok {
					pub.typeMembers[item.Name] = ms
					pub.openTypes[item.Name] = true
					for _, x := range ms {
						pub.ctors[x] = all.ctors[x]
					}
					continue
				}
				if ms, ok := all.effectMembers[item.Name]; ok {
					pub.effectMembers[item.Name] = ms
					pub.openEffects[item.Name] = true
					for _, x := range ms {
						if op := all.ops[x]; op != "" {
							pub.ops[x] = op
						}
						pub.values[x] = all.values[x]
					}
					continue
				}
			}
			continue
		}
		if _, ctor := all.ctors[item.Name]; ctor {
			errs = append(errs, diag.Errorf(item.Sp, "INVALID EXPORT", "Constructors cannot be exported individually; expose their type with `%s(..)`.", owner(all.typeMembers, item.Name)))
			continue
		}
		errs = append(errs, diag.Errorf(item.Sp, "UNKNOWN EXPORT", "Module `%s` has no declaration named `%s`.", n.name, item.Name))
	}
	return pub, errs
}

func owner(m map[string][]string, member string) string {
	for n, xs := range m {
		for _, x := range xs {
			if x == member {
				return n
			}
		}
	}
	return "Type"
}

func (i *iface) selection(ex *ast.Exposing, at source.Span) (*iface, []diag.Error) {
	if ex == nil {
		return newIface(), nil
	}
	if ex.All {
		return i, nil
	}
	out := newIface()
	var errs []diag.Error
	for _, item := range ex.Items {
		if v, ok := i.values[item.Name]; ok && !item.All {
			out.values[item.Name] = v
			if op := i.ops[item.Name]; op != "" {
				out.ops[item.Name] = op
			}
			continue
		}
		if v, ok := i.types[item.Name]; ok {
			out.types[item.Name] = v
			if item.All {
				if rv, ok := i.records[item.Name]; ok {
					out.records[item.Name] = rv
					out.recordFields[item.Name] = append([]string(nil), i.recordFields[item.Name]...)
					out.openTypes[item.Name] = true
					continue
				}
				if !i.openTypes[item.Name] && !i.openEffects[item.Name] {
					errs = append(errs, diag.Errorf(item.Sp, "NON-PUBLIC IMPORT", "Module does not publicly expose the members of `%s`.", item.Name))
					continue
				}
				if i.openTypes[item.Name] {
					out.openTypes[item.Name] = true
					for _, x := range i.typeMembers[item.Name] {
						out.ctors[x] = i.ctors[x]
					}
				}
				if i.openEffects[item.Name] {
					out.openEffects[item.Name] = true
					for _, x := range i.effectMembers[item.Name] {
						if op := i.ops[x]; op != "" {
							out.ops[x] = op
						}
						out.values[x] = i.values[x]
					}
				}
			}
			continue
		}
		errs = append(errs, diag.Errorf(item.Sp, "UNKNOWN IMPORT", "The imported module does not publicly expose `%s`.", item.Name))
	}
	return out, errs
}

type resolver struct {
	node                           *node
	nodes                          map[string]*node
	errs                           []diag.Error
	vals, tys, ctors, ops, records map[string]string
	recordLabels                   map[string][]string
	quals                          map[string]*iface

	// seenModules, aliases, and fullQualifiers guard the duplicate-import
	// diagnostics. Only a module's own imports populate them: the prelude's
	// are implicit, so importing a prelude module explicitly is ordinary.
	seenModules, aliases, fullQualifiers map[string]bool

	// schemas holds the canonical types whose constructors or record fields
	// this module may read. `typeOf` copies it, which is the whole modularity
	// story for reflection: `exposing (T)` reflects opaque and
	// `exposing (T(..))` reflects in full, exactly as those two forms
	// already govern patterns and field access.
	schemas map[string]bool

	// prompt marks the REPL's resolver, whose module is never complete: a
	// prompt may redefine its own names (a rebinding to the same canonical
	// name is not a collision) and may import the same module again for
	// more names, alias included.
	prompt      bool
	predeclared map[string]*ast.ValueDecl
}

func (r *resolver) canon(name string) string {
	if r.node.private {
		return name
	}
	return canonical(r.node.name, name)
}

// preludeImports returns the import list every module resolves as though it
// had written itself. A module that opted out, and the prelude itself, get
// none — which is also what keeps the bundled library below the prelude from
// importing its way into a cycle.
func (r *resolver) preludeImports() []ast.Import {
	if r.node.mod.NoPrelude || r.node.name == PreludeModule {
		return nil
	}
	prelude := r.nodes[PreludeModule]
	if prelude == nil {
		return nil
	}
	return prelude.mod.Imports
}

// applyImports seeds the scope from a list of imports. recordSeen reports
// whether these are the module's own, and so subject to the duplicate-import
// and duplicate-alias diagnostics.
func (r *resolver) applyImports(imports []ast.Import, recordSeen bool) {
	for _, im := range imports {
		if recordSeen {
			if r.seenModules[im.Module] {
				r.errs = append(r.errs, diag.Errorf(im.ModuleSpan, "DUPLICATE IMPORT", "Module `%s` is imported more than once.", im.Module))
				continue
			}
			r.seenModules[im.Module] = true
		}
		dep := r.nodes[im.Module]
		if dep == nil {
			continue
		}
		r.quals[im.Module] = dep.iface
		// Qualified access reaches a public schema without an exposing entry,
		// so reflection follows the same reach.
		r.exposeSchemas(dep.iface)
		for record, canonicalName := range dep.iface.records {
			for _, field := range dep.iface.recordFields[record] {
				r.recordLabels[field] = append(r.recordLabels[field], canonicalName)
			}
		}
		if im.Alias != "" {
			if r.prompt && r.quals[im.Alias] == dep.iface {
				// The prompt imported this module under this alias before;
				// repeating it adds names, and the qualifier already agrees.
			} else if r.aliases[im.Alias] || r.fullQualifiers[im.Alias] || r.quals[im.Alias] != nil {
				r.errs = append(r.errs, diag.Errorf(im.AliasSpan, "DUPLICATE IMPORT ALIAS", "The qualifier `%s` is already in use.", im.Alias))
			} else {
				r.aliases[im.Alias] = true
				r.quals[im.Alias] = dep.iface
			}
		}
		sel, es := dep.iface.selection(im.Exposing, im.ModuleSpan)
		r.errs = append(r.errs, es...)
		r.merge(sel, im.ModuleSpan)
	}
}

// resolve canonicalizes a whole module: its scope is seeded from the prelude
// and its own imports, its type-level names are registered module-wide, and
// then every declaration is rewritten in place.
func (r *resolver) resolve() ([]ast.Decl, []diag.Error) {
	r.init()
	// The prelude's imports are applied first and are ordinary imports, so a
	// module reaches `print` and `IO.write` alike without writing one. They
	// are not recorded as seen, which leaves the module free to import the
	// same module again for more names; `add` accepts a repeated binding at
	// an identical canonical name.
	r.applyImports(r.preludeImports(), false)
	r.applyImports(r.node.mod.Imports, true)
	r.declareHeaders(r.node.mod.Decls)
	r.predeclared = map[string]*ast.ValueDecl{}
	if !r.prompt {
		for _, decl := range r.node.mod.Decls {
			if d, ok := decl.(*ast.ValueDecl); ok && len(d.Params) > 0 && d.Native == nil {
				if _, exists := r.vals[d.Name]; exists {
					r.errs = append(r.errs, diag.Errorf(d.NameSpan, "UNQUALIFIED COLLISION", "The value `%s` collides with an exposed import, operation, or existing declaration.", d.Name))
				} else {
					r.add(r.vals, d.Name, r.canon(d.Name), d.NameSpan)
					r.predeclared[d.Name] = d
				}
			}
		}
	}
	return r.resolveDecls(r.node.mod.Decls), r.errs
}

// init seeds the scope with the builtin names every module sees.
func (r *resolver) init() {
	r.vals = map[string]string{}
	r.tys = map[string]string{"Int": "Int", "Float": "Float", "String": "String", "Char": "Char", "Bool": "Bool", "()": "()"}
	r.ctors = map[string]string{"True": "True", "False": "False"}
	r.ops = map[string]string{}
	r.records = map[string]string{}
	r.recordLabels = map[string][]string{}
	r.schemas = map[string]bool{"Bool": true}
	r.quals = map[string]*iface{}
	r.aliases, r.seenModules = map[string]bool{}, map[string]bool{}
	r.fullQualifiers = map[string]bool{}
	for _, im := range r.node.mod.Imports {
		r.fullQualifiers[im.Module] = true
	}
}

// declareHeaders registers types, effects, constructors, and operations
// module-wide, matching the checker's existing mutually-recursive
// declaration pass.
func (r *resolver) declareHeaders(decls []ast.Decl) {
	for _, d := range decls {
		switch d := d.(type) {
		case *ast.TypeDecl:
			r.add(r.tys, d.Name, r.canon(d.Name), d.NameSpan)
			r.schemas[r.canon(d.Name)] = true
			if d.RecordFields != nil {
				r.add(r.records, d.Name, r.canon(d.Name), d.NameSpan)
				for _, f := range d.RecordFields {
					r.recordLabels[f.Name] = append(r.recordLabels[f.Name], r.canon(d.Name))
				}
			}
			for _, c := range d.Ctors {
				r.add(r.ctors, c.Name, r.canon(c.Name), c.NameSpan)
			}
		case *ast.EffectDecl:
			r.add(r.tys, d.Name, r.canon(d.Name), d.NameSpan)
			for _, o := range d.Ops {
				r.add(r.vals, o.Name, r.canon(o.Name), o.NameSpan)
				r.add(r.ops, o.Name, r.canon(o.Name), o.NameSpan)
			}
		}
	}
}

// resolveDecls rewrites each declaration's names to their canonical form and
// binds the values it declares, in source order.
func (r *resolver) resolveDecls(decls []ast.Decl) []ast.Decl {
	var out []ast.Decl
	for _, d := range decls {
		switch d := d.(type) {
		case *ast.ClassDecl:
			r.add(r.tys, d.Name, r.canon(d.Name), d.NameSpan)
			for _, m := range d.Methods {
				if r.predeclared[m.Name] != nil {
					r.errs = append(r.errs, diag.Errorf(m.NameSpan, "UNQUALIFIED COLLISION", "Method `%s` collides with a module function.", m.Name))
				}
				r.add(r.vals, m.Name, r.canon(m.Name), m.NameSpan)
			}
			d.Name = r.canon(d.Name)
			for i := range d.Methods {
				d.Methods[i].Name = r.canon(d.Methods[i].Name)
				r.typ(d.Methods[i].Type)
			}
			out = append(out, d)
		case *ast.DeriverDecl:
			// A deriver resolves in its own module's scope, exactly as an
			// instance does: the code it quotes is the quoting module's, so
			// the splice site can neither capture nor be captured.
			d.Owner = ""
			if !r.node.private {
				d.Owner = r.node.name
			}
			d.Class = r.qualified(d.Class, r.tys, "type", d.ClassSpan)
			for _, m := range d.Methods {
				r.resolveValueRows(m, r.vals)
			}
			out = append(out, d)
		case *ast.InstanceDecl:
			d.Owner = ""
			if !r.node.private {
				d.Owner = r.node.name
			}
			r.predicate(&d.Head)
			r.instanceMethodsVisible(d)
			for i := range d.Preds {
				r.predicate(&d.Preds[i])
			}
			for _, m := range d.Methods {
				r.resolveValueRows(m, r.vals)
			}
			out = append(out, d)
		case *ast.ValueDecl:
			surface := d.Name
			canon := r.canon(surface)
			if existing, exists := r.vals[surface]; exists && (!r.prompt || existing != canon) && r.predeclared[surface] != d {
				r.errs = append(r.errs, diag.Errorf(d.NameSpan, "UNQUALIFIED COLLISION", "The value `%s` collides with an exposed import or operation.", d.Name))
			}
			visible := clone(r.vals)
			if len(d.Params) > 0 {
				visible[surface] = canon
			}
			r.typeAnn(d.Ann)
			if d.Native != nil {
				d.Native.Module = r.node.nativeModule
			}
			r.resolveValueRows(d, visible)
			d.Name = canon
			r.vals[surface] = canon
			out = append(out, d)
		case *ast.PatternDecl:
			// Pins and the RHS see only the outer scope. Binders are installed
			// simultaneously after both have been resolved.
			r.expr(d.Body, r.vals, map[string]bool{})
			locals := map[string]bool{}
			r.pattern(d.Pattern, locals, r.vals)
			for _, b := range patternBinders(d.Pattern) {
				surface := b.Name
				canon := r.canon(surface)
				if existing, exists := r.vals[surface]; exists && (!r.prompt || existing != canon) {
					r.errs = append(r.errs, diag.Errorf(b.Sp, "UNQUALIFIED COLLISION", "The value `%s` collides with an exposed import or existing declaration.", b.Name))
				}
				b.Name = canon
				r.vals[surface] = canon
			}
			out = append(out, d)
		case *ast.FixityDecl:
			// A fixity binds a spelling, not a value, so Op is never
			// canonicalized. It survives resolution so the REPL can rebuild
			// its table from the merged prelude.
			out = append(out, d)
		case *ast.TypeDecl:
			d.Name = r.canon(d.Name)
			for i := range d.Deriving {
				d.Deriving[i].Name = r.qualified(d.Deriving[i].Name, r.tys, "type", d.Deriving[i].Sp)
			}
			for i := range d.Ctors {
				d.Ctors[i].Name = r.canon(d.Ctors[i].Name)
				for _, a := range d.Ctors[i].Args {
					r.typ(a)
				}
			}
			for i := range d.RecordFields {
				r.typ(d.RecordFields[i].Type)
			}
			out = append(out, d)
		case *ast.EffectDecl:
			d.Name = r.canon(d.Name)
			for i := range d.Ops {
				if d.Ops[i].Native != nil {
					d.Ops[i].Native.Module = r.node.nativeModule
				}
				d.Ops[i].Name = r.canon(d.Ops[i].Name)
				r.typ(d.Ops[i].Type)
			}
			out = append(out, d)
		}
	}
	return out
}

// An abstract exported class can constrain clients, but an instance needs
// access to every method. Qualified access counts, independently of exposing.
func (r *resolver) instanceMethodsVisible(d *ast.InstanceDecl) {
	class := d.Head.Class
	i := strings.LastIndexByte(class, '.')
	if i < 0 || class[:i] == r.node.name || class[:i] == "Basics" {
		return
	}
	n := r.nodes[class[:i]]
	if n == nil {
		return
	}
	for _, decl := range n.mod.Decls {
		cl, ok := decl.(*ast.ClassDecl)
		if !ok || (cl.Name != class && canonical(n.name, cl.Name) != class) {
			continue
		}
		for _, m := range cl.Methods {
			name := m.Name
			if !strings.Contains(name, ".") {
				name = canonical(n.name, name)
			}
			visible := false
			for _, iface := range r.quals {
				for _, value := range iface.values {
					if value == name {
						visible = true
					}
				}
			}
			if !visible {
				r.errs = append(r.errs, diag.Errorf(d.Head.Sp, "NON-PUBLIC METHOD", "An instance of `%s` requires access to method `%s`; import a public class interface exposing all methods.", class, name))
			}
		}
	}
}

func clone(m map[string]string) map[string]string {
	n := map[string]string{}
	for k, v := range m {
		n[k] = v
	}
	return n
}
func (r *resolver) add(m map[string]string, k, v string, sp source.Span) {
	if old, ok := m[k]; ok {
		if old == v {
			return
		}
		r.errs = append(r.errs, diag.Errorf(sp, "UNQUALIFIED COLLISION", "The name `%s` collides with an exposed import or builtin.", k))
		return
	}
	m[k] = v
}

// exposeSchemas records the types i lets a reader see the insides of.
func (r *resolver) exposeSchemas(i *iface) {
	for name := range i.openTypes {
		if v := i.types[name]; v != "" {
			r.schemas[v] = true
		}
	}
	for _, v := range i.records {
		r.schemas[v] = true
	}
}

func (r *resolver) merge(i *iface, sp source.Span) {
	r.exposeSchemas(i)
	for k, v := range i.values {
		r.add(r.vals, k, v, sp)
	}
	for k, v := range i.types {
		r.add(r.tys, k, v, sp)
	}
	for k, v := range i.ctors {
		r.add(r.ctors, k, v, sp)
	}
	for k, v := range i.ops {
		if old := r.ops[k]; old != "" && old != v {
			r.errs = append(r.errs, diag.Errorf(sp, "UNQUALIFIED COLLISION", "The operation `%s` is exposed by more than one import.", k))
		} else {
			r.ops[k] = v
		}
	}
	for k, v := range i.records {
		r.add(r.records, k, v, sp)
		for _, field := range i.recordFields[k] {
			r.recordLabels[field] = append(r.recordLabels[field], v)
		}
	}
}

func (r *resolver) qualified(name string, ns map[string]string, kind string, sp source.Span) string {
	if !strings.Contains(name, ".") {
		if v := ns[name]; v != "" {
			return v
		}
		return name
	}
	best := ""
	var in *iface
	for q, i := range r.quals {
		if strings.HasPrefix(name, q+".") && len(q) > len(best) {
			best, in = q, i
		}
	}
	if in == nil {
		r.errs = append(r.errs, diag.Errorf(sp, "UNKNOWN QUALIFIER", "I don't know a value named `%s`: no imported module has qualifier `%s`.", name, name[:strings.LastIndex(name, ".")]))
		return name
	}
	member := strings.TrimPrefix(name, best+".")
	var v string
	switch kind {
	case "value":
		v = in.values[member]
	case "type":
		v = in.types[member]
	case "ctor":
		v = in.ctors[member]
	case "op":
		v = in.ops[member]
	case "record":
		v = in.records[member]
	}
	if v == "" {
		r.errs = append(r.errs, diag.Errorf(sp, "PRIVATE OR UNKNOWN NAME", "Module qualifier `%s` does not publicly expose `%s`.", best, member))
		return name
	}
	return v
}

func (r *resolver) typ(t ast.TypeExpr) {
	switch t := t.(type) {
	case *ast.TName:
		t.Name = r.qualified(t.Name, r.tys, "type", t.Sp)
	case *ast.TApp:
		if !t.Sugared {
			t.Name = r.qualified(t.Name, r.tys, "type", t.NameSp)
		}
		for _, a := range t.Args {
			r.typ(a)
		}
	case *ast.TFunExpr:
		r.typ(t.Arg)
		r.typ(t.Ret)
		if t.Eff != nil {
			for i := range t.Eff.Labels {
				l := &t.Eff.Labels[i]
				l.Name = r.qualified(l.Name, r.tys, "type", l.NameSp)
				for _, a := range l.Args {
					r.typ(a)
				}
			}
		}
	}
}
func (r *resolver) typeAnn(a *ast.TypeAnn) {
	if a != nil {
		r.typ(a.Type)
		for i := range a.Preds {
			r.predicate(&a.Preds[i])
		}
	}
}

func (r *resolver) predicate(p *ast.PredExpr) {
	p.Class = r.qualified(p.Class, r.tys, "type", p.Sp)
	r.typ(p.Ty)
}

func (r *resolver) expr(e ast.Expr, vals map[string]string, locals map[string]bool) {
	switch e := e.(type) {
	case *ast.Var:
		if !locals[e.Name] {
			e.Name = r.qualified(e.Name, vals, "value", e.Sp)
		}
	case *ast.Ctor:
		if !e.Sugared {
			e.Name = r.qualified(e.Name, r.ctors, "ctor", e.Sp)
		}
	case *ast.RecordLit:
		// An inferred literal has no name to resolve; its field labels carry the
		// visibility instead, the way a projection's do.
		if e.Name != "" {
			e.Name = r.qualified(e.Name, r.records, "record", e.NameSpan)
		}
		for i := range e.Fields {
			f := &e.Fields[i]
			f.Records = append([]string{}, r.recordLabels[f.Name]...)
			r.expr(f.Value, vals, locals)
		}
	case *ast.RecordGet:
		e.Records = append([]string{}, r.recordLabels[e.Field]...)
		r.expr(e.Record, vals, locals)
	case *ast.RecordUpdate:
		r.expr(e.Record, vals, locals)
		for i := range e.Fields {
			f := &e.Fields[i]
			f.Records = append([]string{}, r.recordLabels[f.Name]...)
			r.expr(f.Value, vals, locals)
		}
	case *ast.App:
		r.expr(e.Fn, vals, locals)
		r.expr(e.Arg, vals, locals)
	case *ast.Quote:
		// A quote resolves in the quoting module's scope, always. That is the
		// whole hygiene story: canonical symbols are fixed here, before
		// inference, so a splice site can neither capture nor be captured.
		r.expr(e.Body, vals, locals)
	case *ast.Splice:
		r.expr(e.Operand, vals, locals)
	case *ast.Resume:
		if e.NextState != nil {
			r.expr(e.NextState, vals, locals)
		}
	case *ast.TypeOf:
		r.typ(e.Ty)
		e.Visible = r.schemas
	case *ast.Neg:
		r.expr(e.Operand, vals, locals)
	case *ast.If:
		r.expr(e.Cond, vals, locals)
		r.expr(e.Then, vals, locals)
		r.expr(e.Else, vals, locals)
	case *ast.BinOp:
		// An operator is an ordinary value name, so it resolves like one.
		// `&&` and `||` are the exception: they are surface syntax that
		// elaborates to an `if`, and name nothing.
		if !fixity.IsShortCircuit(e.Op) {
			e.Op = r.qualified(e.Op, vals, "value", e.OpSpan)
		}
		r.expr(e.L, vals, locals)
		r.expr(e.R, vals, locals)
	case *ast.Lambda:
		ls := copySet(locals)
		r.patternVector(e.Params, ls, vals)
		r.expr(e.Body, vals, ls)
	case *ast.Block:
		ls := copySet(locals)
		for i := range e.Binds {
			b := &e.Binds[i]
			if b.Pattern != nil {
				r.expr(b.Body, vals, ls)
				r.pattern(b.Pattern, ls, vals)
				continue
			}
			r.checkBinder(b.Name, b.NameSpan, vals)
			r.typeAnn(b.Ann)
			inner := copySet(ls)
			if len(b.Equations) > 0 {
				for _, eq := range b.Equations {
					row := copySet(ls)
					row[b.Name] = true
					r.patternVector(eq.Params, row, vals)
					r.expr(eq.Body, vals, row)
				}
			} else {
				r.patternVector(b.Params, inner, vals)
				if len(b.Params) > 0 {
					inner[b.Name] = true
				}
				r.expr(b.Body, vals, inner)
			}
			ls[b.Name] = true
		}
		for _, it := range e.Items {
			if it.Expr != nil {
				r.expr(it.Expr, vals, ls)
			}
		}
		r.expr(e.Result, vals, ls)
	case *ast.Case:
		r.expr(e.Scrutinee, vals, locals)
		for i := range e.Branches {
			ls := copySet(locals)
			r.pattern(e.Branches[i].Pattern, ls, vals)
			r.expr(e.Branches[i].Body, vals, ls)
		}
	case *ast.Handle:
		r.expr(e.Body, vals, locals)
		if e.State != nil {
			r.expr(e.State.Initial, vals, locals)
		}
		for i := range e.Clauses {
			c := &e.Clauses[i]
			c.Op = r.qualified(c.Op, r.ops, "op", c.OpSpan)
			if len(c.Equations) > 0 {
				for _, eq := range c.Equations {
					ls := copySet(locals)
					if e.State != nil {
						ls[e.State.Name] = true
					}
					r.patternVector(eq.Params, ls, vals)
					r.expr(eq.Body, vals, ls)
				}
			} else {
				ls := copySet(locals)
				if e.State != nil {
					ls[e.State.Name] = true
				}
				r.patternVector(c.Params, ls, vals)
				r.expr(c.Body, vals, ls)
			}
		}
		if e.Return != nil {
			if len(e.Return.Equations) > 0 {
				for _, eq := range e.Return.Equations {
					ls := copySet(locals)
					if e.State != nil {
						ls[e.State.Name] = true
					}
					r.patternVector(eq.Params, ls, vals)
					r.expr(eq.Body, vals, ls)
				}
			} else {
				ls := copySet(locals)
				if e.State != nil {
					ls[e.State.Name] = true
				}
				r.pattern(e.Return.Param, ls, vals)
				r.expr(e.Return.Body, vals, ls)
			}
		}
	}
}
func copySet(m map[string]bool) map[string]bool {
	n := map[string]bool{}
	for k, v := range m {
		n[k] = v
	}
	return n
}
func (r *resolver) checkBinder(name string, sp source.Span, vals map[string]string) {
	if vals[name] != "" {
		r.errs = append(r.errs, diag.Errorf(sp, "SHADOWING", "The local name `%s` shadows an imported, builtin, or top-level value.", name))
	}
}

func patternBinders(p ast.Pattern) []*ast.PVar {
	var out []*ast.PVar
	var walk func(ast.Pattern)
	walk = func(p ast.Pattern) {
		switch p := p.(type) {
		case *ast.PVar:
			out = append(out, p)
		case *ast.PCtor:
			for _, a := range p.Args {
				walk(a)
			}
		case *ast.PRecord:
			for _, f := range p.Fields {
				walk(f.Pattern)
			}
		}
	}
	walk(p)
	return out
}

func (r *resolver) resolveValueRows(d *ast.ValueDecl, vals map[string]string) {
	if len(d.Equations) == 0 {
		locals := map[string]bool{}
		r.patternVector(d.Params, locals, vals)
		if d.Body != nil {
			r.expr(d.Body, vals, locals)
		}
		return
	}
	for i := range d.Equations {
		eq := &d.Equations[i]
		locals := map[string]bool{}
		r.patternVector(eq.Params, locals, vals)
		r.expr(eq.Body, vals, locals)
	}
}

func (r *resolver) pattern(p ast.Pattern, locals map[string]bool, vals map[string]string) {
	outer := copySet(locals)
	r.patternInner(p, locals, outer, vals)
}

func (r *resolver) patternVector(ps []ast.Pattern, locals map[string]bool, vals map[string]string) {
	outer := copySet(locals)
	for _, p := range ps {
		r.patternInner(p, locals, outer, vals)
	}
}

func (r *resolver) patternInner(p ast.Pattern, locals, outer map[string]bool, vals map[string]string) {
	switch p := p.(type) {
	case *ast.PVar:
		r.checkBinder(p.Name, p.Sp, vals)
		locals[p.Name] = true
	case *ast.PPin:
		if !outer[p.Name] {
			p.Name = r.qualified(p.Name, vals, "value", p.NameSpan)
		}
	case *ast.PCtor:
		if !p.Sugared {
			p.Name = r.qualified(p.Name, r.ctors, "ctor", p.NameSpan)
		}
		for _, a := range p.Args {
			r.patternInner(a, locals, outer, vals)
		}
	case *ast.PRecord:
		if p.Name != "" {
			p.Name = r.qualified(p.Name, r.records, "record", p.NameSpan)
		}
		for i := range p.Fields {
			f := &p.Fields[i]
			f.Records = append([]string{}, r.recordLabels[f.Name]...)
			r.patternInner(f.Pattern, locals, outer, vals)
		}
	}
}

func ManifestJSON(entries []ManifestEntry) []byte {
	b, _ := json.MarshalIndent(entries, "", "  ")
	return append(b, '\n')
}
