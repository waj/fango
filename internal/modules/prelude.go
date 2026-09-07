package modules

import (
	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/source"
)

// Prelude resolves the actual bundled sources using the batch resolver.
func Prelude() (*ast.Module, []diag.Error) {
	nodes := map[string]*node{}
	var errs []diag.Error
	order := []string{"Basics", "Maybe", "IO"}
	for _, name := range order {
		path, data, err := (BundledProvider{}).Source(name)
		if err != nil {
			return nil, []diag.Error{{Title: "INVALID EMBEDDED PRELUDE", Body: err.Error()}}
		}
		m, es := parse(source.NewFile(path, data))
		errs = append(errs, es...)
		nodes[name] = &node{name: name, path: path, content: data, mod: m, bundled: true}
	}
	if len(errs) > 0 {
		return nil, errs
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
	return m, errs
}
