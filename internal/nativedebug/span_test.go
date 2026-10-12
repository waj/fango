package nativedebug

import (
	"bytes"
	"strings"
	"testing"

	"github.com/waj/fango/internal/debugger"
)

func TestSourceUnderlines(t *testing.T) {
	const text = "main() = first + second\n"
	for _, tc := range []struct {
		name  string
		spans []*debugger.NativeSpan
		want  string
	}{
		{"exact", []*debugger.NativeSpan{{Start: 9, End: 14}}, "1| main() = first + second\n            ^^^^^"},
		{"enclosing", []*debugger.NativeSpan{{Start: 9, End: 23}, {Start: 9, End: 14}}, "enclosing expression; multiple native sites"},
		{"disjoint", []*debugger.NativeSpan{{Start: 9, End: 14}, {Start: 17, End: 23}}, "expression span unavailable"},
		{"missing", []*debugger.NativeSpan{nil}, "expression span unavailable"},
		{"invalid", []*debugger.NativeSpan{{Start: -1, End: 100}}, "expression span unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &Controller{Manifest: &Manifest{Sources: map[string]string{"Main.fango": text}}, State: State{CurrentThread: &Thread{Location: Location{File: "Main.fango", Line: 1}}}}
			for _, s := range tc.spans {
				c.Manifest.Scopes = append(c.Manifest.Scopes, debugger.NativeScope{File: "Main.fango", Line: 1, Span: s})
			}
			var out bytes.Buffer
			c.show(&out)
			if !strings.Contains(out.String(), tc.want) {
				t.Fatalf("%q", out.String())
			}
		})
	}
}
