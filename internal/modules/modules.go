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
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/compileevent"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/fixity"
	"github.com/waj/fango/internal/lexer"
	"github.com/waj/fango/internal/libroot"
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

// StageObserver is the instrumentation seam for discovery work. A nil
// observer has no cost or user-visible output.
type StageObserver = compileevent.Observer

type FSProvider struct {
	Root string
}

// OverlayProvider reads unsaved editor content at the same logical paths as
// FSProvider. Keys are absolute, cleaned filesystem paths.
type OverlayProvider struct {
	FSProvider
	Overlays map[string][]byte
}

func (p OverlayProvider) Source(module string) (string, []byte, error) {
	rel := filepath.FromSlash(strings.ReplaceAll(module, ".", "/") + ".fango")
	if b, ok := p.Overlays[filepath.Clean(filepath.Join(p.Root, rel))]; ok {
		if _, err := readExact(p.Root, rel); err != nil && !errors.Is(err, os.ErrNotExist) {
			return filepath.ToSlash(rel), nil, err
		}
		return filepath.ToSlash(rel), b, nil
	}
	return p.FSProvider.Source(module)
}

func (p OverlayProvider) Native(module string) (string, []byte, error) {
	return p.FSProvider.Native(module)
}

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
	var casing *pathCaseError
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
		if found == "" {
			return nil, os.ErrNotExist
		}
		if found != part && casing == nil {
			casing = &pathCaseError{want: part, found: found}
		}
		cur = filepath.Join(cur, found)
	}
	if casing != nil {
		return nil, *casing
	}
	return os.ReadFile(path)
}

func (p FSProvider) Native(module string) (string, []byte, error) {
	rel := filepath.FromSlash(strings.ReplaceAll(module, ".", "/") + ".native.go")
	b, err := readExact(p.Root, rel)
	return filepath.ToSlash(rel), b, err
}

// BundledProvider serves the standard library from the resolved library
// root. It reports paths under the logical <stdlib>/ prefix rather than the
// root they were read from, so a build manifest identifies a bundled module
// the same way wherever the library is installed.
type BundledProvider struct{}

func (BundledProvider) Source(module string) (string, []byte, error) {
	rel := strings.ReplaceAll(module, ".", "/") + ".fango"
	b, err := libroot.ReadStdlib(rel)
	return "<stdlib>/" + rel, b, err
}
func (BundledProvider) Native(module string) (string, []byte, error) {
	rel := strings.ReplaceAll(module, ".", "/") + ".native.go"
	b, err := libroot.ReadStdlib(rel)
	return "<stdlib>/" + rel, b, err
}

