package modules

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"
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
	// libraries are the enabled optional libraries, consulted in order
	// after the bundled library and before the source root.
	libraries []LibraryProvider

	nodes map[string]*node
	// visible is each module's transitive instance visibility: its
	// dependencies and everything they see. It becomes InstanceImports.
	visible map[string]map[string]bool
	// fixities is the graph-wide operator table. The REPL's checker shares
	// the same map, so prompt-declared operators and imported ones meet in
	// one place.
	fixities fixity.Table
	observe  StageObserver

	// reads holds the modules read ahead of the current walk; reading counts
	// the ones still running. loaded is the walk's starting snapshot of nodes,
	// so read-ahead goroutines skip installed modules without touching nodes,
	// which the walk's owner goes on to change.
	reads   map[string]*moduleRead
	loaded  map[string]bool
	readMu  sync.Mutex
	reading sync.WaitGroup
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

// NewGraph starts a session graph rooted at root, with the named optional
// libraries enabled, and loads the bundled prelude closure into it, returning
// the prelude the way Prelude does so the checker installs the same
// declarations either way.
func NewGraph(root string, libraries []string) (*Graph, *PreludeResult, []diag.Error) {
	g := newGraph(FSProvider{Root: root})
	var errs []diag.Error
	if g.libraries, errs = enableLibraries(libraries); len(errs) > 0 {
		return nil, nil, errs
	}
	p, errs := g.loadPrelude()
	if len(errs) > 0 {
		return nil, nil, errs
	}
	return g, p, nil
}

// Loaded reports whether the graph already holds module name.
func (g *Graph) Loaded(name string) bool { return g.nodes[name] != nil }

