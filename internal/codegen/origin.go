package codegen

import (
	"bytes"
	"encoding/json"
	"fmt"
	goast "go/ast"
	"go/format"
	goparser "go/parser"
	gotoken "go/token"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/debugger"
	"github.com/waj/fango/internal/source"
)

// Source positions reach generated Go as `//line` directives, so a Go stack
// walk reports Fango files and lines (runtime/fangort/trace.go). Emission
// never positions the go/ast it builds; instead each emitted call, operator,
// or exit node is attributed to the innermost own-file origin being emitted,
// and after printing the directives are inserted textually.

// exprOrigin is the source position a Core node carries, if any.
func exprOrigin(e core.Expr) source.Span {
	switch e := e.(type) {
	case *core.App:
		return e.Origin
	case *core.Perform:
		return e.Origin
	case *core.ControlExit:
		return e.Origin
	case *core.Let:
		return e.Origin
	case *core.NativeCall:
		return e.Origin
	}
	return source.Span{}
}

// ownSpan reports whether sp positions code in the owner's own file. Inlined
// dependency bodies carry their module's spans, which a cached consumer may
// see relocated, so only the owner's lines become directives.
func (g *gen) ownSpan(sp source.Span) bool {
	return g.source != "" && sp.File != nil && sp.File.Name == g.source
}

// enter installs sp as the current site while its node is emitted and
// returns the restoring action for the caller to defer.
func (g *gen) enter(sp source.Span) func() {
	if !g.ownSpan(sp) {
		return func() {}
	}
	saved := g.site
	g.site = sp
	return func() { g.site = saved }
}

// mark attributes an emitted node to the current site. Only the node kinds
// the pairing in lineDirectives recognizes are recorded; parentheses are
// unwrapped because the printer adds and the parser drops them freely.
func (g *gen) mark(node goast.Node) goast.Node {
	if g.site.File == nil {
		return node
	}
	target := node
	for {
		paren, ok := target.(*goast.ParenExpr)
		if !ok {
			break
		}
		target = paren.X
	}
	if g.pairedNode(target) {
		g.origins[target] = g.site
		if g.debug {
			g.setDebugOrigin(target, g.debugSite)
		}
	}
	return node
}

// copyOrigin carries an attribution to a node a later pass builds from an
// attributed one.
func (g *gen) copyOrigin(from, to goast.Node) {
	if sp, ok := g.origins[from]; ok {
		g.origins[to] = sp
		if g.debug {
			g.setDebugOrigin(to, g.debugOrigins[from])
		}
	}
}

// pairedNode is the set of node kinds whose counts agree between the tree
// the emitter built and the parse of its printed form, so that the n-th node
// of a kind in one is the n-th in the other.
func pairedNode(n goast.Node) bool {
	switch n.(type) {
	case *goast.CallExpr, *goast.BinaryExpr, *goast.UnaryExpr, *goast.AssignStmt, *goast.BranchStmt:
		return true
	}
	return false
}

// anchor is the position Go attributes a node's execution to.
func anchor(n goast.Node) gotoken.Pos {
	switch n := n.(type) {
	case *goast.CallExpr:
		return n.Lparen
	case *goast.BinaryExpr:
		return n.OpPos
	}
	return n.Pos()
}

type nodeKey struct {
	kind    reflect.Type
	ordinal int
}

// strictDirectivesVariable makes a directive pass that would fall back to
// plain Go fail instead, for the tests that pin directive output.
const strictDirectivesVariable = "FANGO_INTERNAL_STRICT_DIRECTIVES"

// lineDirectives inserts `//line <source>:<line>` above each Go line that
// carries an attributed node, the first attributed node in preorder claiming
// the line, and resets the position to the generated file after a claimed
// run. The printed text is parsed back to learn the Go lines; the result is
// formatted again so it stays a gofmt fixed point. Anything the pass cannot
// account for keeps the plain text.
func (g *gen) lineDirectives(file *goast.File, formatted []byte, unitPath string) ([]byte, error) {
	if g.source == "" || (!g.debug && len(g.origins) == 0) {
		return formatted, nil
	}
	out, err := g.insertDirectives(file, formatted, unitPath)
	if err != nil {
		if g.debug || os.Getenv(strictDirectivesVariable) != "" {
			return nil, fmt.Errorf("codegen: line directives for %s: %w", unitPath, err)
		}
		return formatted, nil
	}
	return out, nil
}