type ManifestEntry struct {
	Module string `json:"module"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type Result struct {
	Module     *ast.Module
	Modules    []ResolvedModule
	Entry      string
	FixityHash string
	Manifest   []ManifestEntry
	Units      []Unit
	Fixity     fixity.Table
	Natives    []NativeSource
}

// ModuleRole makes entry-only language obligations explicit. In particular,
// a dependency's declaration named main is an ordinary value.
type ModuleRole uint8

const (
	DependencyRole ModuleRole = iota
	EntryRole
)

// ResolvedModule is one independently checkable module in dependency-first
// order. Module contains only declarations owned by Name (or the headerless
// entry), while InstanceImports retains that owner's graph visibility.
type ResolvedModule struct {
	Name       string
	Role       ModuleRole
	Entry      string
	Source     *source.File
	SourceHash string
	// NativeModule names the owner's sidecar package. A headerless entry's
	// sidecar is named after its file rather than its empty module name.
	NativeModule string
	NativeHash   string
	Dependencies []string
	Interface    Interface
	Module       *ast.Module
}

// resolvedModule is one module as a checker takes it in. Batch entry graphs
// and prompt import increments describe their modules the same way, so both
// reach the same per-module artifact identity.
func (g *Graph) resolvedModule(name, owner string, role ModuleRole, entry string) ResolvedModule {
	n := g.nodes[name]
	var moduleSource *source.File
	if n.mod != nil && n.mod.Header != nil {
		moduleSource = n.mod.Header.NameSpan.File
	}
	if moduleSource == nil {
		moduleSource = firstDeclFile(n.resolved)
	}
	deps := dependencyNames(n)
	sort.Strings(deps)
	nativeHash := ""
	if n.native != nil {
		h := sha256.Sum256(n.native)
		nativeHash = hex.EncodeToString(h[:])
	}
	return ResolvedModule{Name: owner, Role: role, Entry: entry, Source: moduleSource, SourceHash: n.sourceHash,
		NativeModule: n.nativeModule, NativeHash: nativeHash, Dependencies: deps, Interface: exportInterface(n.iface),
		Module: &ast.Module{
			Header:          n.mod.Header,
			Imports:         append([]ast.Import(nil), n.mod.Imports...),
			Decls:           append([]ast.Decl(nil), n.resolved...),
			InstanceImports: map[string]map[string]bool{owner: g.visible[name]},
		}}
}

// Interface is the resolver-visible public surface of one module. Private
// declarations never enter these maps; caching and installation therefore
// cannot widen source visibility.
type Interface struct {
	Values, Types, Ctors, Operations, Records map[string]string
	TypeMembers, EffectMembers                map[string][]string
	RecordFields                              map[string][]string
	OpenTypes, OpenEffects                    map[string]bool
}

type NativeSource struct {
	Module, Path string
	Content      []byte
}

// Unit is one source module in dependency-first build order. Name is empty
// for a headerless entry file; imports always contain logical named modules.
// Program is the entry file's stem, set on the entry unit only — the same
// string the entry's cached artifacts are named after.
type Unit struct {
	Name    string
	Program string
	Imports []string
	Entry   bool
}

type node struct {
	name, path   string
	content      []byte
	mod          *ast.Module
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

type LoadOptions struct {
	Observe StageObserver
	// Root defaults to the entry file's directory. Editor clients may set it
	// when opening an imported module in a nested directory.
	Root string
	// Overlays override disk content for open local files, including entry.
	Overlays map[string][]byte
	// AllowBundledEntry lets editor analysis open a file at its actual bundled
	// stdlib path without treating its module name as a local collision.
	AllowBundledEntry bool
}

// LoadWithOptions loads a batch graph with optional test instrumentation.
func LoadWithOptions(entry string, options LoadOptions) (*Result, []diag.Error) {
	abs, err := filepath.Abs(entry)
	if err != nil {
		return nil, []diag.Error{{Title: "SOURCE ERROR", Body: err.Error()}}
	}
	content, present := options.Overlays[filepath.Clean(abs)]
	if !present {
		content, err = os.ReadFile(abs)
	}
	if err != nil {
		return nil, []diag.Error{{Title: "SOURCE ERROR", Body: err.Error()}}
	}
	root := options.Root
	if root == "" {
		root = filepath.Dir(abs)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return nil, []diag.Error{{Title: "SOURCE ERROR", Body: err.Error()}}
	}
	relEntry, err := filepath.Rel(root, abs)
	if err != nil || relEntry == ".." || strings.HasPrefix(relEntry, ".."+string(filepath.Separator)) {
		return nil, []diag.Error{{Title: "SOURCE ERROR", Body: "entry is outside its source root"}}
	}
	f := source.NewFile(filepath.ToSlash(relEntry), content)
	parseStart := time.Now()
	m, errs := parse(f)
	if len(errs) > 0 {
		return nil, errs
	}
	entryName, private := "<entry>", m.Header == nil
	if !private {
		entryName = m.Header.Name
	}
	options.Observe.Timed("parse", entryName, parseStart)
	var provider Provider = FSProvider{Root: root}
	if options.Overlays != nil {
		provider = OverlayProvider{FSProvider: FSProvider{Root: root}, Overlays: options.Overlays}
	}
	g := newGraph(provider)
	g.observe = options.Observe
	bundledEntry := false
	if !private {
		if path, _, bundleErr := g.bundled.Source(entryName); bundleErr == nil {
			if options.AllowBundledEntry {
				if lib, libErr := libroot.Root(); libErr == nil {
					bundledEntry = filepath.Clean(abs) == filepath.Join(lib, "stdlib", filepath.FromSlash(strings.TrimPrefix(path, "<stdlib>/")))
				}
			}
			if !bundledEntry {
				return nil, []diag.Error{diag.Errorf(m.Header.NameSpan, "RESERVED MODULE", "Module `%s` is bundled with Fango as `%s`; local modules cannot use bundled names.", entryName, path)}
			}
		}
	}
	if bundledEntry {
		g.local = nil
	}
	wantEntry := strings.TrimSuffix(filepath.Base(abs), filepath.Ext(abs))
	if options.Root != "" {
		wantEntry = strings.ReplaceAll(strings.TrimSuffix(filepath.ToSlash(relEntry), ".fango"), "/", ".")
	}
	if !private && m.Header.Name != wantEntry {
		return nil, []diag.Error{diag.Errorf(m.Header.NameSpan, "MODULE/PATH MISMATCH", "The entry file `%s` must declare module `%s`, but declares `%s`.", filepath.Base(abs), wantEntry, m.Header.Name)}
	}
	rootNode := &node{name: entryName, path: filepath.ToSlash(relEntry), content: content, mod: m, sourceHash: hashBytes(content), private: private, deps: syntaxDependencies(m, entryName), nativeModule: wantEntry}
	if bundledEntry {
		rootNode.path = "<stdlib>/" + filepath.ToSlash(relEntry)
		rootNode.bundled = true
	}
	rootNativePath := strings.TrimSuffix(relEntry, ".fango") + ".native.go"
	if nb, ne := os.ReadFile(filepath.Join(root, rootNativePath)); ne == nil {
		if bundledEntry {
			rootNativePath = "<stdlib>/" + filepath.ToSlash(rootNativePath)
		}
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
	resolved := make([]ResolvedModule, 0, len(order))
	for _, name := range order {
		owner := name
		if g.nodes[name].private {
			owner = ""
		}
		merged.InstanceImports[owner] = g.visible[name]
		merged.Decls = append(merged.Decls, g.nodes[name].resolved...)
		role := DependencyRole
		entry := ""
		if name == entryName {
			role = EntryRole
			entry = "main"
			if !private {
				entry = canonical(entryName, "main")
			}
		}
		resolved = append(resolved, g.resolvedModule(name, owner, role, entry))
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
		unit := Unit{Name: unitName, Imports: explicitDependencyNames(n), Entry: name == entryName}
		if unit.Entry {
			unit.Program = wantEntry
		}
		units = append(units, unit)
	}
	entrySymbol := "main"
	if !private {
		entrySymbol = canonical(entryName, "main")
	}
	return &Result{Module: merged, Modules: resolved, Entry: entrySymbol, FixityHash: fixity.Hash(g.fixities), Manifest: manifest, Units: units, Fixity: g.fixities, Natives: natives}, nil
}

func exportInterface(in *iface) Interface {
	if in == nil {
		return Interface{}
	}
	cloneStrings := func(m map[string]string) map[string]string {
		out := make(map[string]string, len(m))
		for k, v := range m {
			out[k] = v
		}
		return out
	}
	cloneSlices := func(m map[string][]string) map[string][]string {
		out := make(map[string][]string, len(m))
		for k, v := range m {
			out[k] = append([]string(nil), v...)
		}
		return out
	}
	cloneBools := func(m map[string]bool) map[string]bool {
		out := make(map[string]bool, len(m))
		for k, v := range m {
			out[k] = v
		}
		return out
	}
	return Interface{Values: cloneStrings(in.values), Types: cloneStrings(in.types), Ctors: cloneStrings(in.ctors), Operations: cloneStrings(in.ops), Records: cloneStrings(in.records), TypeMembers: cloneSlices(in.typeMembers), EffectMembers: cloneSlices(in.effectMembers), RecordFields: cloneSlices(in.recordFields), OpenTypes: cloneBools(in.openTypes), OpenEffects: cloneBools(in.openEffects)}
}

func firstDeclFile(decls []ast.Decl) *source.File {
	for _, decl := range decls {
		var sp source.Span
		switch d := decl.(type) {
		case *ast.ValueDecl:
			sp = d.Sp
		case *ast.PatternDecl:
			sp = d.Sp
		case *ast.TypeDecl:
			sp = d.Sp
		case *ast.EffectDecl:
			sp = d.Sp
		case *ast.FixityDecl:
			sp = d.Sp
		case *ast.ClassDecl:
			sp = d.Sp
		case *ast.InstanceDecl:
			sp = d.Sp
		case *ast.DeriverDecl:
			sp = d.Sp
		}
		if sp.File != nil {
			return sp.File
		}
	}
	return nil
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
// type. A file using a backtick quotation or `$(…)` needs it in the graph to have a type
// for its quotes, so the loader adds the dependency where the syntax appears
// rather than taxing every program with it.
const MetaModule = "Meta"

// DeriveModule supplies the derivers for the standard classes. A file that
// writes `deriving` needs them in the graph for the same reason a file that
// writes a quotation needs Meta: the loader adds the edge where the syntax
// appears rather than taxing every program with it.
const DeriveModule = "Derive"

// ListModule owns the List.Nil/List.Cons constructors used by bracket syntax.
const ListModule = "List"

// TupleModule owns the Pair/Triple types and constructors used by `(a, b)`.
const TupleModule = "Tuple"

// RegexModule owns the opaque type introduced by regex literal syntax.
const RegexModule = "Regex"

// syntaxDependencies is the set of modules a file depends on through syntax
// rather than an import: the Prelude it did not opt out of, and the owners of
// the bracket, tuple, quote, and deriving forms it uses.
func syntaxDependencies(m *ast.Module, self string) []string {
	var deps []string
	if !m.NoPrelude && self != PreludeModule {
		deps = append(deps, PreludeModule)
	}
	if m.UsesStaging && self != MetaModule {
		deps = addDep(deps, MetaModule)
	}
	if self != DeriveModule && usesDeriving(m) {
		deps = addDep(deps, DeriveModule)
	}
	if m.UsesLists && self != ListModule {
		deps = addDep(deps, ListModule)
	}
	if m.UsesTuples && self != TupleModule {
		deps = addDep(deps, TupleModule)
	}
	if m.UsesRegex && self != RegexModule {
		deps = addDep(deps, RegexModule)
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
		errs = append(errs, diag.Error{Title: "INVALID BUNDLED PRELUDE", Body: n.path + " may contain only imports."})
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
			}
		case *ast.EffectDecl:

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
	s := template
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
		return true
	})
	allowed := map[string]bool{
		"fangort": true, "true": true, "false": true, "nil": true,
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
	boundary := nativeBoundary{
		wrappers:        localWrapperTypes(n.mod.Decls),
		localTypes:      localTypeNames(n.mod.Decls),
		fallibleError:   bundledFallibleError(n),
		bytesAllowed:    n.bundled,
		ioHandleAllowed: n.bundled && n.name == "File",
	}
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
// before name resolution: the module's own single-boundary-value wrappers, and
// whether it may declare fallible results or the bundled shared IO.Handle.
// Recognition is by spelling, which
// is safe here because a wrapper must be declared in this very file and the
// fallible shape is admitted only in the module the compiler controls; type
// checking re-establishes both shapes on resolved types.
type nativeBoundary struct {
	wrappers        map[string]string // local wrapper type name -> Go scalar type
	localTypes      map[string]bool   // every type name this module declares
	fallibleError   string
	bytesAllowed    bool
	ioHandleAllowed bool
}

// localTypeNames answers every type name the module declares, so a spelling
// that matches the bundled Bytes can be told apart from a module.s own type of
// that name before resolution has run.
func localTypeNames(decls []ast.Decl) map[string]bool {
	out := map[string]bool{}
	for _, d := range decls {
		if td, ok := d.(*ast.TypeDecl); ok {
			out[td.Name] = true
		}
	}
	return out
}

// localWrapperTypes finds the module's one-constructor, one-boundary-value
// wrappers. Parameters may be phantom: the field must itself be a boundary
// value, so no parameter can affect the erased representation.
func localWrapperTypes(decls []ast.Decl) map[string]string {
	out := map[string]string{}
	for _, d := range decls {
		td, ok := d.(*ast.TypeDecl)
		if !ok || td.RecordFields != nil || len(td.Ctors) != 1 || len(td.Ctors[0].Args) != 1 {
			continue
		}
		if goType := boundaryGoTypeSpelling(td.Ctors[0].Args[0]); goType != "" {
			out[td.Name] = goType
		}
	}
	return out
}

func bundledFallibleError(n *node) string {
	if !n.bundled {
		return ""
	}
	switch n.name {
	case "IO", "File":
		return "IO.Error"
	case "Net":
		return "Net.Error"
	default:
		return ""
	}
}

// fallibleResult recognizes the spelling `Result IO.Error T` (or `Result
// Error T` once IO's Error is imported unqualified), answering T.
func fallibleResult(t ast.TypeExpr) (ast.TypeExpr, string, bool) {
	app, ok := t.(*ast.TApp)
	if !ok || app.Name != "Result" || len(app.Args) != 2 {
		return nil, "", false
	}
	errTy, ok := app.Args[0].(*ast.TName)
	if !ok || errTy.Name != "Error" && errTy.Name != "IO.Error" && errTy.Name != "Net.Error" {
		return nil, "", false
	}
	return app.Args[1], errTy.Name, true
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
				errs = append(errs, diag.Errorf(d.Native.Sp, "NATIVE ABI", "Parameter %d of `%s` must use the scalar Go type for its Fango annotation, or a type this module declares as a single-constructor wrapper around one. `Bytes` crosses as `[]byte`, in bundled sidecars only.", i+1, fn.Name.Name))
			}
			i++
		}
	}
	if payload, errorName, fallible := fallibleResult(t); fallible {
		allowedName := b.fallibleError
		unqualifiedAllowed := errorName == "Error" && allowedName != ""
		if allowedName == "" || fromOp || !unqualifiedAllowed && errorName != allowedName {
			errs = append(errs, diag.Errorf(d.Native.Sp, "FALLIBLE NATIVE NOT ALLOWED", "Only value natives of the bundled `IO`, `File`, and `Net` modules may declare their module's supported error result; return a boundary value and build the `Result` in Fango."))
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
		errs = append(errs, diag.Errorf(d.Native.Sp, "NATIVE ABI", "Function `%s` must return exactly the scalar Go type in native `%s`'s annotation, or a type this module declares as a single-constructor wrapper around one. `Bytes` crosses as `[]byte`, in bundled sidecars only.", fn.Name.Name, d.Name))
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
	if n, ok := t.(*ast.TName); ok && b.ioHandleAllowed && n.Name == "IO.Handle" {
		return "any"
	}
	if n, ok := t.(*ast.TName); ok && (strings.HasSuffix(n.Name, ".Registration") || n.Name == "Registration") && !b.localTypes[n.Name] {
		return "any" // Resolved checking verifies the scoped request protocol.
	}
	if n, ok := t.(*ast.TName); ok && n.Name == "Runtime.Async.Native.Bridge" && !b.localTypes[n.Name] {
		return "any" // Resolved checking verifies the canonical shared bridge.
	}
	if _, ok := t.(*ast.TVarName); ok {
		return "any" // Resolved checking requires a same-index storage edge.
	}
	if app, ok := t.(*ast.TApp); ok {
		return b.wrappers[app.Name]
	}
	if goType := scalarGoType(t); goType != "" {
		return goType
	}
	n, ok := t.(*ast.TName)
	if !ok {
		return ""
	}
	// The bundled Bytes is the one non-scalar boundary type, and only a
	// bundled sidecar may name it. The spelling is checked before resolution,
	// so a module declaring its own type of that name keeps meaning its own.
	if b.bytesAllowed && !b.localTypes[n.Name] && (n.Name == "Bytes" || n.Name == "Bytes.Bytes") {
		return goBytesType
	}
	if !b.localTypes[n.Name] && (n.Name == "Runtime.Native.Any" || n.Name == "Any") {
		return "any"
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

func boundaryGoTypeSpelling(t ast.TypeExpr) string {
	if scalar := scalarGoType(t); scalar != "" {
		return scalar
	}
	n, ok := t.(*ast.TName)
	if ok && (n.Name == "Runtime.Native.Any" || n.Name == "Any") {
		return "any"
	}
	return ""
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

// goBytesType is the only Go spelling a sidecar can use for Bytes: sidecars
// may not import fangort, whose Bytes is an alias for this.
const goBytesType = "[]byte"

func goTypeName(e goast.Expr) string {
	switch e := e.(type) {
	case *goast.Ident:
		return e.Name
	case *goast.InterfaceType:
		if e.Methods != nil && len(e.Methods.List) == 0 {
			return "any"
		}
	case *goast.ArrayType:
		if e.Len == nil {
			if elem := goTypeName(e.Elt); elem == "byte" || elem == "uint8" {
				return goBytesType
			}
		}
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

func buildInterface(n *node, nodes map[string]*node, errs []diag.Error) (*iface, []diag.Error) {
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
	var imported *iface
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
		if imported == nil {
			imported = importedScope(n, nodes)
		}
		if reexport(pub, imported, item) {
			continue
		}
		if _, ctor := imported.ctors[item.Name]; ctor {
			errs = append(errs, diag.Errorf(item.Sp, "INVALID EXPORT", "Constructors cannot be exported individually; expose their type with `%s(..)`.", owner(imported.typeMembers, item.Name)))
			continue
		}
		if imported.types[item.Name] != "" {
			errs = append(errs, diag.Errorf(item.Sp, "NON-PUBLIC EXPORT", "`%s` is imported without its members; import it as `%s(..)` to re-export them.", item.Name, item.Name))
			continue
		}
		if from := qualifiedOnly(n, nodes, item.Name); from != "" {
			errs = append(errs, diag.Errorf(item.Sp, "UNKNOWN EXPORT", "`%s` is reachable only as `%s.%s`; a module re-exports only names its imports expose unqualified.", item.Name, from, item.Name))
			continue
		}
		errs = append(errs, diag.Errorf(item.Sp, "UNKNOWN EXPORT", "Module `%s` has no declaration named `%s`, and its imports do not expose one unqualified.", n.name, item.Name))
	}
	return pub, errs
}

// importedScope is the union of what this module's own imports expose
// unqualified. The prelude's implicit imports are not part of it, so a
// module re-exports only names it chose to import.
func importedScope(n *node, nodes map[string]*node) *iface {
	out := newIface()
	for _, im := range n.mod.Imports {
		dep := nodes[im.Module]
		if dep == nil || dep.iface == nil {
			continue
		}
		// Selection errors are the resolver's to report, once.
		sel, _ := dep.iface.selection(im.Exposing, im.ModuleSpan)
		for k, v := range sel.values {
			out.values[k] = v
		}
		for k, v := range sel.types {
			out.types[k] = v
		}
		for k, v := range sel.ctors {
			out.ctors[k] = v
		}
		for k, v := range sel.ops {
			out.ops[k] = v
		}
		for k, v := range sel.records {
			out.records[k] = v
			out.recordFields[k] = sel.recordFields[k]
		}
		for k := range sel.openTypes {
			out.openTypes[k] = true
			out.typeMembers[k] = dep.iface.typeMembers[k]
		}
		for k := range sel.openEffects {
			out.openEffects[k] = true
			out.effectMembers[k] = dep.iface.effectMembers[k]
		}
	}
	return out
}

// qualifiedOnly names an import that publishes name without exposing it
// to this module unqualified.
func qualifiedOnly(n *node, nodes map[string]*node, name string) string {
	for _, im := range n.mod.Imports {
		dep := nodes[im.Module]
		if dep == nil || dep.iface == nil {
			continue
		}
		if dep.iface.values[name] != "" || dep.iface.types[name] != "" {
			return im.Module
		}
	}
	return ""
}

// reexport adds one exposing item that names an imported binding. The item
// carries exactly what the import brought into scope: `T(..)` requires that
// the import exposed T's members too.
func reexport(pub, in *iface, item ast.ExposeItem) bool {
	if v, ok := in.values[item.Name]; ok && !item.All {
		pub.values[item.Name] = v
		if op := in.ops[item.Name]; op != "" {
			pub.ops[item.Name] = op
		}
		return true
	}
	v, ok := in.types[item.Name]
	if !ok {
		return false
	}
	if !item.All {
		pub.types[item.Name] = v
		return true
	}
	if rv, ok := in.records[item.Name]; ok {
		pub.types[item.Name] = v
		pub.records[item.Name] = rv
		pub.recordFields[item.Name] = append([]string(nil), in.recordFields[item.Name]...)
		pub.openTypes[item.Name] = true
		return true
	}
	if !in.openTypes[item.Name] && !in.openEffects[item.Name] {
		return false
	}
	pub.types[item.Name] = v
	if in.openTypes[item.Name] {
		pub.typeMembers[item.Name] = in.typeMembers[item.Name]
		pub.openTypes[item.Name] = true
		for _, x := range in.typeMembers[item.Name] {
			pub.ctors[x] = in.ctors[x]
		}
	}
	if in.openEffects[item.Name] {
		pub.effectMembers[item.Name] = in.effectMembers[item.Name]
		pub.openEffects[item.Name] = true
		for _, x := range in.effectMembers[item.Name] {
			if op := in.ops[x]; op != "" {
				pub.ops[x] = op
			}
			pub.values[x] = in.values[x]
		}
	}
	return true
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
			// A default resolves in the class's scope, like an instance
			// method; an instance elsewhere receives the resolved copy.
			for _, m := range d.Defaults {
				m.Name = r.canon(m.Name)
				r.resolveValueRows(m, r.vals)
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
			// A function sees itself; a value does not. Either way the name is
			// bound from here on, so a function binds it before its body
			// rather than resolving against a copy of the whole scope.
			if len(d.Params) > 0 {
				r.vals[surface] = canon
			}
			r.typeAnn(d.Ann)
			if d.Native != nil {
				d.Native.Module = r.node.nativeModule
			}
			r.resolveValueRows(d, r.vals)
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
			d.VisitAttributes(func(group *ast.AttributeGroup) {
				for _, e := range group.Exprs {
					r.expr(e, r.vals, map[string]bool{})
				}
			})
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
		r.effRow(t.Eff)
	case *ast.TRow:
		r.effRow(t.Row)
	}
}

// effRow resolves the effect names in a row, wherever the row stands.
func (r *resolver) effRow(row *ast.EffRow) {
	if row == nil {
		return
	}
	for i := range row.Labels {
		l := &row.Labels[i]
		l.Name = r.qualified(l.Name, r.tys, "type", l.NameSp)
		for _, a := range l.Args {
			r.typ(a)
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
		if e.Witness != nil {
			r.typ(e.Witness)
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
			r.typeAnn(c.Signature)
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
