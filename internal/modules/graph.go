package modules

import (
	"errors"
	"io/fs"
	"maps"
	"sort"
	"strings"
	"time"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/fixity"
	"github.com/waj/fango/internal/libroot"
	"github.com/waj/fango/internal/source"
)

// Graph is the persistent module graph of one compilation or one REPL
// session. Batch loading fills it once; the REPL grows it import by import.
// Every node it holds has its interface built and its declarations resolved,
// so a later module — or a later prompt — resolves against them without
// touching them again.
type Graph struct {
	// local supplies source-root modules; nil restricts the graph to bundled
	// modules, which is how the embedded prelude is built.
	local   Provider
	bundled BundledProvider

	nodes map[string]*node
	// visible is each module's transitive instance visibility: its
	// dependencies and everything they see. It becomes InstanceImports.
	visible map[string]map[string]bool
	// fixities is the graph-wide operator table. The REPL's checker shares
	// the same map, so prompt-declared operators and imported ones meet in
	// one place.
	fixities fixity.Table
	observe  StageObserver
}

// Increment is what one import adds to a graph: the newly loaded modules in
// dependency order, their resolved declarations, and the metadata a checker
// needs to take them in. Natives holds the user sidecars only; the bundled
// ones are already in the standard worker every session starts with.
type Increment struct {
	Modules         []string
	Decls           []ast.Decl
	InstanceImports map[string]map[string]bool
	Natives         []NativeSource
	// Units are the increment's modules as a checker takes them in, in the
	// same shape a batch entry graph produces, so both reach the same
	// per-module artifact identity.
	Units []ResolvedModule
	// FixityHash is the graph-wide operator table in effect for this
	// increment. A later increment may widen it; artifacts record the table
	// their module was resolved under.
	FixityHash string
}

func newGraph(local Provider) *Graph {
	return &Graph{local: local, nodes: map[string]*node{}, visible: map[string]map[string]bool{}, fixities: fixity.Builtin()}
}

// NewGraph starts a session graph rooted at root and loads the bundled
// prelude closure into it, returning the prelude the way Prelude does so the
// checker installs the same declarations either way.
func NewGraph(root string) (*Graph, *PreludeResult, []diag.Error) {
	g := newGraph(FSProvider{Root: root})
	p, errs := g.loadPrelude()
	if len(errs) > 0 {
		return nil, nil, errs
	}
	return g, p, nil
}

// Loaded reports whether the graph already holds module name.
func (g *Graph) Loaded(name string) bool { return g.nodes[name] != nil }

// Checkpoint returns a function that forgets every module added after this
// point and restores the operator table, so a failed import leaves the graph
// as it was.
func (g *Graph) Checkpoint() func() {
	had := make(map[string]bool, len(g.nodes))
	for name := range g.nodes {
		had[name] = true
	}
	fixities := maps.Clone(g.fixities)
	return func() {
		for name := range g.nodes {
			if !had[name] {
				delete(g.nodes, name)
				delete(g.visible, name)
			}
		}
		for op := range g.fixities {
			delete(g.fixities, op)
		}
		maps.Copy(g.fixities, fixities)
	}
}

// Import loads module name and its not-yet-loaded dependencies. A module
// already in the graph yields an empty increment.
func (g *Graph) Import(name string, at source.Span) (*Increment, []diag.Error) {
	pending := map[string]*node{}
	if errs := g.load(pending, name, at); len(errs) > 0 {
		return nil, errs
	}
	order, errs := g.complete(pending)
	if len(errs) > 0 {
		return nil, errs
	}
	return g.increment(order), nil
}

func (g *Graph) increment(order []string) *Increment {
	inc := &Increment{Modules: order, InstanceImports: map[string]map[string]bool{}, FixityHash: fixity.Hash(g.fixities)}
	for _, name := range order {
		n := g.nodes[name]
		inc.Decls = append(inc.Decls, n.resolved...)
		inc.InstanceImports[name] = g.visible[name]
		inc.Units = append(inc.Units, g.resolvedModule(name, name, DependencyRole, ""))
		if n.native != nil && !n.bundled {
			inc.Natives = append(inc.Natives, NativeSource{Module: n.nativeModule, Path: n.nativePath, Content: n.native})
		}
	}
	return inc
}

