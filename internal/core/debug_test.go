package core

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"github.com/waj/fango/internal/debugger"
)

func TestLintRejectsInvalidDebugSites(t *testing.T) {
	for _, kind := range []string{"source", "span", "position", "binding", "duplicate"} {
		t.Run(kind, func(t *testing.T) {
			site := &debugger.Site{Source: &debugger.Source{Name: "test", Text: "x", Hash: fmt.Sprintf("%x", sha256.Sum256([]byte("x")))}, Start: 0, End: 1, Line: 1, Column: 1}
			site.Bindings = []debugger.Binding{{Name: "x", Runtime: "x", Type: "Int"}}
			switch kind {
			case "source":
				site.Source.Hash = "outdated"
			case "span":
				site.End = 2
			case "position":
				site.Line = 2
			case "binding":
				site.Bindings[0].Runtime = "missing"
			case "duplicate":
				site.Bindings = append(site.Bindings, site.Bindings[0])
			}
			e := &IntLit{}
			e.SetDebugSite(site)
			l := &linter{scope: map[string]bool{"x": true}}
			l.debugSite(e, "test")
			if len(l.errs) == 0 || !strings.Contains(l.errs[0].Error(), "debug") {
				t.Fatalf("accepted invalid %s metadata", kind)
			}
		})
	}
}
