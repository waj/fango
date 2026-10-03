package modules

import (
	"maps"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/diag"
)

// Prompt is the REPL's name resolver: the batch resolver kept alive across
// inputs, with a synthetic private module standing in for the file a prompt
// never has. Its scope starts where any module's does — the prelude's
// imports — and grows with each accepted import and declaration, so a
// prompt's names reach inference canonical, exactly as a module's do, and a
// module the session never imported stays out of reach.
type Prompt struct {
	g *Graph
	r *resolver
}

// NewPrompt starts a prompt scope over the graph. The graph's node map is
// shared, so modules imported later resolve without rebuilding anything.
func (g *Graph) NewPrompt() *Prompt {
	n := &node{name: "<repl>", path: "<repl>", private: true, mod: &ast.Module{}}
	r := &resolver{node: n, nodes: g.nodes, prompt: true}
	r.init()
	r.applyImports(r.preludeImports(), false)
	return &Prompt{g: g, r: r}
}

// Import loads the module the import names, if the graph lacks it, and
// brings the import's names into the prompt scope. The increment is what the
// checker still has to take in; it is empty when the module was already
// loaded. Importing a module again is cumulative: it adds the names its
// exposing list selects, and repeating an alias for the same module is not a
// conflict.
func (p *Prompt) Import(im ast.Import) (*Increment, []diag.Error) {
	inc, errs := p.g.Import(im.Module, im.ModuleSpan)
	if len(errs) > 0 {
		return nil, errs
	}
	p.r.errs = nil
	p.r.applyImports([]ast.Import{im}, false)
	if len(p.r.errs) > 0 {
		return nil, p.r.errs
	}
	return inc, nil
}

// Decl canonicalizes one prompt declaration in place and binds the names it
// declares. A prompt may rebind its own names; binding one an import exposes
// is the collision it would be in a module.
func (p *Prompt) Decl(d ast.Decl) []diag.Error {
	p.r.errs = nil
	p.r.declareHeaders([]ast.Decl{d})
	p.r.resolveDecls([]ast.Decl{d})
	return p.r.errs
}

// Expr canonicalizes one prompt expression in place. It binds nothing.
func (p *Prompt) Expr(e ast.Expr) []diag.Error {
	p.r.errs = nil
	p.r.expr(e, p.r.vals, map[string]bool{})
	return p.r.errs
}

// Value reports what a prompt value name resolves to, so a prompt level can
// put it back when its own definitions go away.
func (p *Prompt) Value(name string) (string, bool) {
	canonical, ok := p.r.vals[name]
	return canonical, ok
}

// SetValue binds name to canonical, or unbinds it when ok is false.
func (p *Prompt) SetValue(name, canonical string, ok bool) {
	if ok {
		p.r.vals[name] = canonical
	} else {
		delete(p.r.vals, name)
	}
}

// Checkpoint returns a function restoring the scope as it is now, so a
// prompt input that fails after resolution leaves no name behind.
func (p *Prompt) Checkpoint() func() {
	r := p.r
	vals, tys, ctors, ops, records := maps.Clone(r.vals), maps.Clone(r.tys), maps.Clone(r.ctors), maps.Clone(r.ops), maps.Clone(r.records)
	labels := make(map[string][]string, len(r.recordLabels))
	for k, v := range r.recordLabels {
		labels[k] = append([]string(nil), v...)
	}
	schemas, quals := maps.Clone(r.schemas), maps.Clone(r.quals)
	aliases, seen, full := maps.Clone(r.aliases), maps.Clone(r.seenModules), maps.Clone(r.fullQualifiers)
	return func() {
		r.vals, r.tys, r.ctors, r.ops, r.records = vals, tys, ctors, ops, records
		r.recordLabels = labels
		r.schemas, r.quals = schemas, quals
		r.aliases, r.seenModules, r.fullQualifiers = aliases, seen, full
		r.errs = nil
	}
}
