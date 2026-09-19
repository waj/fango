package infer

import (
	"sort"
	"strings"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/types"
)

// Registration follows source order independently of body checking. Each body
// retains its declaration's evidence cutoff, even when a caller forces it early.
type moduleCheck struct {
	deriverBusy           map[string]bool
	patterns              map[int]*ast.PatternDecl
	current               int
	classAt               map[string]int
	methodAt              map[string]int
	ck                    *Checker
	decls                 []ast.Decl
	cursor                int
	at                    map[string]int
	limits                map[int]int
	values                map[int]*ast.ValueDecl
	instances             map[int]*InstanceInfo
	instanceDone          map[int]bool
	done, staged, staging map[int]bool
	infos                 map[int][]DeclInfo
	errs                  []diag.Error
}

func newModuleCheck(ck *Checker, decls []ast.Decl) *moduleCheck {
	b := &moduleCheck{ck: ck, decls: decls, at: map[string]int{}, limits: map[int]int{}, values: map[int]*ast.ValueDecl{}, instances: map[int]*InstanceInfo{}, instanceDone: map[int]bool{}, done: map[int]bool{}, staged: map[int]bool{}, staging: map[int]bool{}, infos: map[int][]DeclInfo{}}
	b.patterns = map[int]*ast.PatternDecl{}
	b.deriverBusy = map[string]bool{}
	b.classAt, b.methodAt = map[string]int{}, map[string]int{}
	for i, decl := range decls {
		if cl, ok := decl.(*ast.ClassDecl); ok {
			b.classAt[cl.Name] = i
			for _, m := range cl.Methods {
				b.methodAt[m.Name] = i
			}
		}
		if pd, ok := decl.(*ast.PatternDecl); ok {
			names := patternNames(pd.Pattern, nil)
			if len(names) == 0 {
				b.errs = append(b.errs, diag.Errorf(pd.Pattern.Span(), "PATTERN BINDING", "A destructuring binding must bind at least one name."))
				continue
			}
			for _, name := range names {
				if _, dup := b.at[name]; dup || ck.Env.Has(name) {
					b.errs = append(b.errs, diag.Errorf(pd.Pattern.Span(), "MULTIPLE DEFINITIONS", "%s is defined more than once.", name))
				}
				b.at[name] = i
			}
			b.patterns[i] = pd
			b.values[i] = &ast.ValueDecl{Name: names[0], NameSpan: pd.Pattern.Span(), Body: pd.Body}
		}
		if d, ok := decl.(*ast.ValueDecl); ok && d.Native == nil {
			if _, dup := b.at[d.Name]; dup || ck.Env.Has(d.Name) {
				b.errs = append(b.errs, diag.Errorf(d.NameSpan, "MULTIPLE DEFINITIONS", "`%s` is defined more than once.", d.Name))
			}
			b.at[d.Name], b.values[i] = i, d
		}
	}
	for _, decl := range decls {
		if cl, ok := decl.(*ast.ClassDecl); ok {
			for _, m := range cl.Methods {
				if _, exists := b.at[m.Name]; exists {
					b.errs = append(b.errs, diag.Errorf(m.NameSpan, "MULTIPLE DEFINITIONS", "Method %s collides with a module declaration.", m.Name))
				}
			}
		}
	}
	return b
}

func (ck *Checker) instanceLimit() int {
	if ck.sourceLimit != nil {
		return *ck.sourceLimit
	}
	return len(ck.Instances)
}

func (b *moduleCheck) context(i int, owner string, f func()) {
	ck := b.ck
	oldOwner, oldLimit, oldCurrent := ck.CurrentOwner, ck.sourceLimit, b.current
	b.current = i
	limit := b.limits[i]
	ck.CurrentOwner, ck.sourceLimit = owner, &limit
	defer func() { ck.CurrentOwner, ck.sourceLimit, b.current = oldOwner, oldLimit, oldCurrent }()
	f()
}