// load reads module name into pending, unless the graph or pending already
// holds it, and recurses into its imports and syntax-driven dependencies.
// The diagnostics are the batch loader's; a bundled-only graph reports a
// missing module as a broken bundled prelude, since nothing a user wrote
// can be at fault.
func (g *Graph) load(pending map[string]*node, name string, at source.Span) []diag.Error {
	if g.nodes[name] != nil || pending[name] != nil {
		return nil
	}
	bundlePath, bundleContent, bundleErr := g.bundled.Source(name)
	// No library at all is its own situation. Left as an ordinary miss it
	// would fall through to the source root and report that Prelude is
	// missing from the user's own directory, which sends them looking in
	// entirely the wrong place.
	if libroot.Missing(bundleErr) {
		return []diag.Error{{Title: "MISSING LIBRARY", Body: bundleErr.Error() + "."}}
	}
	var path string
	var b []byte
	var readErr error
	bundled := false
	if g.local == nil {
		if bundleErr != nil {
			return []diag.Error{{Title: "INVALID BUNDLED PRELUDE", Body: bundleErr.Error()}}
		}
		path, b, bundled = bundlePath, bundleContent, true
	} else {
		localPath, localContent, localErr := g.local.Source(name)
		path, b, readErr = localPath, localContent, localErr
		if bundleErr == nil {
			if localErr == nil {
				return []diag.Error{diag.Errorf(at, "RESERVED MODULE", "Module `%s` is bundled with Fango as `%s`; remove or rename the local `%s`.", name, bundlePath, localPath)}
			}
			if _, caseCollision := localErr.(pathCaseError); caseCollision {
				return []diag.Error{diag.Errorf(at, "RESERVED MODULE", "Module `%s` is bundled with Fango as `%s`; a case-insensitive local path also conflicts with that reserved name.", name, bundlePath)}
			}
			if !errors.Is(localErr, fs.ErrNotExist) {
				return []diag.Error{diag.Errorf(at, "RESERVED MODULE", "Module `%s` is bundled with Fango as `%s`; the local `%s` also occupies that reserved path.", name, bundlePath, localPath)}
			}
			path, b, readErr, bundled = bundlePath, bundleContent, nil, true
		}
	}
	if readErr != nil {
		if ce, ok := readErr.(pathCaseError); ok {
			return []diag.Error{diag.Errorf(at, "MODULE PATH CASING", "Module `%s` requires exact path casing; expected `%s` but found `%s`.", name, ce.want, ce.found)}
		}
		return []diag.Error{diag.Errorf(at, "MISSING MODULE", "I cannot find module `%s`; expected `%s` beneath the entry directory.", name, path)}
	}
	mf := source.NewFile(path, b)
	parseStart := time.Now()
	mm, errs := parse(mf)
	if len(errs) > 0 {
		return errs
	}
	if mm.Header == nil {
		return []diag.Error{diag.Errorf(at, "MISSING MODULE HEADER", "Imported file `%s` must declare `module %s exposing (...)`.", path, name)}
	}
	if mm.Header.Name != name {
		return []diag.Error{diag.Errorf(mm.Header.NameSpan, "MODULE/PATH MISMATCH", "File `%s` must declare module `%s`, but declares `%s`.", path, name, mm.Header.Name)}
	}
	g.observe.Timed("parse", name, parseStart)
	n := &node{name: name, path: path, content: b, mod: mm, sourceHash: hashBytes(b), bundled: bundled, nativeModule: name}
	n.deps = syntaxDependencies(mm, name)
	var np string
	var nb []byte
	var ne error
	if bundled {
		np, nb, ne = g.bundled.Native(name)
	} else {
		np, nb, ne = g.local.Native(name)
	}
	if ne == nil {
		n.nativePath, n.native = np, nb
	}
	pending[name] = n
	return g.loadDeps(pending, n, at)
}

// loadDeps loads a pending node's imports and syntax-driven dependencies,
// collecting every diagnostic rather than stopping at the first.
func (g *Graph) loadDeps(pending map[string]*node, n *node, at source.Span) []diag.Error {
	var errs []diag.Error
	for _, im := range n.mod.Imports {
		errs = append(errs, g.load(pending, im.Module, im.ModuleSpan)...)
	}
	for _, dep := range n.deps {
		errs = append(errs, g.load(pending, dep, at)...)
	}
	return errs
}

// complete keeps discovery, graph validation, and per-module resolution as
// explicit phases. Nothing is committed on failure.
func (g *Graph) complete(pending map[string]*node) ([]string, []diag.Error) {
	names, fixities, errs := g.validatePending(pending)
	if len(errs) > 0 {
		return nil, errs
	}
	return g.resolvePending(pending, names, fixities)
}

// validatePending checks the complete discovered graph and builds its
// effective fixity table before any AST is rewritten or name-resolved.
func (g *Graph) validatePending(pending map[string]*node) ([]string, fixity.Table, []diag.Error) {
	names := make([]string, 0, len(pending))
	for name := range pending {
		names = append(names, name)
	}
	sort.Strings(names)
	var errs []diag.Error
	for _, name := range names {
		errs = append(errs, validateModuleDecls(pending[name])...)
	}
	if len(errs) > 0 {
		return nil, nil, errs
	}
	if errs := detectCycles(pending, names); len(errs) > 0 {
		return nil, nil, errs
	}

	// Fixity is graph-wide, so the table needs every parsed file; the
	// rewrite has to finish before name resolution, whose expression walk
	// would otherwise skip the unresolved chains and leave their operands
	// uncanonicalized. `names` is sorted, so the table and its diagnostics
	// are deterministic.
	fixities := maps.Clone(g.fixities)
	for _, name := range names {
		errs = append(errs, fixities.Collect(pending[name].mod.Decls)...)
	}
	if len(errs) > 0 {
		return nil, nil, errs
	}
	return names, fixities, nil
}

