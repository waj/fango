package core

import (
	"crypto/sha256"
	"fmt"

	"github.com/waj/fango/internal/debugger"
	"github.com/waj/fango/internal/source"
)

// DebugInfo is optional source metadata, excluded from semantic fingerprints.
// Embedding it lets all executable nodes carry provenance without introducing
// a semantic wrapper that would obscure calls, tail positions, or evidence.
type DebugInfo struct {
	Debug *debugger.Site
}

func (d *DebugInfo) DebugSite() *debugger.Site        { return d.Debug }
func (d *DebugInfo) SetDebugSite(site *debugger.Site) { d.Debug = site }

func CopyDebug(from, to Expr) {
	if from != nil && to != nil && to.DebugSite() == nil {
		to.SetDebugSite(from.DebugSite())
	}
}

func (l *linter) debugSite(e Expr, where string) {
	s := e.DebugSite()
	if s == nil {
		return
	}
	if s.Source == nil {
		l.errorf("%s: debug site has no source", where)
		return
	}
	if l.debugSources == nil {
		l.debugSources = map[*debugger.Source]*source.File{}
	}
	if l.debugSources[s.Source] == nil {
		l.debugSources[s.Source] = source.NewFile(s.Source.Name, []byte(s.Source.Text))
		if s.Source.Name == "" || s.Source.Hash != fmt.Sprintf("%x", sha256.Sum256([]byte(s.Source.Text))) {
			l.errorf("%s: invalid debug source identity", where)
		}
	}
	if s.Start < 0 || s.End < s.Start || s.End > len(s.Source.Text) {
		l.errorf("%s: invalid debug source span", where)
	} else {
		f := l.debugSources[s.Source]
		p := (source.Span{File: f, Start: s.Start, End: s.End}).StartPos()
		if p.Line != s.Line || p.Col != s.Column {
			l.errorf("%s: inconsistent debug source position", where)
		}
	}
	names := map[string]bool{}
	for _, b := range s.Bindings {
		if b.Name == "" || b.Runtime == "" || b.Type == "" || names[b.Name] {
			l.errorf("%s: invalid debug binding %q", where, b.Name)
		}
		names[b.Name] = true
		if !l.scope[b.Runtime] {
			l.errorf("%s: debug binding %q refers to unbound %q", where, b.Name, b.Runtime)
		}
	}
}
