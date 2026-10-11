package modules

import (
	"context"
	"sort"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/fixity"
	"github.com/waj/fango/internal/source"
)

// Refresh discovers a candidate graph without changing the installed graph.
// Bundled nodes are immutable for the lifetime of a prompt. Local nodes are
// read afresh, together, so removed imports and fixities cannot linger.
func (g *Graph) Refresh(ctx context.Context, imports []ast.Import, promptFixities []ast.Decl) (*Graph, *Increment, []string, []diag.Error) {
	next := newGraph(g.local)
	next.bundled, next.observe = g.bundled, g.observe
	pending := map[string]*node{}
	for name, n := range g.nodes {
		if n.bundled {
			fresh := *n
			var errs []diag.Error
			fresh.mod, errs = parse(source.NewFile(n.path, n.content))
			if len(errs) != 0 {
				return nil, nil, nil, errs
			}
			fresh.resolved, fresh.iface = nil, nil
			pending[name] = &fresh
		}
	}
	if errs := next.fixities.Collect(promptFixities); len(errs) != 0 {
		return nil, nil, nil, errs
	}
	for _, im := range imports {
		if err := ctx.Err(); err != nil {
			return nil, nil, nil, []diag.Error{{Title: "RELOAD CANCELLED", Body: err.Error()}}
		}
		if errs := next.load(pending, im.Module, im.ModuleSpan); len(errs) != 0 {
			return nil, nil, nil, errs
		}
	}
	reachable := map[string]bool{}
	var visit func(string)
	visit = func(name string) {
		if reachable[name] {
			return
		}
		reachable[name] = true
		if n := pending[name]; n != nil {
			for _, dep := range dependencyNames(n) {
				visit(dep)
			}
		}
	}
	for _, root := range []string{PreludeModule, MetaModule, DeriveModule, ListModule, TupleModule, RegexModule} {
		visit(root)
	}
	for _, im := range imports {
		visit(im.Module)
	}
	for name := range pending {
		if !reachable[name] {
			delete(pending, name)
		}
	}
	_, errs := next.complete(pending)
	if len(errs) != 0 {
		return nil, nil, nil, errs
	}
	var changed []string
	for name, n := range next.nodes {
		old := g.nodes[name]
		if !n.bundled && (old == nil || old.sourceHash != n.sourceHash || string(old.native) != string(n.native)) {
			changed = append(changed, name)
		}
	}
	for name, n := range g.nodes {
		if !n.bundled && next.nodes[name] == nil {
			changed = append(changed, name)
		}
	}
	sort.Strings(changed)
	// Include the retained bundled dependencies, in their normal graph order.
	return next, next.increment(topo(next.nodes)), changed, nil
}

// Reimport keeps an accepted import's surviving selectors. Other errors,
// including collisions and a now-private member list, remain ordinary errors.
func (p *Prompt) Reimport(im ast.Import) (ast.Import, []string, []diag.Error) {
	var removed []string
	if im.Exposing != nil && !im.Exposing.All {
		ex := *im.Exposing
		ex.Items = nil
		i := p.g.nodes[im.Module].iface
		for _, item := range im.Exposing.Items {
			if _, value := i.values[item.Name]; !value && i.types[item.Name] == "" {
				removed = append(removed, im.Module+"."+item.Name)
			} else {
				ex.Items = append(ex.Items, item)
			}
		}
		im.Exposing = &ex
	}
	_, errs := p.Import(im)
	return im, removed, errs
}

// PreserveLocals transfers prompt-owned resolver names, checking them against
// the refreshed imports rather than silently shadowing newly exposed names.
func (p *Prompt) PreserveLocals(old *Prompt) []diag.Error {
	var errs []diag.Error
	copyNames := func(dst, src map[string]string) {
		for name, canonical := range src {
			if canonical != name {
				continue
			}
			if existing, ok := dst[name]; ok && existing != canonical {
				errs = append(errs, diag.Error{Title: "UNQUALIFIED COLLISION", Body: "Reloaded imports conflict with prompt declaration `" + name + "`."})
			} else {
				dst[name] = canonical
			}
		}
	}
	copyNames(p.r.vals, old.r.vals)
	copyNames(p.r.tys, old.r.tys)
	copyNames(p.r.ctors, old.r.ctors)
	copyNames(p.r.ops, old.r.ops)
	copyNames(p.r.records, old.r.records)
	for name, visible := range old.r.schemas {
		if old.r.tys[name] == name {
			p.r.schemas[name] = visible
		}
	}
	for label, records := range old.r.recordLabels {
		for _, record := range records {
			if old.r.records[record] == record {
				p.r.recordLabels[label] = append(p.r.recordLabels[label], record)
			}
		}
	}
	return errs
}

func (g *Graph) Fixities() fixity.Table { return g.fixities }
