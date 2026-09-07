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
	"github.com/waj/fango/internal/lexer"
	"github.com/waj/fango/internal/natives"
	"github.com/waj/fango/internal/parser"
	"github.com/waj/fango/internal/source"
)

// Provider is the package-resolution seam shared by local and compiler-bundled
// modules.
type Provider interface {
	Source(module string) (path string, content []byte, err error)
	Native(module string) (path string, content []byte, err error)
}

type FSProvider struct{ Root string }

type pathCaseError struct{ want, found string }

func (e pathCaseError) Error() string {
	return fmt.Sprintf("path casing mismatch: expected %s, found %s", e.want, e.found)
}

func (p FSProvider) Source(module string) (string, []byte, error) {
	rel := filepath.FromSlash(strings.ReplaceAll(module, ".", "/") + ".fango")
	path := filepath.Join(p.Root, rel)
	absRoot, _ := filepath.Abs(p.Root)
	absPath, _ := filepath.Abs(path)
	if absPath != absRoot && !strings.HasPrefix(absPath, absRoot+string(filepath.Separator)) {
		return rel, nil, fmt.Errorf("module path escapes the source root")
	}
	cur := p.Root
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		entries, readErr := os.ReadDir(cur)
		if readErr != nil {
			break
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
			return rel, nil, pathCaseError{want: part, found: found}
		}
		cur = filepath.Join(cur, part)
	}
	b, err := os.ReadFile(path)
	return filepath.ToSlash(rel), b, err
}

func (p FSProvider) Native(module string) (string, []byte, error) {
	rel := filepath.FromSlash(strings.ReplaceAll(module, ".", "/") + ".native.go")
	b, err := os.ReadFile(filepath.Join(p.Root, rel))
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
	Module    *ast.Module
	Entry     string
	Manifest  []ManifestEntry
	Units     []Unit
	Operators map[string]string
	Natives   []NativeSource
}

