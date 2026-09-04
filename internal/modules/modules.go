// Package modules discovers, validates, and resolves a local source-module
// graph before the existing single-program checker sees it.
package modules

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	fango "github.com/waj/fango"
	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/lexer"
	"github.com/waj/fango/internal/parser"
	"github.com/waj/fango/internal/source"
)

// Provider is the package-resolution seam shared by local and compiler-bundled
// modules.
type Provider interface {
	Source(module string) (path string, content []byte, err error)
}

type FSProvider struct{ Root string }

type pathCaseError struct{ want, found string }

func (e pathCaseError) Error() string {
	return fmt.Sprintf("path casing mismatch: expected %s, found %s", e.want, e.found)
}

func (p FSProvider) Source(module string) (string, []byte, error) {
	rel := filepath.FromSlash(strings.ReplaceAll(module, ".", "/") + ".fango")
	path := filepath.Join(p.Root, rel)
	absRoot, _ := filepath.Abs(p.Root)
	absPath, _ := filepath.Abs(path)
	if absPath != absRoot && !strings.HasPrefix(absPath, absRoot+string(filepath.Separator)) {
		return rel, nil, fmt.Errorf("module path escapes the source root")
	}
	cur := p.Root
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		entries, readErr := os.ReadDir(cur)
		if readErr != nil {
			break
		}
		found := ""
		for _, e := range entries {
			if e.Name() == part {
				found = part
				break
			}
			if strings.EqualFold(e.Name(), part) {
				found = e.Name()
			}
		}
		if found != "" && found != part {
			return rel, nil, pathCaseError{want: part, found: found}
		}
		cur = filepath.Join(cur, part)
	}
	b, err := os.ReadFile(path)
	return filepath.ToSlash(rel), b, err
}

type BundledProvider struct{}

func (BundledProvider) Source(module string) (string, []byte, error) {
	rel := strings.ReplaceAll(module, ".", "/") + ".fango"
	b, err := fs.ReadFile(fango.StdlibFS, "stdlib/"+rel)
	return "<stdlib>/" + rel, b, err
}

func nativeOperations(module string) []string {
	if module == "IO" {
		return []string{"write"}
	}
	return nil
}

