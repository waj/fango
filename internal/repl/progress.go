package repl

import (
	"fmt"
	"io"
	"strings"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/compileevent"
)

// importProgress presents only the active prompt transaction. Startup events
// still reach the caller's observer, but never produce terminal output.
type importProgress struct {
	out   io.Writer
	owner string
}

func (p *importProgress) show(owner string) {
	if p == nil || p.owner == owner {
		return
	}
	p.owner = owner
	fmt.Fprintf(p.out, "\r\x1b[2Kloading %s", owner)
}

func (p *importProgress) observe(event compileevent.Event) {
	if p.owner == "" {
		return
	}
	if event.Stage == "check" && event.Begin || event.Stage == "checked-cache-hit" {
		p.show(event.Owner)
	}
}

func (p *importProgress) clear() {
	if p == nil || p.owner == "" {
		return
	}
	fmt.Fprint(p.out, "\r\x1b[2K")
	p.owner = ""
}

func (p *importProgress) finish(imports []ast.Import, loaded []string) {
	p.clear()
	if len(loaded) == 0 {
		return
	}
	fresh := make(map[string]bool, len(loaded))
	for _, name := range loaded {
		fresh[name] = true
	}
	var names []string
	for _, im := range imports {
		if fresh[im.Module] {
			names = append(names, im.Module)
			delete(fresh, im.Module)
		}
	}
	fmt.Fprintf(p.out, "loaded %s", strings.Join(names, ", "))
	if n := len(fresh); n > 0 {
		suffix := "modules"
		if n == 1 {
			suffix = "module"
		}
		fmt.Fprintf(p.out, " (and %d other %s)", n, suffix)
	}
	fmt.Fprintln(p.out)
}