// resolvePending rewrites and resolves one fresh parsed tree at a time against
// the already validated graph-wide fixity and interface context.
func (g *Graph) resolvePending(pending map[string]*node, names []string, fixities fixity.Table) ([]string, []diag.Error) {
	var errs []diag.Error
	for _, name := range names {
		errs = append(errs, fixities.Resolve(pending[name].mod)...)
	}
	if len(errs) > 0 {
		return nil, errs
	}

	// Interfaces are built dependency-first, because an exposing list may
	// re-export names that an import brought into scope.
	order := topo(pending)
	all := maps.Clone(g.nodes)
	maps.Copy(all, pending)
	for _, name := range order {
		pending[name].iface, errs = buildInterface(pending[name], all, errs)
	}
	if len(errs) > 0 {
		return nil, errs
	}
	visible := map[string]map[string]bool{}
	for _, name := range order {
		n := pending[name]
		vis := map[string]bool{}
		for _, dep := range dependencyNames(n) {
			vis[dep] = true
			seen := visible[dep]
			if seen == nil {
				seen = g.visible[dep]
			}
			for trans := range seen {
				vis[trans] = true
			}
		}
		visible[name] = vis
		r := resolver{node: n, nodes: all}
		resolveStart := time.Now()
		decls, resolveErrs := r.resolve()
		g.observe.Timed("resolve", name, resolveStart)
		n.resolved = decls
		errs = append(errs, resolveErrs...)
	}
	if len(errs) > 0 {
		return nil, errs
	}
	maps.Copy(g.fixities, fixities)
	for _, name := range order {
		g.nodes[name] = pending[name]
		g.visible[name] = visible[name]
	}
	return order, nil
}

// detectCycles walks the pending nodes with a stable lexical traversal and
// reports the complete repeated-start chain. Dependencies already committed
// to the graph are acyclic and cannot depend on pending nodes, so the walk
// stays within pending.
func detectCycles(pending map[string]*node, names []string) []diag.Error {
	state, stack := map[string]int{}, []string{}
	var errs []diag.Error
	var visit func(string) bool
	visit = func(name string) bool {
		state[name] = 1
		stack = append(stack, name)
		deps := dependencyNames(pending[name])
		sort.Strings(deps)
		for _, dep := range deps {
			if pending[dep] == nil {
				continue
			}
			if state[dep] == 0 && visit(dep) {
				return true
			}
			if state[dep] == 1 {
				i := 0
				for stack[i] != dep {
					i++
				}
				chain := append(append([]string{}, stack[i:]...), dep)
				var sp source.Span
				for _, im := range pending[name].mod.Imports {
					if im.Module == dep {
						sp = im.ModuleSpan
						break
					}
				}
				errs = append(errs, diag.Errorf(sp, "IMPORT CYCLE", "Imports form a cycle: %s.", strings.Join(chain, " -> ")))
				return true
			}
		}
		stack = stack[:len(stack)-1]
		state[name] = 2
		return false
	}
	for _, n := range names {
		if state[n] == 0 && visit(n) {
			return errs
		}
	}
	return errs
}

// loadPrelude loads the bundled roots every session starts from: Prelude,
// which declares the default scope, and the modules surface syntax desugars
// into, since a later prompt can quote, derive, or write `[1]` or `(a, b)`,
// and syntax that always parses must always resolve. Rooting those puts none
// of their names in view — only Prelude.fango does that.
func (g *Graph) loadPrelude() (*PreludeResult, []diag.Error) {
	pending := map[string]*node{}
	var errs []diag.Error
	for _, root := range []string{PreludeModule, MetaModule, DeriveModule, ListModule, TupleModule} {
		errs = append(errs, g.load(pending, root, source.Span{})...)
	}
	if len(errs) > 0 {
		return nil, errs
	}
	order, errs := g.complete(pending)
	if len(errs) > 0 {
		return nil, errs
	}
	merged := &ast.Module{InstanceImports: map[string]map[string]bool{}}
	owners := make(map[string]bool, len(order))
	units := make([]ResolvedModule, 0, len(order))
	for _, name := range order {
		owners[name] = true
		merged.InstanceImports[name] = g.visible[name]
		merged.Decls = append(merged.Decls, g.nodes[name].resolved...)
		units = append(units, g.resolvedModule(name, name, DependencyRole, ""))
	}
	// Prompt declarations are the synthetic entry module. Like a batch entry,
	// they can use instances and derivers from every transitive prelude module.
	promptVisible := make(map[string]bool, len(owners))
	for owner := range owners {
		promptVisible[owner] = true
	}
	merged.InstanceImports[""] = promptVisible
	scope, scopeErrs := preludeScope(g.nodes)
	if len(scopeErrs) > 0 {
		return nil, scopeErrs
	}
	return &PreludeResult{Module: merged, Fixities: g.fixities, Owners: owners, Scope: scope,
		Units: units, FixityHash: fixity.Hash(g.fixities), PromptVisible: promptVisible}, nil
}