func (b *moduleCheck) add(i int, infos []DeclInfo, errs []diag.Error) {
	b.infos[i] = append(b.infos[i], infos...)
	b.errs = append(b.errs, errs...)
	if len(errs) == 0 {
		b.ck.Checked = append(b.ck.Checked, infos...)
	}
}

func (b *moduleCheck) registerThrough(end int) {
	for b.cursor <= end && b.cursor < len(b.decls) && len(b.errs) == 0 {
		i := b.cursor
		b.cursor++
		b.limits[i] = len(b.ck.Instances)
		switch d := b.decls[i].(type) {
		case *ast.ClassDecl:
			b.errs = append(b.errs, b.ck.ClassDecl(d)...)
		case *ast.InstanceDecl:
			b.context(i, d.Owner, func() {
				in, es := b.ck.registerInstance(d)
				if in != nil {
					in.Ref = DeclRef{Module: b.ck.moduleName, Index: i}
					in.Cutoff = make([]DeclRef, 0, in.Limit)
					for _, visible := range b.ck.Instances[:in.Limit] {
						in.Cutoff = append(in.Cutoff, visible.Ref)
					}
				}
				b.instances[i] = in
				b.errs = append(b.errs, es...)
			})
			b.limits[i] = len(b.ck.Instances)
		case *ast.DeriverDecl:
			b.deriverBusy[d.Class] = true
			for _, m := range d.Methods {
				b.ensureExpr(&ast.Lambda{Params: m.Params, Body: m.Body})
				for _, eq := range m.Equations {
					b.ensureExpr(&ast.Lambda{Params: eq.Params, Body: eq.Body})
				}
			}
			if len(b.errs) == 0 {
				b.context(i, d.Owner, func() { b.errs = append(b.errs, b.ck.DeriverDecl(d)...) })
			}
			delete(b.deriverBusy, d.Class)
		case *ast.TypeDecl:
			if len(d.Deriving) > 0 {
				for _, dr := range d.Deriving {
					if b.deriverBusy[dr.Name] {
						b.errs = append(b.errs, diag.Errorf(dr.Sp, "STAGE ERROR", "Deriver %s depends on a declaration after this deriving clause.", dr.Name))
					}
				}
				if len(b.errs) > 0 {
					continue
				}
				b.completeInstances(i)
				b.context(i, symbolModule(d.Name), func() { ds, es := b.ck.DeriveDecl(d); b.add(i, ds, es) })
			}

		}
	}
}

func (b *moduleCheck) completeInstances(before int) {
	for i := 0; i < before; i++ {
		in := b.instances[i]
		if in == nil || b.instanceDone[i] {
			continue
		}
		b.instanceDone[i] = true
		d := b.decls[i].(*ast.InstanceDecl)
		for _, m := range d.Methods {
			b.ensureExpr(&ast.Lambda{Params: m.Params, Body: m.Body})
			for _, eq := range m.Equations {
				b.ensureExpr(&ast.Lambda{Params: eq.Params, Body: eq.Body})
			}
		}
		b.context(i, d.Owner, func() {
			for _, m := range d.Methods {
				b.errs = append(b.errs, b.ck.StageDecl(m)...)
			}
			ds, es := b.ck.checkInstance(d, in)
			b.add(i, ds, es)
			for j, m := range d.Methods {
				if j < len(ds) && !b.ck.IsCompileTimeOnly(ds[j].Type) {
					b.errs = append(b.errs, b.ck.checkStageLeaks(m, nil, types.SurfaceName(m.Name))...)
				}
			}
		})
	}
}

// References include callback values and nested bodies. Quotes describe code;
// only their holes execute in the declaring definition.
func references(e ast.Expr, f func(string)) {
	s := &stageChecker{binders: map[string]binderStage{}, reference: f}
	s.expr(e)
}

func (b *moduleCheck) deps(i int) []int {
	seen := map[int]bool{}
	d := b.values[i]
	add := func(name string) {
		if j, ok := b.at[name]; ok {
			seen[j] = true
		}
	}
	if pd := b.patterns[i]; pd != nil {
		s := &stageChecker{binders: map[string]binderStage{}, reference: add}
		s.patternReferences(pd.Pattern)
	}
	for _, eq := range declEquations(d) {
		references(&ast.Lambda{Params: eq.Params, Body: eq.Body}, add)
	}
	var out []int
	for j := range seen {
		out = append(out, j)
	}
	sort.Ints(out)
	return out
}

