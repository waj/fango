package core

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/types"
)

// The abstract machine executes capture contracts, never source computations.
// Scalars are erased, branches are joined, and recursive calls use a finite
// allocation-site heap and monotone call summaries. Each acyclic call path has
// its own context; a recursive edge joins the enclosing context for that code.
// Thus wrappers and callbacks remain relational without unrolling recursion.
type flowValue struct {
	refs    []int
	caps    []int
	unknown bool
}

func joinFlow(vs ...flowValue) flowValue {
	var r flowValue
	for _, v := range vs {
		r.refs = append(r.refs, v.refs...)
		r.caps = append(r.caps, v.caps...)
		r.unknown = r.unknown || v.unknown
	}
	slices.Sort(r.refs)
	r.refs = slices.Compact(r.refs)
	slices.Sort(r.caps)
	r.caps = slices.Compact(r.caps)
	return r
}
func equalFlow(a, b flowValue) bool {
	return a.unknown == b.unknown && slices.Equal(a.refs, b.refs) && slices.Equal(a.caps, b.caps)
}

type flowEnv struct {
	values   map[string]flowValue
	evidence map[int][]int
	types    map[int]types.Type
}

func (e flowEnv) clone() flowEnv {
	return flowEnv{maps.Clone(e.values), maps.Clone(e.evidence), maps.Clone(e.types)}
}

type flowObject struct {
	kind   string
	code   *types.CaptureFlow
	def    string
	env    flowEnv
	fields []flowValue
	ctor   int
	owner  int
}
type flowContext struct {
	origin    source.Span
	target    string
	site      string
	parent    string
	env       flowEnv
	result    flowValue
	busy      bool
	recursive bool
	resumes   []int
	scopes    []int
	def       string
	accesses  []int
}
type flowOwner struct {
	origin  source.Span
	scope   types.ScopeID
	parent  []int
	name    string
	in      string
	scoped  bool
	context string
	code    *types.CaptureFlow
	env     flowEnv
	state   flowValue
	answer  flowValue
}

type CaptureFlowError struct {
	Scope                         types.ScopeID
	Span                          source.Span
	In, Owner, Value, Destination string
	State                         bool
}

func (e CaptureFlowError) Error() string { return "RESOURCE ESCAPES: " + e.Detail() }
func (e CaptureFlowError) Detail() string {
	return fmt.Sprintf("`%s` retains the resource owned by %s. %s outlives that resource's scope.", e.Value, e.Owner, e.Destination)
}

// CursorAccessError reports a conflict after substituting actual cursor,
// callback, and evidence identities into a definition's access contract.
type CursorAccessError struct {
	Span      source.Span
	In, Owner string
}

func (e CursorAccessError) Error() string { return "ITERATOR ADVANCEMENT CONFLICT: " + e.Detail() }
func (e CursorAccessError) Detail() string {
	return fmt.Sprintf("The cursor owned by %s may be advanced while an earlier advancement is still executing its producer.", e.Owner)
}

type flowChecker struct {
	shape     *captureAnalyzer
	defs      map[string]*Def
	objects   []*flowObject
	objectIDs map[string]int
	owners    []*flowOwner
	ownerIDs  map[string]int
	contexts  map[string]*flowContext
	changed   bool
	errors    map[string]error
	root      string
	location  source.Span
	active    map[int]int
	calls     []*flowContext
}

