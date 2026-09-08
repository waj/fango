package modules

import (
	"sort"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/fixity"
	"github.com/waj/fango/internal/source"
)

// Prelude resolves the actual bundled sources and their transitive bundled
// dependencies. The REPL always roots Meta and Derive because a later prompt
// can use staging or deriving; ordinary ambient definitions come from Basics
// and IO. Owners identifies the complete resolved prelude for test projections.
func Prelude() (*ast.Module, fixity.Table, map[string]bool, []diag.Error) {
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
		n.deps = implicitDeps(m, nil, name)
		nodes[name] = n
		for _, im := range m.Imports {
			load(im.Module)
		}
		for _, dep := range n.deps {
			load(dep)
		}
	}

	for _, root := range []string{"Basics", "Meta", "Derive", "IO"} {
		load(root)
	}
	if len(errs) > 0 {
		return nil, nil, nil, errs
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
		return nil, nil, nil, errs
	}

	fixities := fixity.Builtin()
	for _, name := range names {
		errs = append(errs, fixities.Collect(nodes[name].mod.Decls)...)
	}
	for _, name := range names {
		errs = append(errs, fixities.Resolve(nodes[name].mod)...)
	}
	if len(errs) > 0 {
		return nil, nil, nil, errs
	}
	for _, name := range names {
		nodes[name].iface, errs = buildInterface(nodes[name], errs)
	}
	if len(errs) > 0 {
		return nil, nil, nil, errs
	}

	order := topo(nodes)
	if len(order) != len(nodes) {
		return nil, nil, nil, []diag.Error{{Title: "INVALID EMBEDDED PRELUDE", Body: "Bundled prelude imports form a cycle."}}
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
	return merged, fixities, owners, errs
}