func (b *moduleCheck) ensureExpr(e ast.Expr) {
	var indices []int
	references(e, func(name string) {
		if i, ok := b.at[name]; ok {
			indices = append(indices, i)
		}
	})
	sort.Ints(indices)
	for _, i := range indices {
		b.ensure(i)
	}
}

func (b *moduleCheck) stage(i int) {
	if b.staged[i] || len(b.errs) > 0 {
		return
	}
	if b.staging[i] {
		return
	}
	b.staging[i] = true
	b.registerThrough(i)
	d := b.values[i]
	b.context(i, symbolModule(d.Name), func() { b.errs = append(b.errs, b.ck.StageDecl(d)...) })
	if pd := b.patterns[i]; pd != nil {
		pd.Body = d.Body
	}
	b.staging[i] = false
	b.staged[i] = true
}

// Tarjan emits dependencies before callers, with source-order traversal and
// source-order members. Rebuild after expansion: generated expressions can add
// references that were absent from the original syntax.
func (b *moduleCheck) groups(root int) [][]int {
	index := 0
	indices, low := map[int]int{}, map[int]int{}
	active := map[int]bool{}
	var stack []int
	var groups [][]int
	var visit func(int)
	visit = func(i int) {
		if b.done[i] {
			return
		}
		index++
		indices[i], low[i] = index, index
		stack = append(stack, i)
		active[i] = true
		b.stage(i)
		for _, j := range b.deps(i) {
			if len(b.values[j].Params) == 0 && j >= i {
				b.errs = append(b.errs, diag.Errorf(b.values[i].NameSpan, "NAMING ERROR", "I don't know a value named `%s`.", b.values[j].Name))
				continue
			}
			if b.done[j] {
				continue
			}
			if indices[j] == 0 {
				visit(j)
				low[i] = min(low[i], low[j])
			} else if active[j] {
				low[i] = min(low[i], indices[j])
			}
		}
		if low[i] == indices[i] {
			var group []int
			for {
				j := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				active[j] = false
				group = append(group, j)
				if i == j {
					break
				}
			}
			sort.Ints(group)
			groups = append(groups, group)
		}
	}
	visit(root)
	return groups
}

func (b *moduleCheck) ensure(i int) {
	if b.done[i] || len(b.errs) > 0 {
		return
	}
	groups := b.groups(i)
	for _, group := range groups {
		if b.done[group[0]] || len(b.errs) > 0 {
			continue
		}
		recursive := len(group) > 1
		if !recursive {
			for _, j := range b.deps(group[0]) {
				recursive = recursive || j == group[0]
			}
		}
		if recursive {
			value := -1
			for _, j := range group {
				if len(b.values[j].Params) == 0 {
					value = j
					break
				}
			}
			if value >= 0 {
				names := b.valueCycle(value, group)
				b.errs = append(b.errs, diag.Errorf(b.values[value].NameSpan, "CYCLIC VALUE DEFINITION", "A recursive dependency group contains an ordinary value: %s.", strings.Join(names, " -> ")))
				return
			}
		}
		if len(group) > 1 {
			b.inferGroup(group)
		} else {
			j := group[0]
			d := b.values[j]
			b.context(j, symbolModule(d.Name), func() {
				if pd := b.patterns[j]; pd != nil {
					ds, es := b.ck.patternDecl(pd, true)
					b.add(j, ds, es)
					return
				}
				info, es := b.ck.Decl(d)
				es = append(es, b.ck.checkStageLeaks(d, info.Type, types.SurfaceName(d.Name))...)
				b.add(j, []DeclInfo{info}, es)
			})
		}
		for _, j := range group {
			b.done[j] = true
		}
	}
}

