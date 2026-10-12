package nativedebug

import (
	"github.com/waj/fango/internal/debugger"
	"github.com/waj/fango/internal/source"
)

// A native line can contain multiple statement sites. An enclosing recorded
// expression is safe to show, but it must not be presented as an exact subsite.
func (c *Controller) currentSpan(name string, line int) (*debugger.NativeSpan, bool) {
	text, ok := c.Manifest.Sources[name]
	if !ok {
		return nil, false
	}
	file := source.NewFile(name, []byte(text))
	var spans []debugger.NativeSpan
	for _, scope := range c.Manifest.Scopes {
		if scope.File != name || scope.Line != line {
			continue
		}
		s := scope.Span
		if s == nil || s.Start < 0 || s.End <= s.Start || s.End > len(text) {
			return nil, false
		}
		if (source.Span{File: file, Start: s.Start, End: s.End}).StartPos().Line != line {
			return nil, false
		}
		spans = append(spans, *s)
	}
	for _, candidate := range spans {
		contains, distinct := true, false
		for _, s := range spans {
			if candidate.Start > s.Start || candidate.End < s.End {
				contains = false
			}
			if candidate != s {
				distinct = true
			}
		}
		if contains {
			return &candidate, distinct
		}
	}
	return nil, false
}