// Modules names every module an import could load, sorted: the bundled
// library, the enabled libraries, and, for a filesystem source root, the
// modules under it.
func (g *Graph) Modules() []string {
	names, _ := BundledModules()
	for _, library := range g.libraries {
		provided, _ := library.Modules()
		names = append(names, provided...)
	}
	if l, ok := g.local.(interface{ Modules() []string }); ok {
		names = append(names, l.Modules()...)
	}
	sort.Strings(names)
	return slices.Compact(names)
}

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
	errs := g.load(pending, name, at)
	g.discovered()
	if len(errs) > 0 {
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
	parseStart := time.Now()
	read := g.readAhead(name)
	<-read.done
	if read.fail != nil {
		return read.fail(at)
	}
	path, b, bundled, mm := read.path, read.content, read.bundled, read.mod
	if mm.Header == nil {
		return []diag.Error{diag.Errorf(at, "MISSING MODULE HEADER", "Imported file `%s` must declare `module %s exposing (...)`.", path, name)}
	}
	if mm.Header.Name != name {
		return []diag.Error{diag.Errorf(mm.Header.NameSpan, "MODULE/PATH MISMATCH", "File `%s` must declare module `%s`, but declares `%s`.", path, name, mm.Header.Name)}
	}
	g.observe.Timed("parse", name, parseStart)
	n := &node{name: name, path: path, content: b, mod: mm, sourceHash: hashBytes(b), bundled: bundled, nativeModule: name}
	n.deps = read.deps
	var np string
	var nb []byte
	var ne error
	if bundled {
		np, nb, ne = g.bundled.Native(name)
	} else if read.library != "" {
		np, nb, ne = LibraryProvider{Name: read.library}.Native(name)
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
	g.readDependencies(n.mod, n.deps)
	var errs []diag.Error
	for _, im := range n.mod.Imports {
		errs = append(errs, g.load(pending, im.Module, im.ModuleSpan)...)
	}
	for _, dep := range n.deps {
		errs = append(errs, g.load(pending, dep, at)...)
	}
	return errs
}

// moduleRead is one module's source and syntax, read ahead of the walk that
// consumes it. Reading and parsing depend on nothing the walk computes, so
// they run concurrently across the whole reachable graph; the walk still visits
// modules in its own order, so its diagnostics and node order do not depend on
// which read finishes first.
type moduleRead struct {
	done    chan struct{}
	path    string
	content []byte
	bundled bool
	// library names the enabled library the module was read from.
	library string
	mod     *ast.Module
	deps    []string
	// fail is why the module cannot be used, given the span that asked for it.
	fail func(at source.Span) []diag.Error
}

// readAhead starts reading module name unless a read of it already exists.
// Each read goes on to its own dependencies. discovered ends them. The first
// call of a walk comes from the walk's own goroutine and snapshots the loaded
// modules; a module in that snapshot is not read, and yields nil.
func (g *Graph) readAhead(name string) *moduleRead {
	g.readMu.Lock()
	defer g.readMu.Unlock()
	if read := g.reads[name]; read != nil {
		return read
	}
	if g.reads == nil {
		g.reads = map[string]*moduleRead{}
		g.loaded = make(map[string]bool, len(g.nodes))
		for loaded := range g.nodes {
			g.loaded[loaded] = true
		}
	}
	if g.loaded[name] {
		return nil
	}
	read := &moduleRead{done: make(chan struct{})}
	g.reads[name] = read
	g.reading.Add(1)
	go func() {
		defer g.reading.Done()
		g.readModule(name, read)
		close(read.done)
		if read.fail == nil {
			g.readDependencies(read.mod, read.deps)
		}
	}()
	return read
}

func (g *Graph) readDependencies(m *ast.Module, deps []string) {
	for _, im := range m.Imports {
		g.readAhead(im.Module)
	}
	for _, dep := range deps {
		g.readAhead(dep)
	}
}

// discovered waits out the reads a walk started and forgets them, so a later
// import sees the files as they are then. Every walk must end with it, so
// that no read outlives the walk and the next walk snapshots nodes afresh.
func (g *Graph) discovered() {
	g.reading.Wait()
	g.reads, g.loaded = nil, nil
}

func (g *Graph) readModule(name string, read *moduleRead) {
	fail := func(diagnose func(at source.Span) []diag.Error) { read.fail = diagnose }
	bundlePath, bundleContent, bundleErr := g.bundled.Source(name)
	// No library at all is its own situation. Left as an ordinary miss it
	// would fall through to the source root and report that Prelude is
	// missing from the user's own directory, which sends them looking in
	// entirely the wrong place.
	if libroot.Missing(bundleErr) {
		fail(func(source.Span) []diag.Error {
			return []diag.Error{{Title: "MISSING LIBRARY", Body: bundleErr.Error() + "."}}
		})
		return
	}
	// An enabled library sits between the bundled library and the source
	// root. It reserves its names the way the bundled library does, and may
	// not itself take a bundled name or one another enabled library has.
	library := ""
	var libPath string
	var libContent []byte
	for _, provider := range g.libraries {
		path, content, err := provider.Source(name)
		if err != nil {
			continue
		}
		if bundleErr == nil {
			fail(func(at source.Span) []diag.Error {
				return []diag.Error{diag.Errorf(at, "RESERVED MODULE", "Module `%s` is bundled with Fango as `%s`; the library `%s` cannot also provide it as `%s`.", name, bundlePath, provider.Name, path)}
			})
			return
		}
		if library != "" {
			first := library
			fail(func(at source.Span) []diag.Error {
				return []diag.Error{diag.Errorf(at, "LIBRARY CONFLICT", "Module `%s` is provided by both enabled libraries `%s` and `%s`.", name, first, provider.Name)}
			})
			return
		}
		library, libPath, libContent = provider.Name, path, content
	}
	owner, ownerPath, ownerContent, ownerErr := "is bundled with Fango", bundlePath, bundleContent, bundleErr
	if library != "" {
		owner, ownerPath, ownerContent, ownerErr = "is provided by the library `"+library+"`", libPath, libContent, nil
	}
	var path string
	var b []byte
	var readErr error
	bundled := false
	if g.local == nil {
		if ownerErr != nil {
			if len(g.libraries) > 0 {
				fail(func(at source.Span) []diag.Error {
					return []diag.Error{diag.Errorf(at, "MISSING MODULE", "I cannot find module `%s` in the bundled library or the enabled libraries.", name)}
				})
				return
			}
			fail(func(source.Span) []diag.Error {
				return []diag.Error{{Title: "INVALID BUNDLED PRELUDE", Body: bundleErr.Error()}}
			})
			return
		}
		path, b, bundled = ownerPath, ownerContent, library == ""
	} else {
		localPath, localContent, localErr := g.local.Source(name)
		path, b, readErr = localPath, localContent, localErr
		if ownerErr == nil {
			if localErr == nil {
				fail(func(at source.Span) []diag.Error {
					return []diag.Error{diag.Errorf(at, "RESERVED MODULE", "Module `%s` %s as `%s`; remove or rename the local `%s`.", name, owner, ownerPath, localPath)}
				})
				return
			}
			if _, caseCollision := localErr.(pathCaseError); caseCollision {
				fail(func(at source.Span) []diag.Error {
					return []diag.Error{diag.Errorf(at, "RESERVED MODULE", "Module `%s` %s as `%s`; a case-insensitive local path also conflicts with that reserved name.", name, owner, ownerPath)}
				})
				return
			}
			if !errors.Is(localErr, fs.ErrNotExist) {
				fail(func(at source.Span) []diag.Error {
					return []diag.Error{diag.Errorf(at, "RESERVED MODULE", "Module `%s` %s as `%s`; the local `%s` also occupies that reserved path.", name, owner, ownerPath, localPath)}
				})
				return
			}
			path, b, readErr, bundled = ownerPath, ownerContent, nil, library == ""
		}
	}
	if readErr != nil {
		hint := disabledLibraryHint(name)
		fail(func(at source.Span) []diag.Error {
			if ce, ok := readErr.(pathCaseError); ok {
				return []diag.Error{diag.Errorf(at, "MODULE PATH CASING", "Module `%s` requires exact path casing; expected `%s` but found `%s`.", name, ce.want, ce.found)}
			}
			return []diag.Error{diag.Errorf(at, "MISSING MODULE", "I cannot find module `%s`; expected `%s` beneath the entry directory.%s", name, path, hint)}
		})
		return
	}
	read.library = library
	mod, errs := parse(source.NewFile(path, b))
	if len(errs) > 0 {
		fail(func(source.Span) []diag.Error { return errs })
		return
	}
	read.path, read.content, read.bundled, read.mod = path, b, bundled, mod
	read.deps = syntaxDependencies(mod, name)
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
	for _, root := range []string{PreludeModule, MetaModule, DeriveModule, ListModule, TupleModule, RegexModule} {
		errs = append(errs, g.load(pending, root, source.Span{})...)
	}
	g.discovered()
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

// disabledLibraryHint suggests the library that would provide a missing
// module, when one in the library root does but is not enabled.
func disabledLibraryHint(name string) string {
	available, err := libroot.Libraries()
	if err != nil {
		return ""
	}
	for _, library := range available {
		if _, _, err := (LibraryProvider{Name: library}).Source(name); err == nil {
			return fmt.Sprintf(" The library `%s` provides it; enable it with `--lib %s`.", library, library)
		}
	}
	return ""
}