func checkCaptureFlows(a *captureAnalyzer) []error {
	var out []error
	names := make([]string, 0, len(a.p.Defs))
	for _, d := range a.p.Defs {
		names = append(names, d.Name)
	}
	// A definition's graph exports deferred obligations on its abstract
	// parameters. Checking all definitions also rejects unconditional escapes in
	// an unused exported function, independently of whole-program reachability.
	for _, name := range names {
		f := &flowChecker{shape: a, defs: a.defs, objects: []*flowObject{nil}, objectIDs: map[string]int{}, owners: []*flowOwner{nil}, ownerIDs: map[string]int{}, contexts: map[string]*flowContext{}, errors: map[string]error{}, root: name, active: map[int]int{}}
		d := a.defs[name]
		args := make([]flowValue, len(d.Params))
		for i := range args {
			args[i].unknown = true
		}
		env := flowEnv{map[string]flowValue{}, map[int][]int{}, map[int]types.Type{}}
		// All stores and results grow monotonically over a finite set of allocation
		// sites, contexts, and owners. There is no iteration cap or success fallback.
		for {
			f.changed = false
			f.callDef(name, args, nil, env, "root", nil)
			if !f.changed {
				break
			}
		}
		keys := make([]string, 0, len(f.errors))
		for k := range f.errors {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			out = append(out, f.errors[k])
		}
	}
	return out
}
func (f *flowChecker) carry(t types.Type, env flowEnv) bool {
	if t == nil {
		return true
	}
	return f.shape.canCarry(types.SubstRigid(t, env.types), nil)
}
func (f *flowChecker) trim(v flowValue, t types.Type, env flowEnv) flowValue {
	if !f.carry(t, env) {
		return flowValue{}
	}
	return v
}
func (f *flowChecker) merge(dst *flowValue, v flowValue) {
	n := joinFlow(*dst, v)
	if !equalFlow(n, *dst) {
		*dst = n
		f.changed = true
	}
}
func (f *flowChecker) mergeEnv(dst *flowEnv, src flowEnv) {
	if dst.values == nil {
		*dst = flowEnv{map[string]flowValue{}, map[int][]int{}, maps.Clone(src.types)}
	}
	// Reentrant higher-order code can instantiate the same worker at different
	// types, even without source polymorphic recursion. A folded context must
	// not keep a scalar instantiation that would erase a later resource. Forget
	// disagreeing substitutions; the original rigid variable is capture-capable.
	for variable, old := range dst.types {
		if next, ok := src.types[variable]; !ok || !types.Equal(old, next) {
			delete(dst.types, variable)
			f.changed = true
		}
	}
	for k, v := range src.values {
		old := dst.values[k]
		f.merge(&old, v)
		dst.values[k] = old
	}
	for k, ids := range src.evidence {
		joined := append(slices.Clone(dst.evidence[k]), ids...)
		slices.Sort(joined)
		joined = slices.Compact(joined)
		if !slices.Equal(joined, dst.evidence[k]) {
			dst.evidence[k] = joined
			f.changed = true
		}
	}
}
func (f *flowChecker) alloc(key string, o flowObject) int {
	if id := f.objectIDs[key]; id != 0 {
		old := f.objects[id]
		f.mergeEnv(&old.env, o.env)
		for len(old.fields) < len(o.fields) {
			old.fields = append(old.fields, flowValue{})
		}
		for i, v := range o.fields {
			f.merge(&old.fields[i], v)
		}
		return id
	}
	id := len(f.objects)
	f.objectIDs[key] = id
	f.objects = append(f.objects, &o)
	f.changed = true
	return id
}
func (f *flowChecker) contextDef(ctx string) string {
	if c := f.contexts[ctx]; c != nil {
		return c.def
	}
	return f.root
}
func (f *flowChecker) owner(n *types.CaptureFlow, env flowEnv, ctx string, scopes []int) int {
	key := fmt.Sprintf("%s/s%d", ctx, n.ID)
	if id := f.ownerIDs[key]; id != 0 {
		f.mergeEnv(&f.owners[id].env, env)
		return id
	}
	name := "handler"
	if n.Kind == "scope" {
		name = "cleanup scope"
		if len(n.TypeArgs) > 0 {
			name += " for `" + types.Show(types.SubstRigid(n.TypeArgs[0], env.types)) + "`"
		}
	} else if n.Kind == "iterator" {
		name = "cursor scope"
	}
	id := len(f.owners)
	f.ownerIDs[key] = id
	origin := source.Span{}
	// Point at the caller supplying the resource callback, rather than an
	// implementation detail in a bundled wrapper.
	for context := ctx; context != ""; {
		c := f.contexts[context]
		if c == nil {
			break
		}
		if c.origin.File != nil {
			origin = c.origin
		}
		context = c.parent
	}
	f.owners = append(f.owners, &flowOwner{origin: origin, scope: n.Scope, parent: slices.Clone(scopes), name: name + " in `" + f.contextDef(ctx) + "`", in: f.root, scoped: n.Scoped || n.Kind == "scope", context: ctx, code: n, env: env.clone()})
	f.changed = true
	return id
}
func (f *flowChecker) captures(v flowValue) []int {
	caps := slices.Clone(v.caps)
	seen := map[int]bool{}
	owners := map[int]bool{}
	var visit func(flowValue)
	var evidence func(int)
	evidence = func(id int) {
		if id == 0 || owners[id] {
			return
		}
		owners[id] = true
		o := f.owners[id]
		if o.scoped {
			caps = append(caps, id)
		}
		visit(o.state)
		// Durable evidence can itself capture a borrowed value in its clauses.
		for _, cl := range o.code.Clauses {
			free := flowFree(cl.Body, cl.Names)
			for name := range free {
				visit(o.env.values[name])
			}
			for ev := range flowEffects(cl.Body) {
				for _, outer := range o.env.evidence[ev] {
					evidence(outer)
				}
			}
		}
	}
	visit = func(v flowValue) {
		caps = append(caps, v.caps...)
		for _, id := range v.refs {
			if seen[id] {
				continue
			}
			seen[id] = true
			o := f.objects[id]
			for _, v := range o.fields {
				visit(v)
			}
			if o.kind == "lambda" {
				free := flowFree(o.code.Children[0], []string{o.code.Name})
				for name := range free {
					visit(o.env.values[name])
				}
				evs := flowEffects(o.code.Children[0])
				for _, id := range o.code.Effects {
					delete(evs, id)
				}
				for ev := range evs {
					for _, id := range o.env.evidence[ev] {
						evidence(id)
					}
				}
			}
		}
	}
	visit(v)
	slices.Sort(caps)
	return slices.Compact(caps)
}
func flowFree(n *types.CaptureFlow, bound []string) map[string]bool {
	out := map[string]bool{}
	scope := map[string]bool{}
	for _, s := range bound {
		scope[s] = true
	}
	var walk func(*types.CaptureFlow, map[string]bool)
	walk = func(n *types.CaptureFlow, b map[string]bool) {
		if n == nil {
			return
		}
		if (n.Kind == "var" || n.Kind == "global") && !b[n.Name] {
			out[n.Name] = true
		}
		inner := maps.Clone(b)
		switch n.Kind {
		case "lambda", "let", "scope", "case":
			inner[n.Name] = true
		}
		for i, c := range n.Children {
			cb := b
			switch n.Kind {
			case "lambda":
				cb = inner
			case "let":
				if i == 1 || n.Rec {
					cb = inner
				}
			case "scope", "case":
				if i > 0 {
					cb = inner
				}
			case "handle":
				if i == 2 {
					cb = maps.Clone(b)
					cb[n.Name] = true
					for _, s := range n.Names {
						cb[s] = true
					}
				}
			}
			walk(c, cb)
		}
		for _, cl := range n.Clauses {
			cb := maps.Clone(b)
			cb[n.Name] = true
			for _, s := range cl.Names {
				cb[s] = true
			}
			walk(cl.Body, cb)
		}
	}
	walk(n, scope)
	return out
}
func flowEffects(n *types.CaptureFlow) map[int]bool {
	out := map[int]bool{}
	var walk func(*types.CaptureFlow, map[int]bool)
	walk = func(n *types.CaptureFlow, bound map[int]bool) {
		if n == nil {
			return
		}
		if n.Kind == "lambda" || n.Kind == "handle" {
			inner := maps.Clone(bound)
			for _, ev := range n.Effects {
				inner[ev] = true
			}
			if n.Kind == "lambda" {
				walk(n.Children[0], inner)
			} else {
				walk(n.Children[0], bound) // initial state precedes installation
				walk(n.Children[1], inner)
				walk(n.Children[2], bound) // return and clauses use outer evidence
				for _, cl := range n.Clauses {
					walk(cl.Body, bound)
				}
			}
			return
		}
		for _, ev := range n.Effects {
			if !bound[ev] {
				out[ev] = true
			}
		}
		for _, c := range n.Children {
			walk(c, bound)
		}
		for _, cl := range n.Clauses {
			walk(cl.Body, bound)
		}
	}
	walk(n, map[int]bool{})
	return out
}
func (f *flowChecker) escape(v flowValue, owner int, value, dest string) {
	if !slices.Contains(f.captures(v), owner) {
		return
	}
	o := f.owners[owner]
	err := CaptureFlowError{Scope: o.scope, Span: o.origin, In: f.root, Owner: o.name, Value: value, Destination: dest, State: o.code.Kind == "handle" && o.code.Name != ""}
	f.errors[err.Error()] = err
}
func (f *flowChecker) store(v flowValue, dest int, value string) {
	if dest == 0 {
		return
	} // abstract evidence: the contract exports this obligation
	d := f.owners[dest]
	for _, id := range f.captures(v) {
		// A resource may be kept by an owner nested inside its lifetime. Folded
		// recursive activations cannot establish equality of dynamic owners.
		inside := slices.Contains(d.parent, id)
		same := id == dest
		if f.recursiveOwner(id) {
			// A folded resource site represents more than one activation.
			// Neither equal IDs nor a folded ancestry edge proves outliving.
			inside, same = false, false
		}
		if !inside && !same {
			f.escape(v, id, value, "handler storage in "+d.name)
		}
	}
}
func (f *flowChecker) recursiveOwner(id int) bool {
	for ctx := f.owners[id].context; ctx != ""; {
		c := f.contexts[ctx]
		if c == nil {
			break
		}
		if c.recursive {
			return true
		}
		ctx = c.parent
	}
	return false
}