type ManifestEntry struct {
	Module string `json:"module"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type Result struct {
	Module           *ast.Module
	Entry            string
	Manifest         []ManifestEntry
	Units            []Unit
	NativeOperations []string
}

// Unit is one source module in dependency-first build order. Name is empty
// for a headerless entry file; imports always contain logical named modules.
type Unit struct {
	Name    string
	Imports []string
	Entry   bool
}

type node struct {
	name, path string
	content    []byte
	mod        *ast.Module
	iface      *iface
	private    bool
	nativeOps  []string
}

type iface struct {
	values, types, ctors, ops  map[string]string
	typeMembers, effectMembers map[string][]string
	openTypes, openEffects     map[string]bool
}

func newIface() *iface {
	return &iface{values: map[string]string{}, types: map[string]string{}, ctors: map[string]string{}, ops: map[string]string{},
		typeMembers: map[string][]string{}, effectMembers: map[string][]string{}, openTypes: map[string]bool{}, openEffects: map[string]bool{}}
}

// Load uses the entry file's directory as the sole source root and returns a
// dependency-first merged surface program with canonical top-level names.
func Load(entry string) (*Result, []diag.Error) {
	abs, err := filepath.Abs(entry)
	if err != nil {
		return nil, []diag.Error{{Title: "SOURCE ERROR", Body: err.Error()}}
	}
	content, err := os.ReadFile(abs)
	if err != nil {
		return nil, []diag.Error{{Title: "SOURCE ERROR", Body: err.Error()}}
	}
	root := filepath.Dir(abs)
	f := source.NewFile(filepath.Base(abs), content)
	m, errs := parse(f)
	if len(errs) > 0 {
		return nil, errs
	}
	if m.Header == nil && len(m.Imports) == 0 {
		h := sha256.Sum256(content)
		return &Result{
			Module:   m,
			Entry:    "main",
			Manifest: []ManifestEntry{{Module: "<entry>", Path: filepath.Base(abs), SHA256: hex.EncodeToString(h[:])}},
			Units:    []Unit{{Entry: true}},
		}, nil
	}
	entryName, private := "<entry>", m.Header == nil
	if !private {
		entryName = m.Header.Name
	}
	bundledProvider := BundledProvider{}
	if !private {
		if path, _, bundleErr := bundledProvider.Source(entryName); bundleErr == nil {
			return nil, []diag.Error{diag.Errorf(m.Header.NameSpan, "RESERVED MODULE", "Module `%s` is bundled with fango as `%s`; local modules cannot use bundled names.", entryName, path)}
		}
	}
	wantEntry := strings.TrimSuffix(filepath.Base(abs), filepath.Ext(abs))
	if !private && m.Header.Name != wantEntry {
		return nil, []diag.Error{diag.Errorf(m.Header.NameSpan, "MODULE/PATH MISMATCH", "The entry file `%s` must declare module `%s`, but declares `%s`.", filepath.Base(abs), wantEntry, m.Header.Name)}
	}
	nodes := map[string]*node{entryName: {name: entryName, path: filepath.Base(abs), content: content, mod: m, private: private}}
	provider := FSProvider{Root: root}
	var load func(string, source.Span)
	load = func(name string, at source.Span) {
		if nodes[name] != nil {
			return
		}
		localPath, localContent, localErr := provider.Source(name)
		bundlePath, bundleContent, bundleErr := bundledProvider.Source(name)
		path, b, readErr, bundled := localPath, localContent, localErr, false
		if bundleErr == nil {
			if localErr == nil {
				errs = append(errs, diag.Errorf(at, "RESERVED MODULE", "Module `%s` is bundled with fango as `%s`; remove or rename the local `%s`.", name, bundlePath, localPath))
				return
			}
			if _, caseCollision := localErr.(pathCaseError); caseCollision {
				errs = append(errs, diag.Errorf(at, "RESERVED MODULE", "Module `%s` is bundled with fango as `%s`; a case-insensitive local path also conflicts with that reserved name.", name, bundlePath))
				return
			}
			if !errors.Is(localErr, fs.ErrNotExist) {
				errs = append(errs, diag.Errorf(at, "RESERVED MODULE", "Module `%s` is bundled with fango as `%s`; the local `%s` also occupies that reserved path.", name, bundlePath, localPath))
				return
			}
			path, b, readErr, bundled = bundlePath, bundleContent, nil, true
		}
		if readErr != nil {
			if ce, ok := readErr.(pathCaseError); ok {
				errs = append(errs, diag.Errorf(at, "MODULE PATH CASING", "Module `%s` requires exact path casing; expected `%s` but found `%s`.", name, ce.want, ce.found))
				return
			}
			errs = append(errs, diag.Errorf(at, "MISSING MODULE", "I cannot find module `%s`; expected `%s` beneath the entry directory.", name, path))
			return
		}
		mf := source.NewFile(path, b)
		mm, es := parse(mf)
		errs = append(errs, es...)
		if len(es) > 0 {
			return
		}
		if mm.Header == nil {
			errs = append(errs, diag.Errorf(at, "MISSING MODULE HEADER", "Imported file `%s` must declare `module %s exposing (...)`.", path, name))
			return
		}
		if mm.Header.Name != name {
			errs = append(errs, diag.Errorf(mm.Header.NameSpan, "MODULE/PATH MISMATCH", "File `%s` must declare module `%s`, but declares `%s`.", path, name, mm.Header.Name))
			return
		}
		n := &node{name: name, path: path, content: b, mod: mm}
		if bundled {
			n.nativeOps = nativeOperations(name)
		}
		nodes[name] = n
		for _, im := range mm.Imports {
			load(im.Module, im.ModuleSpan)
		}
	}
	for _, im := range m.Imports {
		load(im.Module, im.ModuleSpan)
	}
	if len(errs) > 0 {
		return nil, errs
	}

	// Detect cycles with a stable lexical traversal and retain the complete
	// repeated-start chain in the diagnostic.
	state, stack := map[string]int{}, []string{}
	var visit func(string) bool
	visit = func(name string) bool {
		state[name] = 1
		stack = append(stack, name)
		deps := dependencyNames(nodes[name])
		sort.Strings(deps)
		for _, dep := range deps {
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
				for _, im := range nodes[name].mod.Imports {
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
	names := make([]string, 0, len(nodes))
	for n := range nodes {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if state[n] == 0 && visit(n) {
			return nil, errs
		}
	}

	for _, n := range nodes {
		n.iface, errs = buildInterface(n, errs)
	}
	if len(errs) > 0 {
		return nil, errs
	}
	order := topo(nodes)
	merged := &ast.Module{}
	for _, name := range order {
		r := resolver{node: nodes[name], nodes: nodes}
		decls, es := r.resolve()
		errs = append(errs, es...)
		merged.Decls = append(merged.Decls, decls...)
	}
	if len(errs) > 0 {
		return nil, errs
	}
	manifest := make([]ManifestEntry, 0, len(order))
	units := make([]Unit, 0, len(order))
	var nativeOps []string
	for _, name := range order {
		n := nodes[name]
		h := sha256.Sum256(n.content)
		manifest = append(manifest, ManifestEntry{Module: name, Path: n.path, SHA256: hex.EncodeToString(h[:])})
		for _, op := range n.nativeOps {
			nativeOps = append(nativeOps, canonical(name, op))
		}
		unitName := name
		if n.private {
			unitName = ""
		}
		units = append(units, Unit{Name: unitName, Imports: dependencyNames(n), Entry: name == entryName})
	}
	entrySymbol := "main"
	if !private {
		entrySymbol = canonical(entryName, "main")
	}
	return &Result{Module: merged, Entry: entrySymbol, Manifest: manifest, Units: units, NativeOperations: nativeOps}, nil
}

func parse(f *source.File) (*ast.Module, []diag.Error) {
	toks, errs := lexer.Lex(f)
	if len(errs) > 0 {
		return nil, errs
	}
	return parser.Parse(toks, f)
}

func dependencyNames(n *node) []string {
	out := make([]string, len(n.mod.Imports))
	for i, im := range n.mod.Imports {
		out[i] = im.Module
	}
	return out
}

func topo(nodes map[string]*node) []string {
	indegree, users := map[string]int{}, map[string][]string{}
	for name, n := range nodes {
		indegree[name] = len(n.mod.Imports)
		for _, im := range n.mod.Imports {
			users[im.Module] = append(users[im.Module], name)
		}
	}
	var ready []string
	for name, n := range indegree {
		if n == 0 {
			ready = append(ready, name)
		}
	}
	var out []string
	for len(ready) > 0 {
		sort.Strings(ready)
		name := ready[0]
		ready = ready[1:]
		out = append(out, name)
		for _, user := range users[name] {
			indegree[user]--
			if indegree[user] == 0 {
				ready = append(ready, user)
			}
		}
	}
	return out
}

func canonical(module, name string) string { return module + "." + name }

func buildInterface(n *node, errs []diag.Error) (*iface, []diag.Error) {
	if n.private {
		return newIface(), errs
	}
	all := newIface()
	for _, name := range n.nativeOps {
		all.values[name] = canonical(n.name, name)
		all.ops[name] = canonical(n.name, name)
	}
	for _, d := range n.mod.Decls {
		switch d := d.(type) {
		case *ast.ValueDecl:
			all.values[d.Name] = canonical(n.name, d.Name)
		case *ast.TypeDecl:
			all.types[d.Name] = canonical(n.name, d.Name)
			for _, c := range d.Ctors {
				all.typeMembers[d.Name] = append(all.typeMembers[d.Name], c.Name)
				all.ctors[c.Name] = canonical(n.name, c.Name)
			}
		case *ast.EffectDecl:
			all.types[d.Name] = canonical(n.name, d.Name)
			for _, op := range d.Ops {
				all.effectMembers[d.Name] = append(all.effectMembers[d.Name], op.Name)
				all.ops[op.Name] = canonical(n.name, op.Name)
				all.values[op.Name] = canonical(n.name, op.Name)
			}
		}
	}
	pub := newIface()
	ex := n.mod.Header.Exposing
	if ex.All {
		for k, v := range all.values {
			pub.values[k] = v
		}
		for k, v := range all.types {
			pub.types[k] = v
		}
		for k, v := range all.ctors {
			pub.ctors[k] = v
		}
		for k, v := range all.ops {
			pub.ops[k] = v
		}
		for k, v := range all.typeMembers {
			pub.typeMembers[k] = v
			pub.openTypes[k] = true
		}
		for k, v := range all.effectMembers {
			pub.effectMembers[k] = v
			pub.openEffects[k] = true
		}
		return pub, errs
	}
	seen := map[string]bool{}
	for _, item := range ex.Items {
		if seen[item.Name] {
			errs = append(errs, diag.Errorf(item.Sp, "DUPLICATE EXPORT", "`%s` appears more than once in the exposing list.", item.Name))
			continue
		}
		seen[item.Name] = true
		if v, ok := all.values[item.Name]; ok && !item.All {
			pub.values[item.Name] = v
			if op := all.ops[item.Name]; op != "" {
				pub.ops[item.Name] = op
			}
			continue
		}
		if v, ok := all.types[item.Name]; ok {
			pub.types[item.Name] = v
			if item.All {
				if ms, ok := all.typeMembers[item.Name]; ok {
					pub.typeMembers[item.Name] = ms
					pub.openTypes[item.Name] = true
					for _, x := range ms {
						pub.ctors[x] = all.ctors[x]
					}
					continue
				}
				if ms, ok := all.effectMembers[item.Name]; ok {
					pub.effectMembers[item.Name] = ms
					pub.openEffects[item.Name] = true
					for _, x := range ms {
						pub.ops[x] = all.ops[x]
						pub.values[x] = all.values[x]
					}
					continue
				}
			}
			continue
		}
		if _, ctor := all.ctors[item.Name]; ctor {
			errs = append(errs, diag.Errorf(item.Sp, "INVALID EXPORT", "Constructors cannot be exported individually; expose their type with `%s(..)`.", owner(all.typeMembers, item.Name)))
			continue
		}
		errs = append(errs, diag.Errorf(item.Sp, "UNKNOWN EXPORT", "Module `%s` has no declaration named `%s`.", n.name, item.Name))
	}
	return pub, errs
}

func owner(m map[string][]string, member string) string {
	for n, xs := range m {
		for _, x := range xs {
			if x == member {
				return n
			}
		}
	}
	return "Type"
}

func (i *iface) selection(ex *ast.Exposing, at source.Span) (*iface, []diag.Error) {
	if ex == nil {
		return newIface(), nil
	}
	if ex.All {
		return i, nil
	}
	out := newIface()
	var errs []diag.Error
	for _, item := range ex.Items {
		if v, ok := i.values[item.Name]; ok && !item.All {
			out.values[item.Name] = v
			if op := i.ops[item.Name]; op != "" {
				out.ops[item.Name] = op
			}
			continue
		}
		if v, ok := i.types[item.Name]; ok {
			out.types[item.Name] = v
			if item.All {
				if !i.openTypes[item.Name] && !i.openEffects[item.Name] {
					errs = append(errs, diag.Errorf(item.Sp, "NON-PUBLIC IMPORT", "Module does not publicly expose the members of `%s`.", item.Name))
					continue
				}
				if i.openTypes[item.Name] {
					out.openTypes[item.Name] = true
					for _, x := range i.typeMembers[item.Name] {
						out.ctors[x] = i.ctors[x]
					}
				}
				if i.openEffects[item.Name] {
					out.openEffects[item.Name] = true
					for _, x := range i.effectMembers[item.Name] {
						out.ops[x] = i.ops[x]
						out.values[x] = i.values[x]
					}
				}
			}
			continue
		}
		errs = append(errs, diag.Errorf(item.Sp, "UNKNOWN IMPORT", "The imported module does not publicly expose `%s`.", item.Name))
	}
	return out, errs
}

type resolver struct {
	node                  *node
	nodes                 map[string]*node
	errs                  []diag.Error
	vals, tys, ctors, ops map[string]string
	quals                 map[string]*iface
}

func (r *resolver) canon(name string) string {
	if r.node.private {
		return name
	}
	return canonical(r.node.name, name)
}

func (r *resolver) resolve() ([]ast.Decl, []diag.Error) {
	r.vals = map[string]string{"print": "print", "readLine": "readLine"}
	r.tys = map[string]string{"Int": "Int", "Float": "Float", "String": "String", "Bool": "Bool", "()": "()", "IO": "IO"}
	r.ctors = map[string]string{"True": "True", "False": "False"}
	r.ops = map[string]string{"print": "print", "readLine": "readLine"}
	r.quals = map[string]*iface{}
	seenModules, aliases, fullQualifiers := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, im := range r.node.mod.Imports {
		fullQualifiers[im.Module] = true
	}
	for _, im := range r.node.mod.Imports {
		if seenModules[im.Module] {
			r.errs = append(r.errs, diag.Errorf(im.ModuleSpan, "DUPLICATE IMPORT", "Module `%s` is imported more than once.", im.Module))
			continue
		}
		seenModules[im.Module] = true
		dep := r.nodes[im.Module]
		if dep == nil {
			continue
		}
		r.quals[im.Module] = dep.iface
		if im.Alias != "" {
			if aliases[im.Alias] || fullQualifiers[im.Alias] || r.quals[im.Alias] != nil {
				r.errs = append(r.errs, diag.Errorf(im.AliasSpan, "DUPLICATE IMPORT ALIAS", "The qualifier `%s` is already in use.", im.Alias))
			} else {
				aliases[im.Alias] = true
				r.quals[im.Alias] = dep.iface
			}
		}
		sel, es := dep.iface.selection(im.Exposing, im.ModuleSpan)
		r.errs = append(r.errs, es...)
		r.merge(sel, im.ModuleSpan)
	}
	// Types, effects, constructors, and operations are module-wide, matching
	// the checker's existing mutually-recursive declaration pass.
	for _, d := range r.node.mod.Decls {
		switch d := d.(type) {
		case *ast.TypeDecl:
			r.add(r.tys, d.Name, r.canon(d.Name), d.NameSpan)
			for _, c := range d.Ctors {
				r.add(r.ctors, c.Name, r.canon(c.Name), c.NameSpan)
			}
		case *ast.EffectDecl:
			r.add(r.tys, d.Name, r.canon(d.Name), d.NameSpan)
			for _, o := range d.Ops {
				r.add(r.vals, o.Name, r.canon(o.Name), o.NameSpan)
				r.add(r.ops, o.Name, r.canon(o.Name), o.NameSpan)
			}
		}
	}
	var out []ast.Decl
	for _, d := range r.node.mod.Decls {
		switch d := d.(type) {
		case *ast.ValueDecl:
			surface := d.Name
			canon := r.canon(surface)
			if _, exists := r.vals[surface]; exists {
				r.errs = append(r.errs, diag.Errorf(d.NameSpan, "UNQUALIFIED COLLISION", "The value `%s` collides with an exposed import or operation.", d.Name))
			}
			visible := clone(r.vals)
			if len(d.Params) > 0 {
				visible[surface] = canon
			}
			r.typeAnn(d.Ann)
			locals := map[string]bool{}
			for _, p := range d.Params {
				if p.Name != "_" && p.Name != "()" {
					r.checkBinder(p.Name, p.Sp, visible)
					locals[p.Name] = true
				}
			}
			r.expr(d.Body, visible, locals)
			d.Name = canon
			r.vals[surface] = canon
			out = append(out, d)
		case *ast.TypeDecl:
			d.Name = r.canon(d.Name)
			for i := range d.Ctors {
				d.Ctors[i].Name = r.canon(d.Ctors[i].Name)
				for _, a := range d.Ctors[i].Args {
					r.typ(a)
				}
			}
			out = append(out, d)
		case *ast.EffectDecl:
			d.Name = r.canon(d.Name)
			for i := range d.Ops {
				d.Ops[i].Name = r.canon(d.Ops[i].Name)
				r.typ(d.Ops[i].Type)
			}
			out = append(out, d)
		}
	}
	return out, r.errs
}

func clone(m map[string]string) map[string]string {
	n := map[string]string{}
	for k, v := range m {
		n[k] = v
	}
	return n
}
func (r *resolver) add(m map[string]string, k, v string, sp source.Span) {
	if _, ok := m[k]; ok {
		r.errs = append(r.errs, diag.Errorf(sp, "UNQUALIFIED COLLISION", "The name `%s` collides with an exposed import or builtin.", k))
		return
	}
	m[k] = v
}
func (r *resolver) merge(i *iface, sp source.Span) {
	for k, v := range i.values {
		r.add(r.vals, k, v, sp)
	}
	for k, v := range i.types {
		r.add(r.tys, k, v, sp)
	}
	for k, v := range i.ctors {
		r.add(r.ctors, k, v, sp)
	}
	for k, v := range i.ops {
		if old := r.ops[k]; old != "" && old != v {
			r.errs = append(r.errs, diag.Errorf(sp, "UNQUALIFIED COLLISION", "The operation `%s` is exposed by more than one import.", k))
		} else {
			r.ops[k] = v
		}
	}
}

func (r *resolver) qualified(name string, ns map[string]string, kind string, sp source.Span) string {
	if !strings.Contains(name, ".") {
		if v := ns[name]; v != "" {
			return v
		}
		return name
	}
	best := ""
	var in *iface
	for q, i := range r.quals {
		if strings.HasPrefix(name, q+".") && len(q) > len(best) {
			best, in = q, i
		}
	}
	if in == nil {
		r.errs = append(r.errs, diag.Errorf(sp, "UNKNOWN QUALIFIER", "No imported module has qualifier `%s`.", name[:strings.LastIndex(name, ".")]))
		return name
	}
	member := strings.TrimPrefix(name, best+".")
	var v string
	switch kind {
	case "value":
		v = in.values[member]
	case "type":
		v = in.types[member]
	case "ctor":
		v = in.ctors[member]
	case "op":
		v = in.ops[member]
	}
	if v == "" {
		r.errs = append(r.errs, diag.Errorf(sp, "PRIVATE OR UNKNOWN NAME", "Module qualifier `%s` does not publicly expose `%s`.", best, member))
		return name
	}
	return v
}

func (r *resolver) typ(t ast.TypeExpr) {
	switch t := t.(type) {
	case *ast.TName:
		t.Name = r.qualified(t.Name, r.tys, "type", t.Sp)
	case *ast.TApp:
		t.Name = r.qualified(t.Name, r.tys, "type", t.NameSp)
		for _, a := range t.Args {
			r.typ(a)
		}
	case *ast.TFunExpr:
		r.typ(t.Arg)
		r.typ(t.Ret)
		if t.Eff != nil {
			for i := range t.Eff.Labels {
				l := &t.Eff.Labels[i]
				l.Name = r.qualified(l.Name, r.tys, "type", l.NameSp)
				for _, a := range l.Args {
					r.typ(a)
				}
			}
		}
	}
}
func (r *resolver) typeAnn(a *ast.TypeAnn) {
	if a != nil {
		r.typ(a.Type)
	}
}

func (r *resolver) expr(e ast.Expr, vals map[string]string, locals map[string]bool) {
	switch e := e.(type) {
	case *ast.Var:
		if !locals[e.Name] {
			e.Name = r.qualified(e.Name, vals, "value", e.Sp)
		}
	case *ast.Ctor:
		e.Name = r.qualified(e.Name, r.ctors, "ctor", e.Sp)
	case *ast.App:
		r.expr(e.Fn, vals, locals)
		r.expr(e.Arg, vals, locals)
	case *ast.Neg:
		r.expr(e.Operand, vals, locals)
	case *ast.If:
		r.expr(e.Cond, vals, locals)
		r.expr(e.Then, vals, locals)
		r.expr(e.Else, vals, locals)
	case *ast.BinOp:
		r.expr(e.L, vals, locals)
		r.expr(e.R, vals, locals)
	case *ast.Lambda:
		ls := copySet(locals)
		for _, p := range e.Params {
			if p.Name != "_" {
				r.checkBinder(p.Name, p.Sp, vals)
				ls[p.Name] = true
			}
		}
		r.expr(e.Body, vals, ls)
	case *ast.Block:
		ls := copySet(locals)
		for i := range e.Binds {
			b := &e.Binds[i]
			r.checkBinder(b.Name, b.NameSpan, vals)
			r.typeAnn(b.Ann)
			inner := copySet(ls)
			for _, p := range b.Params {
				if p.Name != "_" {
					r.checkBinder(p.Name, p.Sp, vals)
					inner[p.Name] = true
				}
			}
			if len(b.Params) > 0 {
				inner[b.Name] = true
			}
			r.expr(b.Body, vals, inner)
			ls[b.Name] = true
		}
		for _, it := range e.Items {
			if it.Expr != nil {
				r.expr(it.Expr, vals, ls)
			}
		}
		r.expr(e.Result, vals, ls)
	case *ast.Case:
		r.expr(e.Scrutinee, vals, locals)
		for i := range e.Branches {
			ls := copySet(locals)
			r.pattern(e.Branches[i].Pattern, ls, vals)
			r.expr(e.Branches[i].Body, vals, ls)
		}
	case *ast.Handle:
		r.expr(e.Body, vals, locals)
		for i := range e.Clauses {
			c := &e.Clauses[i]
			c.Op = r.qualified(c.Op, r.ops, "op", c.OpSpan)
			ls := copySet(locals)
			for _, p := range c.Params {
				if p.Name != "_" && p.Name != "()" {
					r.checkBinder(p.Name, p.Sp, vals)
					ls[p.Name] = true
				}
			}
			r.expr(c.Body, vals, ls)
		}
		if e.Return != nil {
			ls := copySet(locals)
			r.checkBinder(e.Return.Param.Name, e.Return.Param.Sp, vals)
			ls[e.Return.Param.Name] = true
			r.expr(e.Return.Body, vals, ls)
		}
	}
}
func copySet(m map[string]bool) map[string]bool {
	n := map[string]bool{}
	for k, v := range m {
		n[k] = v
	}
	return n
}
func (r *resolver) checkBinder(name string, sp source.Span, vals map[string]string) {
	if vals[name] != "" {
		r.errs = append(r.errs, diag.Errorf(sp, "SHADOWING", "The local name `%s` shadows an imported, builtin, or top-level value.", name))
	}
}

func (r *resolver) pattern(p ast.Pattern, locals map[string]bool, vals map[string]string) {
	switch p := p.(type) {
	case *ast.PVar:
		r.checkBinder(p.Name, p.Sp, vals)
		locals[p.Name] = true
	case *ast.PCtor:
		p.Name = r.qualified(p.Name, r.ctors, "ctor", p.NameSpan)
		for _, a := range p.Args {
			r.pattern(a, locals, vals)
		}
	}
}

func ManifestJSON(entries []ManifestEntry) []byte {
	b, _ := json.MarshalIndent(entries, "", "  ")
	return append(b, '\n')
}