func (b *moduleCheck) prepareSplice(e ast.Expr, sp source.Span) []diag.Error {
	// A completed group is stage-visible only if every transitive dependency
	// precedes this splice. Completion order alone does not grant visibility.
	seen := map[int]bool{}
	var bad string
	var visit func(int)
	visit = func(i int) {
		if seen[i] || bad != "" {
			return
		}
		seen[i] = true
		d := b.values[i]
		if d.NameSpan.File == sp.File && d.NameSpan.Start >= sp.Start || b.staging[i] {
			bad = d.Name
			return
		}
		for _, j := range b.deps(i) {
			visit(j)
		}
	}
	references(e, func(name string) {
		if i, ok := b.at[name]; ok {
			visit(i)
		}
	})
	if bad != "" {
		return []diag.Error{diag.Errorf(sp, "STAGE ERROR", "Compile-time evaluation depends on `%s`, whose dependency group is not complete before this splice.", bad)}
	}
	before := len(b.errs)
	b.ensureExpr(e)
	// Expansion can introduce dependencies, so check the closure again.
	seen = map[int]bool{}
	references(e, func(name string) {
		if i, ok := b.at[name]; ok {
			visit(i)
		}
	})
	if bad != "" {
		return []diag.Error{diag.Errorf(sp, "STAGE ERROR", "Compile-time evaluation depends on `%s`, declared after this splice.", bad)}
	}
	b.completeInstances(b.current)
	return b.errs[before:]
}

func (b *moduleCheck) run() ([]DeclInfo, []diag.Error) {
	b.registerThrough(len(b.decls) - 1)
	b.completeInstances(len(b.decls))
	for i := range b.decls {
		if b.values[i] != nil {
			b.ensure(i)
		}
	}
	var infos []DeclInfo
	for i := range b.decls {
		infos = append(infos, b.infos[i]...)
	}
	return infos, b.errs
}

func (ck *Checker) valueVisible(name string) bool {
	b := ck.moduleCheck
	if b == nil {
		return true
	}
	if i, ok := b.at[name]; ok {
		if symbolModule(name) == "" && ck.CurrentOwner != "" {
			return false
		}
		return len(b.values[i].Params) > 0 || i < b.current
	}
	if i, ok := b.methodAt[name]; ok {
		return i < b.current
	}
	return true
}

func (ck *Checker) boundName(name string) bool {
	if b := ck.moduleCheck; b != nil {
		if i, ok := b.at[name]; ok && len(b.values[i].Params) > 0 && symbolModule(name) == ck.CurrentOwner {
			return true
		}
	}
	return ck.Env.Has(name) && ck.valueVisible(name)
}

// CheckStageReference covers dependencies exposed by elaboration, including
// calls through concrete dictionaries. REPL generations have no pending module.
func (ck *Checker) CheckStageReference(name string, sp source.Span) []diag.Error {
	b := ck.moduleCheck
	if b == nil {
		return nil
	}
	if i, ok := b.at[name]; ok {
		if !b.done[i] || b.values[i].NameSpan.File == sp.File && b.values[i].NameSpan.Start >= sp.Start {
			return []diag.Error{diag.Errorf(sp, "STAGE ERROR", "Compile-time evaluation depends on %s, whose group is not complete before this splice.", name)}
		}
	}
	return nil
}

// StageInstanceLimit is the splice site's source-position evidence cutoff.
func (ck *Checker) StageInstanceLimit() int { return ck.instanceLimit() }

func (b *moduleCheck) valueCycle(start int, group []int) []string {
	inside := map[int]bool{}
	for _, i := range group {
		inside[i] = true
	}
	seen := map[int]bool{}
	var path []int
	var walk func(int) bool
	walk = func(i int) bool {
		seen[i] = true
		path = append(path, i)
		for _, j := range b.deps(i) {
			if !inside[j] {
				continue
			}
			if j == start {
				path = append(path, start)
				return true
			}
			if !seen[j] && walk(j) {
				return true
			}
		}
		path = path[:len(path)-1]
		return false
	}
	walk(start)
	var names []string
	for _, i := range path {
		names = append(names, b.values[i].Name)
	}
	return names
}
