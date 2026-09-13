package machine

import (
	"fmt"
	"sort"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

// Lower selects the concrete Machine roots and every transport-polymorphic
// worker they call in Machine context, then lowers only that closed island.
// Direct and Exit definitions are absent from the result and remain on their
// existing backend path.
func Lower(p *core.Prog, b *types.Builtins) (*Prog, []error) {
	if errs := core.LintMachineInput(p, b); len(errs) != 0 {
		return nil, errs
	}
	defs := make(map[string]*core.Def, len(p.Defs))
	selected := map[string]bool{}
	for i := range p.Defs {
		d := &p.Defs[i]
		defs[d.Name] = d
		if d.Control.Transport == types.Machine {
			selected[d.Name] = true
		}
	}

	// A polymorphic call resolves in its enclosing Machine context. Close the
	// selected set before lowering so every Call has a materialized callee.
	for changed := true; changed; {
		changed = false
		for name := range selected {
			d := defs[name]
			if d == nil {
				continue
			}
			core.Rewrite(d.Body, identityType, func(e core.Expr) core.Expr {
				app, ok := e.(*core.App)
				if !ok || app.CalleeKind != core.Worker || app.Control.Resolve(types.Machine) != types.Machine {
					return e
				}
				if ref, ok := app.Callee.(*core.VarRef); ok && defs[ref.Name] != nil && !selected[ref.Name] {
					selected[ref.Name] = true
					changed = true
				}
				return e
			})
		}
	}

	names := make([]string, 0, len(selected))
	for name := range selected {
		names = append(names, name)
	}
	sort.Strings(names)
	out := &Prog{}
	var errs []error
	for _, name := range names {
		w, es := lowerWorker(defs[name], selected)
		if len(es) != 0 {
			errs = append(errs, es...)
			continue
		}
		out.Workers = append(out.Workers, w)
	}
	if len(errs) == 0 {
		errs = append(errs, Lint(out)...)
	}
	return out, errs
}

func identityType(t types.Type) types.Type { return t }

type builder struct {
	def      *core.Def
	selected map[string]bool
	blocks   []Block
	locals   map[string]types.Type
	tmp      int
	errs     []error
}

func lowerWorker(d *core.Def, selected map[string]bool) (Worker, []error) {
	b := &builder{def: d, selected: selected, locals: map[string]types.Type{}}
	argTys, result := core.PeelFun(d.Type, len(d.Params))
	params := make([]Local, len(d.Params))
	for i, name := range d.Params {
		name = b.localName(name)
		params[i] = Local{Name: name, Ty: argTys[i]}
		b.declare(params[i])
	}
	resultLocal := Local{Name: "_machine_result", Ty: result}
	b.declare(resultLocal)
	ret := b.add(&Return{Value: localRef(resultLocal)})
	entry := b.lowerInto(d.Body, resultLocal, ret)
	if len(b.errs) != 0 {
		return Worker{}, b.errs
	}

	locals := make([]Local, 0, len(b.locals))
	for name, ty := range b.locals {
		locals = append(locals, Local{Name: name, Ty: ty})
	}
	sort.Slice(locals, func(i, j int) bool { return locals[i].Name < locals[j].Name })
	w := Worker{Name: d.Name, Owner: d.Owner, Params: params, Result: result, Entry: entry, Blocks: b.blocks, Locals: locals}
	analyze(&w)
	return w, nil
}

func (b *builder) lowerInto(e core.Expr, bind Local, next BlockID) BlockID {
	switch e := e.(type) {
	case *core.Let:
		body := b.lowerInto(e.Body, bind, next)
		name := b.localName(e.Name)
		local := Local{Name: name, Ty: e.Rhs.Type()}
		b.declare(local)
		return b.lowerInto(e.Rhs, local, body)
	case *core.Seq:
		body := b.lowerInto(e.Then, bind, next)
		discard := Local{Name: b.fresh("discard"), Ty: e.First.Type()}
		b.declare(discard)
		return b.lowerInto(e.First, discard, body)
	case *core.If:
		if machineControl(e.Cond) {
			b.errorf("%s: machine-producing If condition was not ANF-hoisted", b.def.Name)
			return next
		}
		thenBlock := b.lowerInto(e.Then, bind, next)
		elseBlock := b.lowerInto(e.Else, bind, next)
		return b.add(&Branch{Cond: e.Cond, Then: thenBlock, Else: elseBlock})
	case *core.Case:
		if machineControl(e.Scrut) {
			b.errorf("%s: machine-producing Case scrutinee was not ANF-hoisted", b.def.Name)
			return next
		}
		scrut := Local{Name: b.localName(e.Bind), Ty: e.Scrut.Type()}
		b.declare(scrut)
		tree := b.lowerTree(e.Tree, bind, next)
		return b.add(&Eval{Bind: scrut, Value: e.Scrut, Next: tree})
	case *core.Suspend:
		if machineControl(e.Request) {
			b.errorf("%s: suspension request itself requires Machine control", b.def.Name)
			return next
		}
		return b.add(&Suspend{Request: e.Request, Bind: bind, Next: next})
	case *core.App:
		if e.Control.Resolve(types.Machine) == types.Machine {
			ref, ok := e.Callee.(*core.VarRef)
			if e.CalleeKind != core.Worker || !ok || !b.selected[ref.Name] {
				b.errorf("%s: unsupported indirect or unresolved Machine call", b.def.Name)
				return next
			}
			for _, arg := range e.Args {
				if machineControl(arg) {
					b.errorf("%s: Machine call argument was not ANF-hoisted", b.def.Name)
					return next
				}
			}
			call := &Call{Callee: ref.Name, Args: e.Args, EvidenceArgs: e.EvidenceArgs, Bind: bind, Next: next}
			call.Tail = b.isReturnOf(next, bind)
			return b.add(call)
		}
	case *core.Bracket:
		if machineControl(e.Acquire) {
			b.errorf("%s: suspending cleanup acquisition is not implemented", b.def.Name)
			return next
		}
		if machineControl(e.Release) {
			b.errorf("%s: cleanup release may not suspend", b.def.Name)
			return next
		}
		resource := Local{Name: e.Resource, Ty: e.ResourceTy}
		b.declare(resource)
		pop := b.add(&PopCleanup{Next: next})
		body := b.lowerInto(e.Body, bind, pop)
		return b.add(&PushCleanup{Acquire: e.Acquire, Resource: resource, Release: e.Release, Next: body})
	}
	if machineControl(e) {
		b.errorf("%s: machine lowering does not yet support %T", b.def.Name, e)
		return next
	}
	return b.add(&Eval{Bind: bind, Value: e, Next: next})
}

func (b *builder) lowerTree(tree core.Tree, bind Local, next BlockID) BlockID {
	switch tree := tree.(type) {
	case *core.Leaf:
		return b.lowerInto(tree.Body, bind, next)
	case *core.Guard:
		if machineControl(tree.Cond) {
			b.errorf("%s: machine-producing decision-tree guard was not ANF-hoisted", b.def.Name)
			return next
		}
		thenBlock := b.lowerTree(tree.Then, bind, next)
		elseBlock := b.lowerTree(tree.Else, bind, next)
		return b.add(&Branch{Cond: tree.Cond, Then: thenBlock, Else: elseBlock})
	case *core.SwitchCtor:
		scrutTy, ok := b.locals[tree.Scrut].(*types.TCon)
		if !ok {
			b.errorf("%s: constructor switch scrutinee %q has no nominal local type", b.def.Name, tree.Scrut)
			return next
		}
		term := &SwitchCtor{Scrut: tree.Scrut, ADT: tree.ADT}
		for _, c := range tree.Cases {
			fields := tree.ADT.InstFields(c.Ctor, scrutTy.Args)
			binds := make([]Local, len(c.Binds))
			for i, name := range c.Binds {
				if name == "" {
					binds[i] = Local{Ty: fields[i]}
					continue
				}
				binds[i] = Local{Name: name, Ty: fields[i]}
				b.declare(binds[i])
			}
			term.Cases = append(term.Cases, CtorCase{Ctor: c.Ctor, Binds: binds, Next: b.lowerTree(c.Tree, bind, next)})
		}
		if tree.Default != nil {
			id := b.lowerTree(tree.Default, bind, next)
			term.Default = &id
		}
		return b.add(term)
	case *core.SwitchLit:
		term := &SwitchLit{Scrut: tree.Scrut, Default: b.lowerTree(tree.Default, bind, next)}
		for _, c := range tree.Cases {
			term.Cases = append(term.Cases, LitCase{Lit: c.Lit, Next: b.lowerTree(c.Tree, bind, next)})
		}
		return b.add(term)
	case *core.Unreachable:
		b.errorf("%s: reachable decision-tree Unreachable cannot enter Machine IR", b.def.Name)
		return next
	default:
		b.errorf("%s: unknown decision tree %T", b.def.Name, tree)
		return next
	}
}

func machineControl(e core.Expr) bool {
	return core.ExprControl(e).Resolve(types.Machine) == types.Machine
}

func (b *builder) add(term Term) BlockID {
	id := BlockID(len(b.blocks))
	b.blocks = append(b.blocks, Block{ID: id, Term: term})
	return id
}

func (b *builder) declare(local Local) {
	if old, ok := b.locals[local.Name]; ok {
		if !types.Equal(old, local.Ty) {
			b.errorf("%s: local %q has inconsistent types %s and %s", b.def.Name, local.Name, types.Show(old), types.Show(local.Ty))
		}
		return
	}
	b.locals[local.Name] = local.Ty
}

func (b *builder) localName(name string) string {
	if name == "_" || name == "()" || name == "" {
		return b.fresh("discard")
	}
	return name
}

func (b *builder) fresh(prefix string) string {
	name := fmt.Sprintf("_machine_%s%d", prefix, b.tmp)
	b.tmp++
	return name
}

func (b *builder) errorf(format string, args ...any) {
	b.errs = append(b.errs, fmt.Errorf(format, args...))
}

func (b *builder) isReturnOf(id BlockID, local Local) bool {
	if int(id) < 0 || int(id) >= len(b.blocks) {
		return false
	}
	ret, ok := b.blocks[id].Term.(*Return)
	if !ok {
		return false
	}
	ref, ok := ret.Value.(*core.VarRef)
	return ok && ref.Local && ref.Name == local.Name
}

func localRef(local Local) core.Expr {
	return &core.VarRef{Name: local.Name, Local: true, Ty: local.Ty}
}