func (g *gen) insertDirectives(file *goast.File, formatted []byte, unitPath string) ([]byte, error) {
	var order []nodeKey
	wanted := map[nodeKey]source.Span{}
	var scopes map[nodeKey]*debugger.Site
	if g.debug {
		scopes = map[nodeKey]*debugger.Site{}
	}
	counts := map[reflect.Type]int{}
	goast.Inspect(file, func(n goast.Node) bool {
		if n == nil || !g.pairedNode(n) {
			return true
		}
		key := nodeKey{reflect.TypeOf(n), counts[reflect.TypeOf(n)]}
		counts[key.kind]++
		if sp, ok := g.origins[n]; ok {
			wanted[key] = sp
			if g.debug {
				scopes[key] = g.debugOrigins[n]
			}
			order = append(order, key)
		}
		return true
	})
	fset := gotoken.NewFileSet()
	parsed, err := goparser.ParseFile(fset, unitPath, formatted, goparser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	lines := map[nodeKey]int{}
	parsedCounts := map[reflect.Type]int{}
	goast.Inspect(parsed, func(n goast.Node) bool {
		if n == nil || !g.pairedNode(n) {
			return true
		}
		key := nodeKey{reflect.TypeOf(n), parsedCounts[reflect.TypeOf(n)]}
		parsedCounts[key.kind]++
		if _, ok := wanted[key]; ok {
			lines[key] = fset.Position(anchor(n)).Line
		}
		return true
	})
	for kind, count := range counts {
		if parsedCounts[kind] != count {
			return nil, fmt.Errorf("%d %s nodes emitted, %d printed", count, kind, parsedCounts[kind])
		}
	}
	claims := map[int]source.Span{}
	var claimedScopes map[int]*debugger.Site
	if g.debug {
		claimedScopes = map[int]*debugger.Site{}
	}
	for _, key := range order {
		line := lines[key]
		if _, taken := claims[line]; !taken {
			claims[line] = wanted[key]
			if g.debug {
				claimedScopes[line] = scopes[key]
			}
		}
	}
	text := strings.Split(strings.TrimSuffix(string(formatted), "\n"), "\n")
	out := make([]string, 0, len(text)+2*len(claims))
	if g.debug {
		// Reset every unclaimed line, including braces and declarations. Otherwise
		// Go increments the previous Fango line across synthetic instructions.
		for i, line := range text {
			directive := "<fango-generated>:1"
			if sp, ok := claims[i+1]; ok {
				directive = g.source + ":" + strconv.Itoa(sp.StartPos().Line)
			}
			out = append(out, "//line "+directive, line)
		}
		// Store only scopes actually claimed by emitted statements. Comments travel
		// with cached Go output but add no code or target runtime dependency.
		var metadata []debugger.NativeScope
		seen := map[string]bool{}
		for line, sp := range claims {
			scope := debugger.NativeScope{File: g.source, Line: sp.StartPos().Line}
			if site := claimedScopes[line]; site != nil {
				scope.Span = &debugger.NativeSpan{Start: site.Start, End: site.End}
				for _, b := range site.Bindings {
					b.Runtime = mangleValue(b.Runtime)
					scope.Bindings = append(scope.Bindings, b)
				}
			}
			encoded, _ := json.Marshal(scope)
			key := string(encoded)
			if !seen[key] {
				metadata = append(metadata, scope)
				seen[key] = true
			}
		}
		sort.Slice(metadata, func(i, j int) bool {
			a, _ := json.Marshal(metadata[i])
			b, _ := json.Marshal(metadata[j])
			return string(a) < string(b)
		})
		encoded, err := json.Marshal(metadata)
		if err != nil {
			return nil, err
		}
		out = append(out, debugScopePrefix+string(encoded))
		layouts, err := json.Marshal(g.nativeDebugTypes(unitPath))
		if err != nil {
			return nil, err
		}
		out = append(out, debugTypePrefix+string(layouts))
		return format.Source([]byte(strings.Join(out, "\n") + "\n"))
	}
	redirected := false
	for i, line := range text {
		// A directive is a full line of its own. It cannot precede a line
		// that must follow `}` directly, as `} else` must, and gofmt would
		// separate it from a top-level declaration with a blank line, so
		// directives go only before indented lines; a reset waits for one.
		body := strings.TrimLeft(line, "\t")
		eligible := body != line && !strings.HasPrefix(body, "}")
		if sp, claimed := claims[i+1]; claimed && eligible {
			out = append(out, "//line "+g.source+":"+strconv.Itoa(sp.StartPos().Line))
			redirected = true
		} else if redirected && eligible {
			out = append(out, "//line "+unitPath+":"+strconv.Itoa(len(out)+2))
			redirected = false
		}
		out = append(out, line)
	}
	joined := []byte(strings.Join(out, "\n") + "\n")
	final, err := format.Source(joined)
	if err != nil {
		return nil, err
	}
	if bytes.Count(final, []byte("\n")) != len(out) {
		return nil, fmt.Errorf("formatting moved lines around the directives")
	}
	return final, nil
}

// markExpr is mark for expression positions.
func (g *gen) markExpr(e goast.Expr) goast.Expr {
	g.mark(e)
	return e
}

// Debug builds use the complete expression sites, including literals and local
// references. Ordinary emission retains the existing stack-trace origins.
func (g *gen) exprOrigin(e core.Expr) source.Span {
	if g.debug && e != nil {
		if site := e.DebugSite(); site != nil && site.Source != nil {
			return source.Span{File: source.NewLineFile(site.Source.Name), Start: site.Line - 1, End: site.Line - 1}
		}
	}
	return exprOrigin(e)
}

func (g *gen) pairedNode(n goast.Node) bool {
	if g.debug {
		switch n.(type) {
		case *goast.ReturnStmt, *goast.DeclStmt, *goast.ExprStmt, *goast.IfStmt, *goast.SwitchStmt:
			return true
		}
	}
	return pairedNode(n)
}

func (g *gen) debugStatements(stmts []goast.Stmt, e core.Expr) {
	if !g.debug {
		return
	}
	sp := g.exprOrigin(e)
	if !g.ownSpan(sp) {
		return
	}
	for _, stmt := range stmts {
		if g.pairedNode(stmt) {
			if _, exists := g.origins[stmt]; !exists {
				g.origins[stmt] = sp
				g.setDebugOrigin(stmt, e.DebugSite())
			}
		}
	}
}

const debugScopePrefix = "//fango:debug-scopes "

// DebugScopes reads emission metadata on both cold and cached debug builds.
func DebugScopes(data []byte) ([]debugger.NativeScope, error) {
	for _, line := range bytes.Split(data, []byte("\n")) {
		if bytes.HasPrefix(line, []byte(debugScopePrefix)) {
			var scopes []debugger.NativeScope
			err := json.Unmarshal(line[len(debugScopePrefix):], &scopes)
			return scopes, err
		}
	}
	return nil, nil
}

func (g *gen) setDebugOrigin(node goast.Node, site *debugger.Site) {
	if g.debugOrigins == nil {
		g.debugOrigins = map[goast.Node]*debugger.Site{}
	}
	g.debugOrigins[node] = site
}

func (g *gen) enterExpr(e core.Expr) func() {
	restore := g.enter(g.exprOrigin(e))
	if !g.debug {
		return restore
	}
	saved := g.debugSite
	if e != nil && e.DebugSite() != nil && g.ownSpan(g.exprOrigin(e)) {
		g.debugSite = e.DebugSite()
	}
	return func() { restore(); g.debugSite = saved }
}

const debugTypePrefix = "//fango:debug-types "

func DebugTypes(data []byte) (map[string]debugger.NativeType, error) {
	for _, line := range bytes.Split(data, []byte("\n")) {
		if bytes.HasPrefix(line, []byte(debugTypePrefix)) {
			var layouts map[string]debugger.NativeType
			err := json.Unmarshal(line[len(debugTypePrefix):], &layouts)
			return layouts, err
		}
	}
	return nil, nil
}
