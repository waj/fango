package elaborate

import (
	"crypto/sha256"
	"fmt"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/debugger"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/types"
)

func (el *elab) debugSite(e ast.Expr) *debugger.Site {
	if !el.ck.RuntimeDebug || e == nil || e.Span().File == nil {
		return nil
	}
	// Blocks carry their children's sites, rather than stopping twice on the
	// first binding. Lambda creation is a value; its body has its own sites.
	switch e.(type) {
	case *ast.Block, *ast.Lambda:
		return nil
	}
	sp := e.Span()
	if el.debugSources == nil {
		el.debugSources = map[*source.File]*debugger.Source{}
	}
	src := el.debugSources[sp.File]
	if src == nil {
		src = &debugger.Source{Name: sp.File.Name, Text: string(sp.File.Content), Hash: fmt.Sprintf("%x", sha256.Sum256(sp.File.Content))}
		if src.Name == "<repl>" {
			src.Name = "<repl:" + src.Hash[:12] + ">"
		}
		el.debugSources[sp.File] = src
	}
	p := sp.StartPos()
	site := &debugger.Site{Source: src, Start: sp.Start, End: sp.End, Line: p.Line, Column: p.Col}
	seen := map[string]bool{}
	for i := len(el.scope) - 1; i >= 0; i-- {
		b := el.scope[i]
		if seen[b.source] || b.source == "_" || b.source == "()" {
			continue
		}
		seen[b.source] = true
		site.Bindings = append(site.Bindings, debugger.Binding{Name: b.source, Runtime: b.name, Type: types.Show(b.ty)})
	}
	return site
}

func attachDebug(e core.Expr, site *debugger.Site) {
	if e != nil && site != nil && e.DebugSite() == nil {
		e.SetDebugSite(site)
	}
}
