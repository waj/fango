package format

import (
	"sort"
	"strings"

	"github.com/waj/fango/internal/ast"
)

// itemKind orders exposed names by what they name: types, effects and
// constructors first, then ordinary values, then operators. An uppercase
// initial marks the first group and a non-letter initial the last, which is
// the whole of the language's naming rule.
type itemKind int

const (
	kindType itemKind = iota
	kindValue
	kindOperator
)

func exposeKind(name string) itemKind {
	if name == "" {
		return kindValue
	}
	c := name[0]
	switch {
	case c >= 'A' && c <= 'Z':
		return kindType
	case (c >= 'a' && c <= 'z') || c == '_':
		return kindValue
	default:
		return kindOperator
	}
}

// sortExposed returns the items in canonical order: by kind, then by name.
// It does not mutate its argument, because the same list is read again when
// the formatter verifies its own output.
func sortExposed(items []ast.ExposeItem) []ast.ExposeItem {
	out := append([]ast.ExposeItem(nil), items...)
	sort.SliceStable(out, func(i, j int) bool {
		ki, kj := exposeKind(out[i].Name), exposeKind(out[j].Name)
		if ki != kj {
			return ki < kj
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// sortImports returns the imports in canonical order, by module name.
func sortImports(imports []ast.Import) []ast.Import {
	out := append([]ast.Import(nil), imports...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Module < out[j].Module })
	return out
}

// equivalent compares two modules up to the reorderings the formatter is
// allowed to perform. The AST dump is the oracle for "the same program", but
// it prints imports and exposed names in source order, and sorting them is
// exactly what the formatter does — so both sides are canonicalized first.
// Every other difference still fails the comparison, and that the sorting
// itself is right is held by its own fixtures rather than by this check.
func equivalent(a, b *ast.Module) bool {
	return canonicalDump(a) == canonicalDump(b)
}

func canonicalDump(m *ast.Module) string {
	c := *m
	c.Imports = sortImports(m.Imports)
	for i := range c.Imports {
		if e := c.Imports[i].Exposing; e != nil {
			sorted := *e
			sorted.Items = sortExposed(e.Items)
			c.Imports[i].Exposing = &sorted
		}
	}
	if m.Header != nil {
		h := *m.Header
		h.Exposing.Items = sortExposed(m.Header.Exposing.Items)
		c.Header = &h
	}
	return strings.TrimSpace(ast.Dump(&c))
}
