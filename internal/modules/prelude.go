package modules

import (
	"sort"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/fixity"
	"github.com/waj/fango/internal/source"
)

// Scope is the unqualified view Prelude.fango's exposing lists produce,
// mapping each surface name to its canonical one. The batch resolver reaches
// the same view by merging those imports into every module; the REPL and the
// focused checker tests have no name resolver, so they bind these names
// directly and read the list from here rather than repeating it.
type Scope struct {
	Values, Types, Ops map[string]string
}

// PreludeResult is the resolved bundled prelude. Owners identifies the
// complete resolved module set for test projections.
type PreludeResult struct {
	Module   *ast.Module
	Fixities fixity.Table
	Owners   map[string]bool
	Scope    Scope
}

// Prelude resolves the actual bundled sources and their transitive bundled
// dependencies. Its roots are Prelude itself, which declares the default
// scope, and the modules that surface syntax desugars into: a later prompt
// can quote, derive, or write `[1]` or `(a, b)`, and syntax that always
// parses must always resolve. Rooting those puts none of their names in
// view — only Prelude.fango does that.
func Prelude() (*PreludeResult, []diag.Error) {
	provider := BundledProvider{}
	nodes := map[string]*node{}
	var errs []diag.Error

	var load func(string)
	load = func(name string) {
		if nodes[name] != nil {
			return
		}
		path, data, err := provider.Source(name)
		if err != nil {
			errs = append(errs, diag.Error{Title: "INVALID EMBEDDED PRELUDE", Body: err.Error()})
			return
		}
		m, parseErrs := parse(source.NewFile(path, data))
		errs = append(errs, parseErrs...)
		if len(parseErrs) > 0 {
			return
		}
		if m.Header == nil || m.Header.Name != name {
			errs = append(errs, diag.Error{Title: "INVALID EMBEDDED PRELUDE", Body: path + " must declare module " + name + "."})
			return
		}
		n := &node{name: name, path: path, content: data, mod: m, bundled: true, nativeModule: name}
		if nativePath, native, nativeErr := provider.Native(name); nativeErr == nil {
			n.nativePath, n.native = nativePath, native
		}
		n.deps = implicitDeps(m, preludeDeps(m, name), name)
		nodes[name] = n
		for _, im := range m.Imports {
			load(im.Module)
		}
		for _, dep := range n.deps {
			load(dep)
		}
	}

	for _, root := range []string{PreludeModule, MetaModule, DeriveModule, ListModule, TupleModule} {
		load(root)
	}
	if len(errs) > 0 {
		return nil, errs
	}

	names := make([]string, 0, len(nodes))
	for name := range nodes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		errs = append(errs, validateModuleDecls(nodes[name])...)
	}
	if len(errs) > 0 {
		return nil, errs
	}

	fixities := fixity.Builtin()
	for _, name := range names {
		errs = append(errs, fixities.Collect(nodes[name].mod.Decls)...)
	}
	for _, name := range names {
		errs = append(errs, fixities.Resolve(nodes[name].mod)...)
	}
	if len(errs) > 0 {
		return nil, errs
	}
	for _, name := range names {
		nodes[name].iface, errs = buildInterface(nodes[name], errs)
	}
	if len(errs) > 0 {
		return nil, errs
	}

	order := topo(nodes)
	if len(order) != len(nodes) {
		return nil, []diag.Error{{Title: "INVALID EMBEDDED PRELUDE", Body: "Bundled prelude imports form a cycle."}}
	}
	merged := &ast.Module{InstanceImports: map[string]map[string]bool{}}
	owners := make(map[string]bool, len(order))
	for _, name := range order {
		owners[name] = true
		visible := map[string]bool{}
		for _, dep := range dependencyNames(nodes[name]) {
			visible[dep] = true
			for trans := range merged.InstanceImports[dep] {
				visible[trans] = true
			}
		}
		merged.InstanceImports[name] = visible
		r := resolver{node: nodes[name], nodes: nodes}
		decls, resolveErrs := r.resolve()
		merged.Decls = append(merged.Decls, decls...)
		errs = append(errs, resolveErrs...)
	}
	// Prompt declarations are the synthetic entry module. Like a batch entry,
	// they can use instances and derivers from every transitive prelude module.
	promptVisible := make(map[string]bool, len(owners))
	for owner := range owners {
		promptVisible[owner] = true
	}
	merged.InstanceImports[""] = promptVisible
	scope, scopeErrs := preludeScope(nodes)
	errs = append(errs, scopeErrs...)
	return &PreludeResult{Module: merged, Fixities: fixities, Owners: owners, Scope: scope}, errs
}

// preludeScope projects Prelude.fango's imports through the imported modules'
// public interfaces, which is exactly what the batch resolver merges into
// each module.
func preludeScope(nodes map[string]*node) (Scope, []diag.Error) {
	s := Scope{Values: map[string]string{}, Types: map[string]string{}, Ops: map[string]string{}}
	prelude := nodes[PreludeModule]
	if prelude == nil {
		return s, []diag.Error{{Title: "INVALID EMBEDDED PRELUDE", Body: "The bundled " + PreludeModule + " module is missing."}}
	}
	var errs []diag.Error
	for _, im := range prelude.mod.Imports {
		dep := nodes[im.Module]
		if dep == nil || dep.iface == nil {
			continue
		}
		sel, es := dep.iface.selection(im.Exposing, im.ModuleSpan)
		errs = append(errs, es...)
		for k, v := range sel.values {
			s.Values[k] = v
		}
		for k, v := range sel.types {
			s.Types[k] = v
		}
		for k, v := range sel.ops {
			s.Ops[k] = v
		}
	}
	return s, errs
}