func (f *flowChecker) callDef(name string, args []flowValue, typeArgs []types.Type, caller flowEnv, site string, scopes []int) flowValue {
	d := f.defs[name]
	if d == nil {
		return flowValue{unknown: true}
	}
	contract := d.CaptureContract
	if contract == nil {
		contract = inferCaptureContract(d)
	}
	env := flowEnv{map[string]flowValue{}, map[int][]int{}, map[int]types.Type{}}
	for i, p := range contract.Params {
		if i < len(args) {
			env.values[p] = args[i]
		}
	}
	for _, ev := range contract.Effects {
		env.evidence[ev] = caller.evidence[ev]
	}
	for i, tv := range contract.TypeParams {
		if i < len(typeArgs) {
			env.types[tv] = types.SubstRigid(typeArgs[i], caller.types)
		}
	}
	return f.invoke("def:"+name, name, contract.Body, env, site, scopes, 0)
}
func (f *flowChecker) invoke(target, def string, body *types.CaptureFlow, env flowEnv, site string, scopes []int, resume int) flowValue {
	parent := site
	for f.contexts[parent] == nil {
		i := strings.LastIndex(parent, "/")
		if i < 0 {
			parent = ""
			break
		}
		parent = parent[:i]
	}
	key := site + ">" + target
	// Fold at a repeated code identity AND lexical call site. Distinct nested
	// uses of the same scope wrapper introduce independent owners; invoking an
	// intrinsic twice is not, by itself, recursive source computation.
	callSite := f.contextDef(parent) + strings.TrimPrefix(site, parent)
	for p := parent; p != ""; {
		c := f.contexts[p]
		if c == nil {
			break
		}
		if c.target == target && c.site == callSite {
			key = p
			if !c.recursive {
				c.recursive = true
				f.changed = true
			}
			break
		}
		p = c.parent
	}
	c := f.contexts[key]
	if c == nil {
		c = &flowContext{origin: f.location, target: target, site: callSite, parent: parent, def: def, scopes: slices.Clone(scopes)}
		f.contexts[key] = c
		f.changed = true
	}
	f.mergeEnv(&c.env, env)
	if !slices.Contains(c.resumes, resume) {
		c.resumes = append(c.resumes, resume)
		f.changed = true
	}
	// A recursive edge may reuse a busy summary. Its already-discovered access
	// obligations still apply; skipping them would accept reentrant helpers.
	for _, owner := range c.accesses {
		f.requireAdvance(owner)
	}
	if c.busy {
		return c.result
	}
	c.busy = true
	f.calls = append(f.calls, c)
	var got flowValue
	for _, owner := range c.resumes {
		got = joinFlow(got, f.eval(body, c.env, key, c.scopes, owner))
	}
	c.busy = false
	f.calls = f.calls[:len(f.calls)-1]
	f.merge(&c.result, got)
	return c.result
}
func (f *flowChecker) apply(fn flowValue, args []flowValue, env flowEnv, site string, scopes []int) flowValue {
	var result flowValue
	if fn.unknown {
		result.unknown = true
	}
	for _, id := range fn.refs {
		o := f.objects[id]
		switch o.kind {
		case "global":
			result = joinFlow(result, f.callDef(o.def, args, o.code.TypeArgs, env, site, scopes))
		case "lambda":
			if len(args) == 0 {
				continue
			}
			inner := o.env.clone()
			inner.values[o.code.Name] = args[0]
			for _, ev := range o.code.Effects {
				inner.evidence[ev] = env.evidence[ev]
			}
			got := f.invoke(fmt.Sprintf("lambda:%s:%d", o.def, o.code.ID), o.def, o.code.Children[0], inner, site, scopes, 0)
			if len(args) > 1 {
				got = f.apply(got, args[1:], env, site+"/apply", scopes)
			}
			result = joinFlow(result, got)
		}
	}
	return result
}
func (f *flowChecker) eval(n *types.CaptureFlow, env flowEnv, ctx string, scopes []int, resume int) flowValue {
	if n == nil {
		return flowValue{}
	}
	oldLocation := f.location
	if n.Origin.File != nil {
		f.location = n.Origin
	}
	defer func() { f.location = oldLocation }()
	key := fmt.Sprintf("%s/%d", ctx, n.ID)
	child := func(i int) flowValue {
		if i >= len(n.Children) {
			return flowValue{}
		}
		return f.eval(n.Children[i], env, ctx, scopes, resume)
	}
	all := func(start int) []flowValue {
		var vs []flowValue
		for i := start; i < len(n.Children); i++ {
			vs = append(vs, child(i))
		}
		return vs
	}
	var result flowValue
	switch n.Kind {
	case "var":
		result = env.values[n.Name]
	case "global":
		if local, ok := env.values[n.Name]; ok {
			return f.trim(local, n.Type, env)
		}
		if d := f.defs[n.Name]; d != nil && !d.IsWorker() {
			result = f.callDef(n.Name, nil, n.TypeArgs, env, key, scopes)
		} else {
			id := f.alloc(key, flowObject{kind: "global", def: n.Name, code: n, env: env.clone()})
			result.refs = []int{id}
		}
	case "native":
		result = joinFlow(all(0)...)
	case "scalar":
		all(0)
	case "branch":
		child(0)
		result = joinFlow(child(1), child(2))
	case "choice":
		result = joinFlow(all(0)...)
	case "seq":
		child(0)
		result = child(1)
	case "let":
		inner := env.clone()
		v := child(0)
		inner.values[n.Name] = v
		if n.Rec {
			for _, id := range v.refs {
				o := f.objects[id]
				if o.kind == "lambda" {
					f.mergeEnv(&o.env, inner)
				}
			}
		}
		result = f.eval(n.Children[1], inner, ctx, scopes, resume)
	case "lambda":
		id := f.alloc(key, flowObject{kind: "lambda", code: n, def: f.contextDef(ctx), env: env.clone()})
		result.refs = []int{id}
	case "ctor":
		id := f.alloc(key, flowObject{kind: "ctor", ctor: n.Index, fields: all(0)})
		result.refs = []int{id}
	case "call":
		fn := child(0)
		args := all(1)
		_, local := env.values[n.Children[0].Name]
		if n.Children[0].Kind == "global" && !local {
			result = f.callDef(n.Children[0].Name, args, n.TypeArgs, env, key, scopes)
		} else {
			result = f.apply(fn, args, env, key, scopes)
		}
	case "case":
		v := child(0)
		inner := env.clone()
		inner.values[n.Name] = v
		result = f.eval(n.Children[1], inner, ctx, scopes, resume)
	case "switch":
		v := env.values[n.Name]
		for _, cl := range n.Clauses {
			inner := env.clone()
			possible := v.unknown || len(v.refs) == 0
			for _, id := range v.refs {
				o := f.objects[id]
				if o.kind == "ctor" && o.ctor == cl.Index {
					possible = true
					for i, name := range cl.Names {
						if i < len(o.fields) {
							inner.values[name] = joinFlow(inner.values[name], o.fields[i])
						}
					}
				}
			}
			if !possible {
				continue
			}
			for i, name := range cl.Names {
				value := inner.values[name]
				value.caps = append(value.caps, v.caps...)
				value.unknown = value.unknown || v.unknown
				if i < len(cl.Types) {
					value = f.trim(value, cl.Types[i], env)
				}
				inner.values[name] = value
			}
			result = joinFlow(result, f.eval(cl.Body, inner, ctx, scopes, resume))
		}
		result = joinFlow(result, child(0))
	case "scope":
		acquired := child(0)
		id := f.owner(n, env, ctx, scopes)
		resource := acquired
		if len(n.TypeArgs) > 0 && f.carry(n.TypeArgs[0], env) {
			resource.caps = append(resource.caps, id)
		}
		inner := env.clone()
		inner.values[n.Name] = resource
		inside := append(slices.Clone(scopes), id)
		result = f.eval(n.Children[1], inner, ctx, inside, resume)
		f.eval(n.Children[2], inner, ctx, inside, resume)
		f.escape(result, id, "scope result", "The returned value")
	case "handle":
		initial := child(0)
		id := f.owner(n, env, ctx, scopes)
		o := f.owners[id]
		f.store(initial, id, "initial state")
		f.merge(&o.state, initial)
		inner := env.clone()
		inner.evidence[n.Effects[0]] = []int{id}
		result = f.eval(n.Children[1], inner, ctx, append(slices.Clone(scopes), id), resume)
		if n.Children[2] != nil {
			ret := env.clone()
			ret.values[n.Name] = o.state
			if len(n.Names) > 0 {
				ret.values[n.Names[0]] = result
			}
			result = f.eval(n.Children[2], ret, ctx, scopes, resume)
		}
		result = joinFlow(result, o.answer)
		if n.Scoped {
			f.escape(result, id, "handler result", "The returned value")
		}
	case "perform", "exit":
		args := all(0)
		targets := env.evidence[n.Effects[0]]
		if len(targets) == 0 {
			result.unknown = true
		}
		for _, id := range targets {
			if id == 0 {
				result.unknown = true
				continue
			}
			o := f.owners[id]
			if n.Kind == "exit" {
				for _, v := range args {
					for _, owner := range f.captures(v) {
						if !slices.Contains(o.parent, owner) || f.recursiveOwner(owner) {
							f.escape(v, owner, "abort payload", "the abort destination")
						}
					}
				}
			}
			if n.Retain {
				for _, v := range args {
					f.store(v, id, "operation argument")
				}
			}
			for _, cl := range o.code.Clauses {
				if cl.Index != n.Index {
					continue
				}
				inner := o.env.clone()
				inner.values[o.code.Name] = o.state
				for i, name := range cl.Names {
					if i < len(args) {
						inner.values[name] = args[i]
					}
				}
				// Clause code runs with definition-site evidence. Its synchronous
				// execution still lies inside the caller's live resource scopes.
				got := f.invoke(fmt.Sprintf("clause:%s:%d", f.contextDef(o.context), cl.Body.ID), f.contextDef(o.context), cl.Body, inner, key, scopes, id)
				if n.Kind == "exit" {
					f.merge(&o.answer, got)
				} else {
					result = joinFlow(result, got)
				}
			}
			if n.Borrow {
				result.caps = append(result.caps, id)
			}
		}
	case "resume":
		result = child(0)
		if len(n.Children) > 1 && n.Children[1] != nil && resume != 0 {
			next := child(1)
			f.store(next, resume, "next state")
			f.merge(&f.owners[resume].state, next)
		}
		// Resume's apparent Core result is the handler answer, while the value
		// flowing back to the perform site is the operation result.
		return result
	case "suspend":
		result = child(0)
		// Existing iterator ownership remains independently checked. A yielded
		// value must not smuggle an inner resource outside its producer lifetime.
		for _, id := range f.captures(result) {
			f.escape(result, id, "yielded value", "the iterator consumer")
		}
		result = flowValue{}
	case "iterator":
		producer, consumer := child(0), child(1)
		owner := f.owner(n, env, ctx, scopes)
		cursor := f.alloc(key+"/cursor", flowObject{kind: "cursor", owner: owner, fields: []flowValue{producer}})
		inside := append(slices.Clone(scopes), owner)
		result = f.apply(consumer, []flowValue{{refs: []int{cursor}, caps: []int{owner}}}, env, key+"/consumer", inside)
		f.escape(result, owner, "cursor scope result", "The returned value")
	case "foreach":
		action := child(0)
		f.advance(child(1), env, key, scopes)
		f.apply(action, []flowValue{{unknown: true}}, env, key, scopes)
	case "fold":
		combine, initial := child(0), child(1)
		f.advance(child(2), env, key, scopes)
		// A later iteration can invoke or retain a callback stored by an earlier
		// one. Feed the growing accumulator back through the callback contract,
		// just as ordinary recursive Fango folds do through call summaries.
		state := f.alloc(key+"/accumulator", flowObject{kind: "fold-state", fields: []flowValue{initial}})
		accumulator := f.objects[state].fields[0]
		next := f.apply(combine, []flowValue{{unknown: true}, accumulator}, env, key, scopes)
		f.merge(&f.objects[state].fields[0], next)
		result = f.objects[state].fields[0]
	default:
		panic("unknown capture flow: " + n.Kind)
	}
	return f.trim(result, n.Type, env)
}

func (f *flowChecker) requireAdvance(owner int) {
	for _, call := range f.calls {
		if !slices.Contains(call.accesses, owner) {
			call.accesses = append(call.accesses, owner)
			f.changed = true
		}
	}
	if f.active[owner] == 0 {
		return
	}
	o := f.owners[owner]
	span := f.location
	if span.File == nil {
		span = o.origin
	}
	err := CursorAccessError{Span: span, In: f.root, Owner: o.name}
	f.errors[err.Error()] = err
}

// advance models the entire producer execution under an exclusive borrow.
// Yield does not release that borrow within the contract: any call reached
// before the producer returns to its caller is checked against the same owner.
// Consumer callbacks run after this method returns, with the borrow discharged.
func (f *flowChecker) advance(cursor flowValue, env flowEnv, site string, scopes []int) {
	for _, ref := range cursor.refs {
		o := f.objects[ref]
		if o.kind != "cursor" {
			continue
		}
		owner := o.owner
		f.requireAdvance(owner)
		if f.active[owner] != 0 {
			continue
		}
		f.active[owner]++
		f.apply(o.fields[0], []flowValue{{}}, env, site+"/advance", scopes)
		f.active[owner]--
	}
}