type NativeSource struct {
	Module, Path string
	Content      []byte
	Bundled      bool
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
	iface        *iface
	private      bool
	bundled      bool
	deps         []string
	nativePath   string
	native       []byte
	nativeModule string
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
	m, errs := parse(f)
	if len(errs) > 0 {
		return nil, errs
	}
	entryName, private := "<entry>", m.Header == nil
	if !private {
		entryName = m.Header.Name
	}
	bundledProvider := BundledProvider{}
	if !private {
		if path, _, bundleErr := bundledProvider.Source(entryName); bundleErr == nil {
			return nil, []diag.Error{diag.Errorf(m.Header.NameSpan, "RESERVED MODULE", "Module `%s` is bundled with fango as `%s`; local modules cannot use bundled names.", entryName, path)}
		}
	}
	wantEntry := strings.TrimSuffix(filepath.Base(abs), filepath.Ext(abs))
	if !private && m.Header.Name != wantEntry {
		return nil, []diag.Error{diag.Errorf(m.Header.NameSpan, "MODULE/PATH MISMATCH", "The entry file `%s` must declare module `%s`, but declares `%s`.", filepath.Base(abs), wantEntry, m.Header.Name)}
	}
	rootNode := &node{name: entryName, path: filepath.Base(abs), content: content, mod: m, private: private, deps: implicitDeps(m, []string{"Basics", "IO"}, entryName), nativeModule: wantEntry}
	rootNativePath := wantEntry + ".native.go"
	if nb, ne := os.ReadFile(filepath.Join(root, rootNativePath)); ne == nil {
		rootNode.nativePath, rootNode.native = rootNativePath, nb
	}
	nodes := map[string]*node{entryName: rootNode}
	provider := FSProvider{Root: root}
	var load func(string, source.Span)
	load = func(name string, at source.Span) {
		if nodes[name] != nil {
			return
		}
		localPath, localContent, localErr := provider.Source(name)
		bundlePath, bundleContent, bundleErr := bundledProvider.Source(name)
		path, b, readErr, bundled := localPath, localContent, localErr, false
		if bundleErr == nil {
			if localErr == nil {
				errs = append(errs, diag.Errorf(at, "RESERVED MODULE", "Module `%s` is bundled with fango as `%s`; remove or rename the local `%s`.", name, bundlePath, localPath))
				return
			}
			if _, caseCollision := localErr.(pathCaseError); caseCollision {
				errs = append(errs, diag.Errorf(at, "RESERVED MODULE", "Module `%s` is bundled with fango as `%s`; a case-insensitive local path also conflicts with that reserved name.", name, bundlePath))
				return
			}
			if !errors.Is(localErr, fs.ErrNotExist) {
				errs = append(errs, diag.Errorf(at, "RESERVED MODULE", "Module `%s` is bundled with fango as `%s`; the local `%s` also occupies that reserved path.", name, bundlePath, localPath))
				return
			}
			path, b, readErr, bundled = bundlePath, bundleContent, nil, true
		}
		if readErr != nil {
			if ce, ok := readErr.(pathCaseError); ok {
				errs = append(errs, diag.Errorf(at, "MODULE PATH CASING", "Module `%s` requires exact path casing; expected `%s` but found `%s`.", name, ce.want, ce.found))
				return
			}
			errs = append(errs, diag.Errorf(at, "MISSING MODULE", "I cannot find module `%s`; expected `%s` beneath the entry directory.", name, path))
			return
		}
		mf := source.NewFile(path, b)
		mm, es := parse(mf)
		errs = append(errs, es...)
		if len(es) > 0 {
			return
		}
		if mm.Header == nil {
			errs = append(errs, diag.Errorf(at, "MISSING MODULE HEADER", "Imported file `%s` must declare `module %s exposing (...)`.", path, name))
			return
		}
		if mm.Header.Name != name {
			errs = append(errs, diag.Errorf(mm.Header.NameSpan, "MODULE/PATH MISMATCH", "File `%s` must declare module `%s`, but declares `%s`.", path, name, mm.Header.Name))
			return
		}
		n := &node{name: name, path: path, content: b, mod: mm, bundled: bundled, nativeModule: name}
		if !bundled {
			n.deps = []string{"Basics", "IO"}
		}
		n.deps = implicitDeps(mm, n.deps, name)
		var np string
		var nb []byte
		var ne error
		if bundled {
			np, nb, ne = bundledProvider.Native(name)
		} else {
			np, nb, ne = provider.Native(name)
		}
		if ne == nil {
			n.nativePath, n.native = np, nb
		}
		nodes[name] = n
		for _, im := range mm.Imports {
			load(im.Module, im.ModuleSpan)
		}
		for _, dep := range n.deps {
			load(dep, at)
		}
	}
	for _, im := range m.Imports {
		load(im.Module, im.ModuleSpan)
	}
	for _, dep := range nodes[entryName].deps {
		load(dep, source.Span{})
	}
	if len(errs) > 0 {
		return nil, errs
	}
	for _, n := range nodes {
		errs = append(errs, validateNatives(n)...)
	}
	if len(errs) > 0 {
		return nil, errs
	}

	// Detect cycles with a stable lexical traversal and retain the complete
	// repeated-start chain in the diagnostic.
	state, stack := map[string]int{}, []string{}
	var visit func(string) bool
	visit = func(name string) bool {
		state[name] = 1
		stack = append(stack, name)
		deps := dependencyNames(nodes[name])
		sort.Strings(deps)
		for _, dep := range deps {
			if state[dep] == 0 && visit(dep) {
				return true
			}
			if state[dep] == 1 {
				i := 0
				for stack[i] != dep {
					i++
				}
				chain := append(append([]string{}, stack[i:]...), dep)
				var sp source.Span
				for _, im := range nodes[name].mod.Imports {
					if im.Module == dep {
						sp = im.ModuleSpan
						break
					}
				}
				errs = append(errs, diag.Errorf(sp, "IMPORT CYCLE", "Imports form a cycle: %s.", strings.Join(chain, " -> ")))
				return true
			}
		}
		stack = stack[:len(stack)-1]
		state[name] = 2
		return false
	}
	names := make([]string, 0, len(nodes))
	for n := range nodes {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if state[n] == 0 && visit(n) {
			return nil, errs
		}
	}

	for _, n := range nodes {
		n.iface, errs = buildInterface(n, errs)
	}
	if len(errs) > 0 {
		return nil, errs
	}
	order := topo(nodes)
	merged := &ast.Module{InstanceImports: map[string]map[string]bool{}}
	for _, name := range order {
		owner := name
		if nodes[name].private {
			owner = ""
		}
		visible := map[string]bool{}
		for _, dep := range dependencyNames(nodes[name]) {
			visible[dep] = true
			for trans := range merged.InstanceImports[dep] {
				visible[trans] = true
			}
		}
		merged.InstanceImports[owner] = visible
		r := resolver{node: nodes[name], nodes: nodes}
		decls, es := r.resolve()
		errs = append(errs, es...)
		merged.Decls = append(merged.Decls, decls...)
	}
	if len(errs) > 0 {
		return nil, errs
	}
	manifest := make([]ManifestEntry, 0, len(order))
	units := make([]Unit, 0, len(order))
	operators := map[string]string{}
	var natives []NativeSource
	for _, name := range order {
		n := nodes[name]
		h := sha256.Sum256(n.content)
		manifest = append(manifest, ManifestEntry{Module: name, Path: n.path, SHA256: hex.EncodeToString(h[:])})
		if n.native != nil {
			nh := sha256.Sum256(n.native)
			manifest = append(manifest, ManifestEntry{Module: name, Path: n.nativePath, SHA256: hex.EncodeToString(nh[:])})
			natives = append(natives, NativeSource{Module: n.nativeModule, Path: n.nativePath, Content: n.native, Bundled: n.bundled})
		}
		for _, d := range n.mod.Decls {
			if inf, ok := d.(*ast.InfixDecl); ok {
				target := inf.Target
				if !strings.Contains(target, ".") {
					target = canonical(name, target)
				}
				operators[inf.Op] = target
			}
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
	return &Result{Module: merged, Entry: entrySymbol, Manifest: manifest, Units: units, Operators: operators, Natives: natives}, nil
}

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

// implicitDeps adds the bundled modules a file needs because of the syntax it
// used rather than because it imported them.
func implicitDeps(m *ast.Module, deps []string, self string) []string {
	if m.UsesStaging && self != MetaModule {
		deps = addDep(deps, MetaModule)
	}
	if self != DeriveModule && usesDeriving(m) {
		deps = addDep(deps, DeriveModule)
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

// validateNatives keeps the Go boundary deliberately small. Bundled modules
// may use inline templates; ordinary modules may only use sidecar call form
// with a closed scalar ABI that can be checked without running Go tooling.
func validateNatives(n *node) []diag.Error {
	var errs []diag.Error
	callDecls := map[string]*ast.ValueDecl{}
	templateTargets := map[string]bool{}
	infixes := map[string]source.Span{}
	for _, d := range n.mod.Decls {
		switch d := d.(type) {
		case *ast.ClassDecl:
			for _, m := range d.Methods {
				templateTargets[m.Name] = true
			}
		case *ast.ValueDecl:
			templateTargets[d.Name] = true
			if d.Native == nil {
				continue
			}
			if d.Native.Template != nil {
				if !n.bundled {
					errs = append(errs, diag.Errorf(d.Native.Sp, "NATIVE TEMPLATE NOT ALLOWED", "Inline native templates are reserved for compiler-bundled modules; use `native` with a sidecar function."))
					continue
				}
				errs = append(errs, validateTemplate(*d.Native.Template, nativeArity(d.Ann), d.Native.Sp)...)
				templateTargets[d.Name] = true
				if spec, ok := natives.Lookup(canonical(n.name, d.Name)); !ok || spec.Arity != nativeArity(d.Ann) || spec.Effect {
					errs = append(errs, diag.Errorf(d.Native.Sp, "INVALID BUNDLED NATIVE", "Bundled native `%s.%s` does not match the interpreter registry.", n.name, d.Name))
				}
			} else {
				callDecls[d.Name] = d
			}
		case *ast.EffectDecl:
			for _, op := range d.Ops {
				if op.Native == nil {
					continue
				}
				if !n.bundled {
					errs = append(errs, diag.Errorf(op.Native.Sp, "NATIVE EFFECT NOT ALLOWED", "User sidecars implement pure native values; native effect operations are reserved for bundled modules."))
					continue
				}
				if op.Native.Template == nil {
					errs = append(errs, diag.Errorf(op.Native.Sp, "NATIVE EFFECT TEMPLATE", "Bundled native effect operations require an inline template."))
				} else {
					errs = append(errs, validateTemplate(*op.Native.Template, typeArity(op.Type), op.Native.Sp)...)
					if spec, ok := natives.Lookup(canonical(n.name, op.Name)); !ok || spec.Arity != typeArity(op.Type) || !spec.Effect {
						errs = append(errs, diag.Errorf(op.Native.Sp, "INVALID BUNDLED NATIVE", "Bundled native `%s.%s` does not match the interpreter registry.", n.name, op.Name))
					}
				}
			}
		case *ast.InfixDecl:
			if !n.bundled {
				errs = append(errs, diag.Errorf(d.OpSpan, "INFIX NOT ALLOWED", "Operator bindings are reserved for compiler-bundled modules."))
				continue
			}
			if _, exists := infixes[d.Op]; exists {
				errs = append(errs, diag.Errorf(d.OpSpan, "DUPLICATE INFIX", "Operator `%s` already has a binding in this module.", d.Op))
			}
			infixes[d.Op] = d.OpSpan
		}
	}
	for _, d := range n.mod.Decls {
		if inf, ok := d.(*ast.InfixDecl); ok && n.bundled && !templateTargets[inf.Target] {
			errs = append(errs, diag.Errorf(inf.TargetSpan, "INVALID INFIX TARGET", "Operator `%s` must name a value or class method in the same bundled module.", inf.Op))
		}
	}
	if n.bundled {
		return errs
	}
	if len(callDecls) == 0 {
		if n.native != nil {
			errs = append(errs, diag.Errorf(source.Span{}, "ORPHAN NATIVE SIDECAR", "Module `%s` has `%s`, but declares no call-form native values.", n.name, n.nativePath))
		}
		return errs
	}
	if n.native == nil {
		for _, d := range callDecls {
			errs = append(errs, diag.Errorf(d.Native.Sp, "MISSING NATIVE SIDECAR", "Native `%s` requires `%s.native.go` beside the module source.", d.Name, n.name))
		}
		return errs
	}
	return append(errs, validateSidecar(n, callDecls)...)
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

func validateSidecar(n *node, decls map[string]*ast.ValueDecl) []diag.Error {
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
			errs = append(errs, diag.Errorf(source.Span{}, "NATIVE IMPORT NOT ALLOWED", "User sidecar %s may import only Go standard-library packages; `%s` is external.", n.nativePath, path))
		}
	}
	funcs := map[string]*goast.FuncDecl{}
	for _, d := range f.Decls {
		if fn, ok := d.(*goast.FuncDecl); ok && fn.Recv == nil && goast.IsExported(fn.Name.Name) {
			funcs[fn.Name.Name] = fn
		}
	}
	used := map[string]bool{}
	for name, d := range decls {
		goName := exportNativeName(name)
		fn := funcs[goName]
		if fn == nil {
			errs = append(errs, diag.Errorf(d.Native.Sp, "MISSING NATIVE FUNCTION", "Native `%s` requires exported function `%s` in %s.", name, goName, n.nativePath))
			continue
		}
		used[goName] = true
		errs = append(errs, validateNativeShape(d, fn)...)
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

func validateNativeShape(d *ast.ValueDecl, fn *goast.FuncDecl) []diag.Error {
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
		if f.Eff != nil && (len(f.Eff.Labels) > 0 || f.Eff.Tail != "") {
			return []diag.Error{diag.Errorf(d.Native.Sp, "NATIVE ABI", "User native `%s` must be pure.", d.Name)}
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
			if want := nativeGoType(params[i]); want == "" || goTypeName(field.Type) != want {
				errs = append(errs, diag.Errorf(d.Native.Sp, "NATIVE ABI", "Parameter %d of `%s` must use the scalar Go type for its Fango annotation.", i+1, fn.Name.Name))
			}
			i++
		}
	}
	if isUnitType(t) {
		if fieldCount(fn.Type.Results) != 0 {
			errs = append(errs, diag.Errorf(d.Native.Sp, "NATIVE ABI", "Function `%s` must return no value for Fango Unit.", fn.Name.Name))
		}
	} else if fieldCount(fn.Type.Results) != 1 || len(fn.Type.Results.List) != 1 || goTypeName(fn.Type.Results.List[0].Type) != nativeGoType(t) {
		errs = append(errs, diag.Errorf(d.Native.Sp, "NATIVE ABI", "Function `%s` must return exactly the scalar Go type in native `%s`'s annotation.", fn.Name.Name, d.Name))
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

func nativeGoType(t ast.TypeExpr) string {
	n, ok := t.(*ast.TName)
	if !ok {
		return ""
	}
	return map[string]string{"Int": "int64", "Float": "float64", "String": "string", "Char": "rune", "Bool": "bool"}[n.Name]
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
		deps := dependencyNames(n)
		indegree[name] = len(deps)
		for _, dep := range deps {
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

	// schemas holds the canonical types whose constructors or record fields
	// this module may read. `typeOf` copies it, which is the whole modularity
	// story for reflection: `exposing (T)` reflects opaque and
	// `exposing (T(..))` reflects in full, exactly as those two forms
	// already govern patterns and field access.
	schemas map[string]bool
}

func (r *resolver) canon(name string) string {
	if r.node.private {
		return name
	}
	return canonical(r.node.name, name)
}

func (r *resolver) resolve() ([]ast.Decl, []diag.Error) {
	r.vals = map[string]string{}
	r.tys = map[string]string{"Int": "Int", "Float": "Float", "String": "String", "Char": "Char", "Bool": "Bool", "()": "()"}
	r.ctors = map[string]string{"True": "True", "False": "False"}
	r.ops = map[string]string{}
	r.records = map[string]string{}
	r.recordLabels = map[string][]string{}
	r.schemas = map[string]bool{"Bool": true}
	if r.node.name != "Basics" {
		if basics := r.nodes["Basics"]; basics != nil {
			for _, name := range []string{"Num", "Eq", "Ord", "Show"} {
				if v := basics.iface.types[name]; v != "" {
					r.tys[name] = v
				}
			}
			if v := basics.iface.values["show"]; v != "" {
				r.vals["show"] = v
			}
		}
	}
	if !r.node.bundled {
		if ioNode := r.nodes["IO"]; ioNode != nil && ioNode.iface != nil {
			for _, name := range []string{"print", "readLine"} {
				if v := ioNode.iface.values[name]; v != "" {
					r.vals[name] = v
					r.ops[name] = ioNode.iface.ops[name]
				}
			}
			if v := ioNode.iface.types["IO"]; v != "" {
				r.tys["IO"] = v
			}
		}
	}
	r.quals = map[string]*iface{}
	seenModules, aliases, fullQualifiers := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, im := range r.node.mod.Imports {
		fullQualifiers[im.Module] = true
	}
	for _, im := range r.node.mod.Imports {
		if seenModules[im.Module] {
			r.errs = append(r.errs, diag.Errorf(im.ModuleSpan, "DUPLICATE IMPORT", "Module `%s` is imported more than once.", im.Module))
			continue
		}
		seenModules[im.Module] = true
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
			if aliases[im.Alias] || fullQualifiers[im.Alias] || r.quals[im.Alias] != nil {
				r.errs = append(r.errs, diag.Errorf(im.AliasSpan, "DUPLICATE IMPORT ALIAS", "The qualifier `%s` is already in use.", im.Alias))
			} else {
				aliases[im.Alias] = true
				r.quals[im.Alias] = dep.iface
			}
		}
		sel, es := dep.iface.selection(im.Exposing, im.ModuleSpan)
		r.errs = append(r.errs, es...)
		r.merge(sel, im.ModuleSpan)
	}
	// Types, effects, constructors, and operations are module-wide, matching
	// the checker's existing mutually-recursive declaration pass.
	for _, d := range r.node.mod.Decls {
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
	var out []ast.Decl
	for _, d := range r.node.mod.Decls {
		switch d := d.(type) {
		case *ast.ClassDecl:
			r.add(r.tys, d.Name, r.canon(d.Name), d.NameSpan)
			for _, m := range d.Methods {
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
				locals := map[string]bool{}
				for _, p := range m.Params {
					if p.Name != "_" && p.Name != "()" {
						r.checkBinder(p.Name, p.Sp, r.vals)
						locals[p.Name] = true
					}
				}
				r.expr(m.Body, r.vals, locals)
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
				locals := map[string]bool{}
				for _, p := range m.Params {
					if p.Name != "_" && p.Name != "()" {
						r.checkBinder(p.Name, p.Sp, r.vals)
						locals[p.Name] = true
					}
				}
				r.expr(m.Body, r.vals, locals)
			}
			out = append(out, d)
		case *ast.ValueDecl:
			surface := d.Name
			canon := r.canon(surface)
			if _, exists := r.vals[surface]; exists {
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
			locals := map[string]bool{}
			for _, p := range d.Params {
				if p.Name != "_" && p.Name != "()" {
					r.checkBinder(p.Name, p.Sp, visible)
					locals[p.Name] = true
				}
			}
			r.expr(d.Body, visible, locals)
			d.Name = canon
			r.vals[surface] = canon
			out = append(out, d)
		case *ast.InfixDecl:
			d.Target = r.canon(d.Target)
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
				d.Ops[i].Name = r.canon(d.Ops[i].Name)
				r.typ(d.Ops[i].Type)
			}
			out = append(out, d)
		}
	}
	return out, r.errs
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
		t.Name = r.qualified(t.Name, r.tys, "type", t.NameSp)
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
		e.Name = r.qualified(e.Name, r.ctors, "ctor", e.Sp)
	case *ast.RecordLit:
		e.Name = r.qualified(e.Name, r.records, "record", e.NameSpan)
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
		r.expr(e.L, vals, locals)
		r.expr(e.R, vals, locals)
	case *ast.Lambda:
		ls := copySet(locals)
		for _, p := range e.Params {
			if p.Name != "_" {
				r.checkBinder(p.Name, p.Sp, vals)
				ls[p.Name] = true
			}
		}
		r.expr(e.Body, vals, ls)
	case *ast.Block:
		ls := copySet(locals)
		for i := range e.Binds {
			b := &e.Binds[i]
			r.checkBinder(b.Name, b.NameSpan, vals)
			r.typeAnn(b.Ann)
			inner := copySet(ls)
			for _, p := range b.Params {
				if p.Name != "_" {
					r.checkBinder(p.Name, p.Sp, vals)
					inner[p.Name] = true
				}
			}
			if len(b.Params) > 0 {
				inner[b.Name] = true
			}
			r.expr(b.Body, vals, inner)
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
		for i := range e.Clauses {
			c := &e.Clauses[i]
			c.Op = r.qualified(c.Op, r.ops, "op", c.OpSpan)
			ls := copySet(locals)
			for _, p := range c.Params {
				if p.Name != "_" && p.Name != "()" {
					r.checkBinder(p.Name, p.Sp, vals)
					ls[p.Name] = true
				}
			}
			r.expr(c.Body, vals, ls)
		}
		if e.Return != nil {
			ls := copySet(locals)
			r.checkBinder(e.Return.Param.Name, e.Return.Param.Sp, vals)
			ls[e.Return.Param.Name] = true
			r.expr(e.Return.Body, vals, ls)
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

func (r *resolver) pattern(p ast.Pattern, locals map[string]bool, vals map[string]string) {
	outer := copySet(locals)
	r.patternInner(p, locals, outer, vals)
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
		p.Name = r.qualified(p.Name, r.ctors, "ctor", p.NameSpan)
		for _, a := range p.Args {
			r.patternInner(a, locals, outer, vals)
		}
	case *ast.PRecord:
		p.Name = r.qualified(p.Name, r.records, "record", p.NameSpan)
		for _, f := range p.Fields {
			r.patternInner(f.Pattern, locals, outer, vals)
		}
	}
}

func ManifestJSON(entries []ManifestEntry) []byte {
	b, _ := json.MarshalIndent(entries, "", "  ")
	return append(b, '\n')
}
