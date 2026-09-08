package modules

import (
	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/fixity"
	"github.com/waj/fango/internal/source"
)

// Prelude resolves the actual bundled sources using the batch resolver, and
// returns the operator table alongside the merged module: the REPL parses
// each prompt entry itself, so it needs the same fixities the bundled
// sources declare.
func Prelude() (*ast.Module, fixity.Table, []diag.Error) {
	nodes := map[string]*node{}
	var errs []diag.Error
	// The prompt can contain a quote, so the REPL always has Meta available.
	order := []string{"Basics", "Meta", "Derive", "Maybe", "IO"}
	for _, name := range order {
		path, data, err := (BundledProvider{}).Source(name)
		if err != nil {
			return nil, nil, []diag.Error{{Title: "INVALID EMBEDDED PRELUDE", Body: err.Error()}}
		}
		m, es := parse(source.NewFile(path, data))
		errs = append(errs, es...)
		nodes[name] = &node{name: name, path: path, content: data, mod: m, bundled: true}
	}
	if len(errs) > 0 {
		return nil, nil, errs
	}
	// Collect and resolve before name resolution, as batch loading does.
	fixities := fixity.Builtin()
	for _, name := range order {
		errs = append(errs, fixities.Collect(nodes[name].mod.Decls)...)
	}
	for _, name := range order {
		errs = append(errs, fixities.Resolve(nodes[name].mod)...)
	}
	if len(errs) > 0 {
		return nil, nil, errs
	}
	for _, name := range order {
		nodes[name].iface, errs = buildInterface(nodes[name], errs)
	}
	m := &ast.Module{}
	for _, name := range order {
		r := resolver{node: nodes[name], nodes: nodes}
		ds, es := r.resolve()
		m.Decls = append(m.Decls, ds...)
		errs = append(errs, es...)
	}
	return m, fixities, errs
}
