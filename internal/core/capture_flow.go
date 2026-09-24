package core

import (
	"fmt"
	"maps"
	"slices"

	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/types"
)

// The abstract machine executes capture contracts, never source computations.
// Scalars are erased, branches are joined, and recursive calls use a finite
// allocation-site heap and monotone call summaries. Equivalent capability
// states share contexts; recursive growth joins an enclosing activation.
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
	controlRow types.Type
	invocation types.Type
	values     map[string]flowValue
	evidence   map[int][]int
	types      map[int]types.Type
	rows       map[types.CaptureVar]flowRow
}

func (e flowEnv) clone() flowEnv {
	return flowEnv{controlRow: e.controlRow, invocation: e.invocation, values: maps.Clone(e.values), evidence: maps.Clone(e.evidence), types: maps.Clone(e.types), rows: maps.Clone(e.rows)}
}

type flowObject struct {
	budget     types.Type
	allocation string
	ancestry   []string
	kind       string
	code       *types.CaptureFlow
	def        string
	env        flowEnv
	fields     []flowValue
	ctor       int
	owner      int
}

// A call site is lexical code plus an application phase, never a call path.
// The context component is used only for allocation and stable dataflow edges.
type flowSite struct {
	context string
	node    int
	phase   string
}

func (s flowSite) within(phase string) flowSite {
	s.phase += "/" + phase
	return s
}

func (s flowSite) allocation() string {
	return fmt.Sprintf("%s/%d%s", s.context, s.node, s.phase)
}

type flowLexicalSite struct {
	def   string
	node  int
	phase string
}

type flowEdge struct {
	parent                   string
	site                     flowSite
	target, boundary, scopes string
	resume                   int
}

var rootFlowSite = flowSite{phase: "root"}

type flowContext struct {
	sourceRows         []types.Type
	control            types.Control
	completionFailures flowValue
	// entry is the invocation state used for sharing; env is its monotone
	// recursive widening. Both refer to the live heap. Stable incoming edges
	// keep supplying growth even after widening changes the sharing key.
	entry       flowEnv
	ancestry    []string
	id          string
	boundary    string
	evaluated   int
	evaluations map[int]int
	fingerprint string
	revision    int
	origin      source.Span
	target      string
	site        flowLexicalSite
	parent      string
	env         flowEnv
	result      flowValue
	busy        bool
	recursive   bool
	resumes     []int
	scopes      []int
	def         string
	accesses    []int
	drives      []int
	suspensions []flowSuspension
}
type flowOwner struct {
	registry int
	failures flowValue
	origin   source.Span
	scope    types.ScopeID
	parent   []int
	name     string
	in       string
	scoped   bool
	context  string
	ancestry []string
	code     *types.CaptureFlow
	env      flowEnv
	state    flowValue
	answer   flowValue
	yielded  flowValue
	reply    flowValue
	finished flowValue
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

// SuspensionError reports a producer suspension reached through an actual
// acquisition or release callback, after substituting its exported contract.
type SuspensionError struct {
	Span      source.Span
	In, Phase string
}

func (e SuspensionError) Error() string { return "SUSPENDING RESOURCE CALLBACK: " + e.Detail() }
func (e SuspensionError) Detail() string {
	return fmt.Sprintf("Resource %s must complete synchronously, but this callback may suspend.", e.Phase)
}

type synchronousFlow struct {
	phase     string
	span      source.Span
	enclosing []int
}

type flowPull struct {
	owner       int
	synchronous []synchronousFlow
	calls       []*flowContext
}

type flowChecker struct {
	collectWorkNeed    func(WorkNeed)
	collectControlNeed func(ControlNeed)
	detached           []*detachedFlow
	shape              *captureAnalyzer
	defs               map[string]*Def
	objects            []*flowObject
	objectIDs          map[string]int
	owners             []*flowOwner
	ownerIDs           map[string]int
	contexts           map[string]*flowContext
	key                flowKey
	byTarget           map[string][]*flowContext
	edges              map[flowEdge]string
	revision           int
	// heapRevision counts only the mutations a sharing key can observe: an
	// object's fields and a closure object's environment. Every other kind of
	// growth leaves every key intact.
	heapRevision    int
	generation      int
	changed         bool
	errors          map[string]error
	root            string
	location        source.Span
	active          map[int]int
	calls           []*flowContext
	synchronous     []synchronousFlow
	suspensionCalls []*flowContext
	effectHandlers  []int
	pulls           []flowPull
}

type detachedFlow struct {
	site      flowSite
	callDepth int
	outer     []int
	failures  flowValue
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
		env := emptyFlowEnv()
		env.rows[0] = flowRow{unknown: true}
		// All stores and results grow monotonically over a finite set of allocation
		// sites, contexts, and owners. There is no iteration cap or success fallback.
		for {
			f.generation++
			f.changed = false
			f.callDef(name, args, nil, env, rootFlowSite, nil)
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
		f.grow()
	}
}

// mergeObjectEnv merges into a closure object's environment, which sharing
// keys read, so a change here is a heap revision. Context and owner
// environments go through mergeEnv and leave every key intact.
func (f *flowChecker) mergeObjectEnv(dst *flowEnv, src flowEnv) {
	before := f.revision
	f.mergeEnv(dst, src)
	if f.revision != before {
		f.heapRevision++
	}
}

func (f *flowChecker) mergeEnv(dst *flowEnv, src flowEnv) {
	if dst.values == nil {
		*dst = emptyFlowEnv()
		dst.types = maps.Clone(src.types)
	}
	// Reentrant higher-order code can instantiate the same worker at different
	// types, even without source polymorphic recursion. A folded context must
	// not keep a scalar instantiation that would erase a later resource. Forget
	// disagreeing substitutions; the original rigid variable is capture-capable.
	for variable, old := range dst.types {
		if next, ok := src.types[variable]; !ok || !types.Equal(old, next) {
			delete(dst.types, variable)
			f.grow()
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
			f.grow()
		}
	}
	for k, row := range src.rows {
		joined := joinFlowRow(dst.rows[k], row)
		if !equalFlowRow(joined, dst.rows[k]) {
			dst.rows[k] = joined
			f.grow()
		}
	}
}
func (f *flowChecker) alloc(key string, o flowObject) int {
	if id := f.objectIDs[key]; id != 0 {
		old := f.objects[id]
		if (old.kind == "work-owner" || old.kind == "coroutine-registry") && old.budget != nil && !types.Equal(old.budget, o.budget) {
			old.budget = nil
			f.grow()
		}
		f.mergeAncestry(&old.ancestry)
		f.mergeObjectEnv(&old.env, o.env)
		// A sharing key reads an object's fields, so their growth is a heap
		// revision. The generic merge is not, because its other destinations
		// are call results and owner state, which no key reads.
		before := f.revision
		for len(old.fields) < len(o.fields) {
			old.fields = append(old.fields, flowValue{})
			f.grow()
		}
		for i, v := range o.fields {
			f.merge(&old.fields[i], v)
		}
		if f.revision != before {
			f.heapRevision++
		}
		return id
	}
	id := len(f.objects)
	o.allocation = key
	for _, c := range f.calls {
		o.ancestry = append(o.ancestry, c.id)
	}
	f.objectIDs[key] = id
	f.objects = append(f.objects, &o)
	f.grow()
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
		f.mergeAncestry(&f.owners[id].ancestry)
		f.mergeEnv(&f.owners[id].env, env)
		return id
	}
	name := "handler"
	if n.Kind == "scope" {
		name = "cleanup scope"
		if len(n.TypeArgs) > 0 {
			name += " for `" + types.Show(types.SubstRigid(n.TypeArgs[0], env.types)) + "`"
		}
	} else if n.Kind == "coroutine" {
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
	f.mergeAncestry(&f.owners[id].ancestry)
	f.grow()
	return id
}
func (f *flowChecker) captures(v flowValue) []int {
	caps := slices.Clone(v.caps)
	seen := map[int]bool{}
	owners := map[int]bool{}
	var visit func(flowValue)
	var evidence func(int)
	visitRows := func(n *types.CaptureFlow, env flowEnv, own types.CaptureVar) {
		for row := range f.shape.rows(n) {
			if row == own {
				continue
			}
			for _, owners := range env.rows[row].evidence {
				for _, owner := range owners {
					evidence(owner)
				}
			}
		}
	}
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
			visitRows(cl.Body, o.env, 0)
			free := f.shape.free(cl.Body, cl.Names)
			for name := range free {
				visit(o.env.values[name])
			}
			for ev := range f.shape.effects(cl.Body) {
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
				visitRows(o.code.Children[0], o.env, o.code.RowParam)
				free := f.shape.free(o.code.Children[0], []string{o.code.Name})
				for name := range free {
					visit(o.env.values[name])
				}
				// The cached set is shared, so an owner's own effects are
				// skipped rather than deleted from it.
				for ev := range f.shape.effects(o.code.Children[0]) {
					if slices.Contains(o.code.Effects, ev) || slices.Contains(o.code.Deferred, ev) {
						continue
					}
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
		if n.Row != nil {
			for _, ev := range n.Row.Effects {
				if !bound[ev] {
					out[ev] = true
				}
			}
		}
		if n.Kind == "coroutine" {
			// Constructing either callback still uses outer evidence; pause
			// authority is supplied separately when production starts.
			for _, child := range n.Children {
				walk(child, bound)
			}
			return
		}
		if n.Kind == "lambda" || n.Kind == "handle" {
			inner := maps.Clone(bound)
			for _, ev := range n.Effects {
				inner[ev] = true
			}
			for _, ev := range n.Deferred {
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
	for _, ctx := range f.owners[id].ancestry {
		if f.contextAncestry(ctx, func(c *flowContext) bool { return c.recursive }) {
			return true
		}
	}
	return false
}

func (f *flowChecker) callDef(name string, args []flowValue, typeArgs []types.Type, caller flowEnv, site flowSite, scopes []int) flowValue {
	d := f.defs[name]
	if d == nil {
		return flowValue{unknown: true}
	}
	contract := d.CaptureContract
	if contract == nil {
		contract = inferCaptureContract(d)
	}
	env := emptyFlowEnv()
	f.bindFlowRow(&env, contract.RowParam, contract.RowEffects, caller.rows[0])
	matchSourceRows(contract.SourceType, caller.invocation, env.types)

	env.controlRow = executionRow(types.SubstRigid(contract.SourceType, env.types), len(contract.Params))
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
	return f.invoke("def:"+name, name, contract.Body, env, site, scopes, 0, d.Control)
}

// Distinct pre-existing callbacks and descriptions distinguish nested uses of
// the same helper. Values allocated within the repeated activation may grow
// recursively, so those are widened into its finite summary instead.
func (f *flowChecker) recursiveInputs(context string, previous, next flowEnv) bool {
	allocatedWithin := func(ref int) bool {
		for _, ctx := range f.objects[ref].ancestry {
			if ctx == context || f.contextAncestry(ctx, func(c *flowContext) bool { return c.id == context }) {
				return true
			}
		}
		return false
	}
	var references func(flowValue, map[int]bool, map[int]bool)
	references = func(value flowValue, seen, frontier map[int]bool) {
		for _, ref := range value.refs {
			if seen[ref] {
				continue
			}
			seen[ref] = true
			if !allocatedWithin(ref) {
				frontier[ref] = true
				continue
			}
			o := f.objects[ref]
			// Locally accumulated data must widen (for example a growing
			// reverse/fold accumulator). Only freshly allocated callable
			// adapters need their pre-existing descriptions distinguished.
			if o.kind != "lambda" {
				continue
			}
			for _, field := range o.fields {
				references(field, seen, frontier)
			}
			for _, captured := range o.env.values {
				references(captured, seen, frontier)
			}
		}
	}
	for name, value := range next.values {
		// A cursor supplied by a nested owner is a distinct resource even when
		// its allocation happened inside this activation. Folding it into the
		// enclosing invocation would mix the two exclusive-access contracts.
		// Other scope owners can widen: recursiveOwner makes uncertain lifetime
		// equality fail closed during escape checking.
		previousCaps := append(f.captures(previous.values[name]), f.retainedExecutions(previous.values[name])...)
		for _, owner := range append(f.captures(value), f.retainedExecutions(value)...) {
			if !slices.Contains(previousCaps, owner) && f.owners[owner].code.Kind == "coroutine" {
				return false
			}
		}
		// Fresh adapters must not hide distinct pre-existing descriptions.
		// Inspect their reachable objects as well as the outer wrapper.
		previousRefs, nextRefs := map[int]bool{}, map[int]bool{}
		references(previous.values[name], map[int]bool{}, previousRefs)
		references(value, map[int]bool{}, nextRefs)
		for ref := range nextRefs {
			if !previousRefs[ref] {
				return false
			}
		}
	}
	return true
}

func (f *flowChecker) invoke(target, def string, body *types.CaptureFlow, env flowEnv, site flowSite, scopes []int, resume int, controls ...types.Control) flowValue {
	var control types.Control
	if len(controls) > 0 {
		control = controls[0]
	}
	parent := ""
	if len(f.calls) > 0 {
		parent = f.calls[len(f.calls)-1].id
	}
	sourceRow := env.controlRow
	env = f.relevantFlowEnv(body, env, nil, nil, 0)
	key := ""
	boundary := f.boundaryKey()
	edge := flowEdge{parent: parent, site: site, target: target, boundary: boundary, scopes: fmt.Sprint(scopes), resume: resume}
	boundEdge := parent != "" || site == rootFlowSite
	if boundEdge {
		// An already-bound invocation is a dataflow edge, not a new lookup.
		// Otherwise a growing recursive argument could allocate a new context
		// every generation and lose the very widening that makes it finite.
		key = f.edges[edge]
	}
	// Fold at a repeated code identity AND lexical call site. Distinct nested
	// uses of the same scope wrapper introduce independent owners; invoking an
	// intrinsic twice is not, by itself, recursive source computation.
	callSite := flowLexicalSite{def: f.contextDef(site.context), node: site.node, phase: site.phase}
	for i := len(f.calls) - 1; i >= 0; i-- {
		c := f.calls[i]
		p := c.id
		if c.target == target && c.site == callSite && f.recursiveInputs(p, c.env, env) {
			key = p
			if !c.recursive {
				c.recursive = true
				f.grow()
			}
			break
		}
	}
	if key == "" {
		fingerprint := f.flowFingerprint(env)
		// Fingerprints describe the current heap, not permanent identities.
		// Reindex candidates from their current inputs before every lookup.
		for _, candidate := range f.byTarget[target] {
			if candidate.boundary == boundary && slices.Equal(candidate.scopes, scopes) && slices.Equal(candidate.resumes, []int{resume}) && f.contextFingerprint(candidate) == fingerprint {
				key = candidate.id
				break
			}
		}
	}
	c := f.contexts[key]
	if c == nil {
		key = fmt.Sprintf("c%d", len(f.contexts)+1)
		// revision -1 marks the key as never computed: heap revision 0 is a
		// real state, so a zero here would pass off the empty string as this
		// context's key.
		c = &flowContext{control: control, id: key, revision: -1, entry: env.clone(), boundary: boundary, evaluations: map[int]int{}, origin: f.location, target: target, site: callSite, parent: parent, def: def, scopes: slices.Clone(scopes)}
		f.contexts[key] = c
		if f.byTarget == nil {
			f.byTarget = map[string][]*flowContext{}
		}
		f.byTarget[target] = append(f.byTarget[target], c)
		f.grow()
	}
	if boundEdge {
		if f.edges == nil {
			f.edges = map[flowEdge]string{}
		}
		f.edges[edge] = key
	}
	if sourceRow != nil {
		found := false
		for _, row := range c.sourceRows {
			if types.Equal(row, sourceRow) {
				found = true
				break
			}
		}
		if !found {
			c.sourceRows = append(c.sourceRows, sourceRow)
			f.grow()
		}
	}
	f.mergeEnv(&c.env, env)
	f.mergeAncestry(&c.ancestry)
	if c.busy && !c.recursive {
		c.recursive = true
		f.grow()
	}
	if !slices.Contains(c.resumes, resume) {
		c.resumes = append(c.resumes, resume)
		f.grow()
	}
	// A recursive edge may reuse a busy summary. Its already-discovered access
	// obligations still apply; skipping them would accept reentrant helpers.
	for _, owner := range c.accesses {
		f.checkAdvance(owner)
	}
	for _, owner := range c.drives {
		f.requireDrive(owner)
	}
	for _, suspension := range c.suspensions {
		f.suspendThrough(suspension.owner, suspension.effects)
	}
	if c.busy || c.evaluated == f.generation {
		if len(f.detached) > 0 {
			boundary := f.detached[len(f.detached)-1]
			boundary.failures = joinFlow(boundary.failures, c.completionFailures)
		}
		return c.result
	}
	c.evaluated = f.generation
	c.evaluations[f.generation]++
	c.busy = true
	f.calls = append(f.calls, c)
	f.suspensionCalls = append(f.suspensionCalls, c)
	got := f.eval(body, c.env, key, c.scopes, c.resumes)
	c.busy = false
	f.calls = f.calls[:len(f.calls)-1]
	f.suspensionCalls = f.suspensionCalls[:len(f.suspensionCalls)-1]
	f.merge(&c.result, got)
	return c.result
}
func (f *flowChecker) apply(fn flowValue, args []flowValue, env flowEnv, site flowSite, scopes []int) flowValue {
	var result flowValue
	if fn.unknown {
		result.unknown = true
	}
	for _, id := range fn.refs {
		o := f.objects[id]
		switch o.kind {
		case "abstract-control":
			fn := o.code.Type.(*types.TFun)
			for _, l := range fn.Eff.Labels {
				if l.Name == types.CoroutineSuspensionName {
					f.suspend(0)
				}
				if l.Name == types.CoroutineDriveName {
					if len(args) > 0 && f.abstractDrive(args[0], env, site.within("abstract-drive"), scopes, map[int]bool{}) {
						continue
					}
					for _, call := range f.calls {
						f.requireControl(call, 0, "drive")
					}
				}
			}
			result = joinFlow(result, flowValue{unknown: true})
		case "pause":
			if len(args) != 1 {
				panic("malformed pause capture call")
			}
			if f.active[o.owner] == 0 {
				err := fmt.Errorf("COROUTINE PRODUCER AUTHORITY: pause owned by %s is invoked outside its active producer", f.owners[o.owner].name)
				f.errors[err.Error()] = err
			}
			f.suspend(o.owner)
			f.crossing(args[0], o.owner, "coroutine request")
			f.merge(&f.owners[o.owner].yielded, args[0])
			result = joinFlow(result, f.owners[o.owner].reply)
		case "global":
			result = joinFlow(result, f.callDef(o.def, args, o.code.TypeArgs, env, site, scopes))
		case "lambda":
			if len(args) == 0 {
				continue
			}
			inner := o.env.clone()
			matchSourceRows(o.code.SourceType, env.invocation, inner.types)
			f.bindFlowRow(&inner, o.code.RowParam, o.code.Deferred, env.rows[0])
			inner.values[o.code.Name] = args[0]
			inner.controlRow = executionRow(types.SubstRigid(o.code.SourceType, inner.types), 1)
			for _, ev := range o.code.Effects {
				inner.evidence[ev] = env.evidence[ev]
			}
			control := types.Control{Polymorphic: true}
			if fn, ok := o.code.Type.(*types.TFun); ok {
				control = types.FunctionControl(fn)
			}
			got := f.invoke(fmt.Sprintf("lambda:%s:%d", o.def, o.code.ID), o.def, o.code.Children[0], inner, site, scopes, 0, control)
			if len(args) > 1 {
				got = f.apply(got, args[1:], env, site.within("apply"), scopes)
			}
			result = joinFlow(result, got)
		}
	}
	return result
}

// An abstract driver may receive its handle through an ordinary data wrapper.
// Follow data fields only: a closure's captures are not necessarily handles
// that the callback has authority to advance.
func (f *flowChecker) abstractDrive(value flowValue, env flowEnv, site flowSite, scopes []int, seen map[int]bool) bool {
	found, complete := false, true
	var visit func(flowValue)
	visit = func(value flowValue) {
		complete = complete && !value.unknown
		for _, ref := range value.refs {
			if seen[ref] {
				continue
			}
			seen[ref] = true
			o := f.objects[ref]
			switch o.kind {
			case "coroutine":
				f.advance(flowValue{refs: []int{ref}}, flowValue{unknown: true}, env, site, scopes)
				found = true
			case "ctor":
				for _, field := range o.fields {
					visit(field)
				}
			default:
				complete = false
			}
		}
	}
	visit(value)
	return found && complete
}
func (f *flowChecker) eval(n *types.CaptureFlow, env flowEnv, ctx string, scopes []int, resumes []int) flowValue {
	if n == nil {
		return flowValue{}
	}
	oldLocation := f.location
	if n.Origin.File != nil {
		f.location = n.Origin
	}
	defer func() { f.location = oldLocation }()
	key := flowSite{context: ctx, node: n.ID}
	child := func(i int) flowValue {
		if i >= len(n.Children) {
			return flowValue{}
		}
		return f.eval(n.Children[i], env, ctx, scopes, resumes)
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
	case "work-registration", "work-registry-owner":
		result = child(0)
	case "work-create", "work-register":
		destination, producer := child(0), child(1)
		need := f.registeredBudget(producer, types.SubstRigid(n.SourceType, env.types))
		if n.Kind == "work-register" && f.collectWorkNeed != nil {
			f.collectWorkNeed(WorkNeed{Need: types.SubstRigid(n.SourceType, env.types), Immediate: true, Span: f.location, In: f.root})
		}
		for _, ref := range destination.refs {
			scope := f.objects[ref]
			if scope.kind != "coroutine-registry" {
				continue
			}
			f.store(producer, scope.owner, "coroutine producer")
			f.checkWorkBudget(destination, need)
			parents := append(slices.Clone(f.owners[scope.owner].parent), scope.owner)
			code := *n
			code.Kind = "coroutine"
			code.Scoped = true
			// One allocation site can select among destination scopes. Keep
			// its execution identities distinct even when the call is shared.
			owner := f.owner(&code, env, fmt.Sprintf("%s/registry%d", ctx, ref), parents)
			f.owners[owner].context = ctx
			f.owners[owner].registry = scope.owner
			cursor := f.alloc(key.within(fmt.Sprintf("registered%d", ref)).allocation(), flowObject{kind: "coroutine", budget: need, owner: owner, fields: []flowValue{producer}})
			value := flowValue{refs: []int{cursor}, caps: []int{scope.owner}}
			if n.Kind == "work-register" {
				f.checkWorkTransfer(value)
				packaged := f.alloc(key.within(fmt.Sprintf("package%d", ref)).allocation(), flowObject{kind: "work", fields: []flowValue{destination, value}})
				value = flowValue{refs: []int{packaged}}
			}
			result = joinFlow(result, value)
		}
		result.unknown = destination.unknown
	case "coroutine-scope":
		consumer := child(1)
		owner := f.owner(n, invocationFlowRow(env, n.Row), ctx, scopes)
		registry := f.alloc(key.within("registry").allocation(), flowObject{kind: "coroutine-registry", owner: owner, budget: types.SubstRigid(n.SourceType, env.types)})
		inside := append(slices.Clone(scopes), owner)
		result = f.apply(consumer, []flowValue{{refs: []int{registry}, caps: []int{owner}}}, env, key.within("consumer"), inside)
		f.escape(result, owner, "coroutine scope result", "The returned value")
	case "work-begin":
		ref := f.alloc(key.within("budget").allocation(), flowObject{kind: "work-owner", budget: types.SubstRigid(n.SourceType, env.types)})
		result = flowValue{refs: []int{ref}}
	case "work-end":
		for i := range n.Children {
			child(i)
		}
	case "work-facet":
		result = child(0)
	case "work-pack":
		facet, cursor := child(0), child(1)
		f.checkWorkTransfer(cursor)
		need := types.SubstRigid(n.SourceType, env.types)
		if f.collectWorkNeed != nil {
			f.collectWorkNeed(WorkNeed{Need: need, Immediate: true, Span: f.location, In: f.root})
		}
		f.checkWorkBudget(facet, need)
		if f.collectWorkNeed == nil {
			for _, ref := range cursor.refs {
				if actual := f.objects[ref]; actual.kind == "coroutine" && !workRowIncludes(need, actual.budget) {
					err := fmt.Errorf("WORK EFFECT BUDGET: package proof does not cover the actual coroutine residual in %s", f.root)
					f.errors[err.Error()] = err
				}
			}
		}
		for _, owner := range f.captures(facet) {
			f.store(cursor, owner, "work package")
		}
		ref := f.alloc(key.within("package").allocation(), flowObject{kind: "work", fields: []flowValue{facet, cursor}})
		result = flowValue{refs: []int{ref}}
	case "work-open":
		owner, work := child(0), child(1)
		for _, ref := range work.refs {
			o := f.objects[ref]
			if o.kind != "work" {
				continue
			}
			want, got := f.workOwnerIDs(owner), f.workOwnerIDs(o.fields[0])
			if !owner.unknown && !o.fields[0].unknown && (len(want) != 1 || len(got) != 1 || want[0] != got[0]) {
				err := fmt.Errorf("WORK OWNER MISMATCH: package does not belong to the selected Work.run owner in %s", f.root)
				f.errors[err.Error()] = err
			}
			result = joinFlow(result, o.fields[1])
		}
		result.unknown = result.unknown || work.unknown
	case "var":
		result = env.values[n.Name]
	case "global":
		if local, ok := env.values[n.Name]; ok {
			return f.trim(local, n.Type, env)
		}
		if d := f.defs[n.Name]; d != nil && !d.IsWorker() {
			result = f.callDef(n.Name, nil, n.TypeArgs, env, key, scopes)
		} else {
			id := f.alloc(key.allocation(), flowObject{kind: "global", def: n.Name, code: n, env: env.clone()})
			result.refs = []int{id}
		}
	case "native":
		result = joinFlow(all(0)...)
	case "completion_failure", "completion_replay":
		completed := child(0)
		result.unknown = completed.unknown
		for _, id := range completed.refs {
			object := f.objects[id]
			if object.kind != "completion" || len(object.fields) != 2 {
				result = joinFlow(result, completed)
				continue
			}
			if n.Kind == "completion_failure" {
				result = joinFlow(result, object.fields[1])
				continue
			}
			result = joinFlow(result, object.fields[0])
			for _, failureID := range object.fields[1].refs {
				failure := f.objects[failureID]
				if failure.kind != "completion-abort" || failure.code == nil {
					continue
				}
				inner := invocationFlowRow(env, n.Row)
				inner.evidence = maps.Clone(inner.rows[0].evidence)
				replayed := *failure.code
				replayed.ID = n.ID
				replayed.Children = nil
				for i, payload := range failure.fields {
					name := fmt.Sprintf("_completionPayload%d", i)
					inner.values[name] = payload
					replayed.Children = append(replayed.Children, &types.CaptureFlow{ID: n.ID, Kind: "var", Name: name})
				}
				result = joinFlow(result, f.eval(&replayed, inner, ctx, scopes, resumes))
			}
		}
	case "completion_capture":
		fn := child(0)
		boundary := &detachedFlow{site: key, callDepth: len(f.calls), outer: slices.Clone(scopes)}
		f.detached = append(f.detached, boundary)
		result = f.apply(fn, []flowValue{{}}, invocationFlowRow(env, n.Row), key, scopes)
		f.detached = f.detached[:len(f.detached)-1]
		id := f.alloc(key.allocation(), flowObject{kind: "completion", fields: []flowValue{result, boundary.failures}})
		result = flowValue{refs: []int{id}}
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
					f.mergeObjectEnv(&o.env, inner)
				}
			}
		}
		result = f.eval(n.Children[1], inner, ctx, scopes, resumes)
	case "lambda":
		id := f.alloc(key.allocation(), flowObject{kind: "lambda", code: n, def: f.contextDef(ctx), env: env.clone()})
		result.refs = []int{id}
	case "ctor":
		id := f.alloc(key.allocation(), flowObject{kind: "ctor", ctor: n.Index, fields: all(0)})
		result.refs = []int{id}
	case "call":
		fn := child(0)
		args := all(1)
		env = invocationFlowRow(env, n.Row)
		env.invocation = types.SubstRigid(n.SourceType, env.types)
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
		result = f.eval(n.Children[1], inner, ctx, scopes, resumes)
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
			result = joinFlow(result, f.eval(cl.Body, inner, ctx, scopes, resumes))
		}
		result = joinFlow(result, child(0))
	case "scope":
		acquired := f.syncEval("acquisition", n.Children[0], env, ctx, scopes, resumes)
		id := f.owner(n, env, ctx, scopes)
		resource := acquired
		if len(n.TypeArgs) > 0 && f.carry(n.TypeArgs[0], env) {
			resource.caps = append(resource.caps, id)
		}
		inner := env.clone()
		inner.values[n.Name] = resource
		inside := append(slices.Clone(scopes), id)
		result = f.eval(n.Children[1], inner, ctx, inside, resumes)
		f.syncEval("release", n.Children[2], inner, ctx, inside, resumes)
		f.escape(result, id, "scope result", "The returned value")
	case "handle":
		initial := child(0)
		id := f.owner(n, env, ctx, scopes)
		o := f.owners[id]
		f.store(initial, id, "initial state")
		f.merge(&o.state, initial)
		inner := env.clone()
		inner.evidence[n.Effects[0]] = []int{id}
		result = f.eval(n.Children[1], inner, ctx, append(slices.Clone(scopes), id), resumes)
		if n.Children[2] != nil {
			ret := env.clone()
			ret.values[n.Name] = o.state
			if len(n.Names) > 0 {
				ret.values[n.Names[0]] = result
			}
			result = f.eval(n.Children[2], ret, ctx, scopes, resumes)
		}
		result = joinFlow(result, o.answer)
		if n.Scoped {
			f.escape(result, id, "handler result", "The returned value")
		}
	case "perform", "exit":
		args := all(0)
		targets := env.evidence[n.Effects[0]]
		if n.Kind == "exit" && len(f.detached) > 0 {
			boundary := f.detached[len(f.detached)-1]
			outward := len(targets) == 0
			for _, target := range targets {
				outward = outward || target == 0 || slices.Contains(boundary.outer, target)
			}
			if outward {
				failureID := f.alloc(key.within("detached").allocation(), flowObject{kind: "completion-abort", code: n, fields: args})
				failure := flowValue{refs: []int{failureID}}
				boundary.failures = joinFlow(boundary.failures, failure)
				for _, context := range f.calls[boundary.callDepth:] {
					f.merge(&context.completionFailures, failure)
				}
				for _, arg := range args {
					for _, owner := range f.captures(arg) {
						if !slices.Contains(boundary.outer, owner) {
							f.escape(arg, owner, "completion failure payload", "the completion destination")
						}
					}
				}
				break
			}
		}
		cleanupExit := false
		if n.Kind == "exit" {
			for _, obligation := range f.synchronous {
				if obligation.phase != "release" {
					continue
				}
				for _, target := range targets {
					cleanupExit = cleanupExit || slices.Contains(obligation.enclosing, target)
				}
			}
		}
		if cleanupExit {
			for _, id := range scopes {
				o := f.owners[id]
				for _, cl := range o.code.Clauses {
					if cl.Suppressed != "" {
						for _, arg := range args {
							f.merge(&o.failures, arg)
							for _, owner := range f.captures(arg) {
								if !slices.Contains(o.parent, owner) || f.recursiveOwner(owner) {
									f.escape(arg, owner, "suppressed failure payload", "the report destination")
								}
							}
						}
						break
					}
				}
			}
		}
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
				if cl.Suppressed != "" {
					inner.values[cl.Suppressed] = o.failures
				}
				inner.values[o.code.Name] = o.state
				for i, name := range cl.Names {
					if i < len(args) {
						inner.values[name] = args[i]
					}
				}
				// Clause code runs with definition-site evidence. Its synchronous
				// execution still lies inside the caller's live resource scopes.
				savedSync, savedCalls := f.synchronous, f.suspensionCalls
				if n.Kind == "exit" {
					// An abort clause runs after unwinding to its handler. It is
					// outside callbacks entered beneath that handler, even though
					// the contract interpreter visits it at the operation site.
					f.synchronous = nil
					for _, obligation := range savedSync {
						ownedRelease := false
						if obligation.phase == "release" {
							for _, active := range f.active {
								ownedRelease = ownedRelease || active > 0
							}
						}
						// An owned producer may be stopped while this release is
						// pending. Its captured abort clause then executes as
						// part of synchronous close, without producer resumption.
						if !slices.Contains(obligation.enclosing, id) || ownedRelease {
							f.synchronous = append(f.synchronous, obligation)
						}
					}
					f.suspensionCalls = nil
					for i, call := range savedCalls {
						if call == f.contexts[o.context] {
							f.suspensionCalls = slices.Clone(savedCalls[:i+1])
							break
						}
					}
				}
				depth := len(f.effectHandlers)
				if n.Kind != "exit" {
					f.effectHandlers = append(f.effectHandlers, n.Effects[0])
				}
				got := f.invoke(fmt.Sprintf("clause:%s:%d", f.contextDef(o.context), cl.Body.ID), f.contextDef(o.context), cl.Body, inner, key, scopes, id, types.Control{Polymorphic: true})
				f.effectHandlers = f.effectHandlers[:depth]
				f.synchronous, f.suspensionCalls = savedSync, savedCalls
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
		if len(n.Children) > 1 && n.Children[1] != nil && slices.ContainsFunc(resumes, func(id int) bool { return id != 0 }) {
			next := child(1)
			for _, owner := range resumes {
				if owner != 0 {
					f.store(next, owner, "next state")
					f.merge(&f.owners[owner].state, next)
				}
			}
		}
		// Resume's apparent Core result is the handler answer, while the value
		// flowing back to the perform site is the operation result.
		return result
	case "suspend":
		request := child(0)
		f.suspend(0)
		for _, owner := range f.captures(request) {
			f.escape(request, owner, "suspension request", "the host")
		}
	case "coroutine":
		producer, consumer := child(0), child(1)
		env = invocationFlowRow(env, n.Row)
		owner := f.owner(n, env, ctx, scopes)
		cursor := f.alloc(key.within("cursor").allocation(), flowObject{kind: "coroutine", budget: types.SubstRigid(n.SourceType, env.types), owner: owner, fields: []flowValue{producer}})
		inside := append(slices.Clone(scopes), owner)
		result = f.apply(consumer, []flowValue{{refs: []int{cursor}, caps: []int{owner}}}, env, key.within("consumer"), inside)
		f.escape(result, owner, "cursor scope result", "The returned value")

	case "close":
		cursor := child(0)
		for _, ref := range cursor.refs {
			if o := f.objects[ref]; o.kind == "coroutine" {
				f.requireAdvance(o.owner)
			}
		}
	case "advance":
		cursor, input := child(0), child(1)
		value := f.advance(cursor, input, invocationFlowRow(env, n.Row), key, scopes)
		var finished flowValue
		for _, ref := range cursor.refs {
			if o := f.objects[ref]; o.kind == "coroutine" {
				finished = joinFlow(finished, f.owners[o.owner].finished)
			}
		}
		for i, fields := range [][]flowValue{{value}, {finished}, nil} {
			id := f.alloc(key.within(fmt.Sprintf("step%d", i)).allocation(), flowObject{kind: "ctor", ctor: i, fields: fields})
			result.refs = append(result.refs, id)
		}

	default:
		panic("unknown capture flow: " + n.Kind)
	}
	// A clause's structural wrappers carry the handler answer type, while
	// ResumeTail returns the operation payload to this flow interpreter. The
	// leaf already checked that payload's own type; trimming it to the answer
	// type here would erase a facet/resource resumed through a scalar handler.
	if len(resumes) > 0 {
		switch n.Kind {
		case "let", "seq", "branch", "choice", "case", "switch":
			return result
		}
	}
	return f.trim(result, n.Type, env)
}

func (f *flowChecker) requireDrive(owner int) {

	if f.owners[owner].code.Kind == "coroutine" {
		for i := len(f.calls) - 1; i >= 0; i-- {
			call := f.calls[i]
			// A recursive ancestor does not make this live lexical boundary
			// ambiguous. Only folding this boundary's own activation does.
			boundary := owner
			if f.owners[owner].registry != 0 {
				boundary = f.owners[owner].registry
			}
			if call.id == f.owners[boundary].context && !call.recursive {
				break
			}
			if !slices.Contains(call.drives, owner) {
				call.drives = append(call.drives, owner)
				f.grow()
			}
			f.requireControl(call, owner, "drive")
		}
	}
}

func (f *flowChecker) requireAdvance(owner int) {
	f.requireDrive(owner)
	f.checkAdvance(owner)
}

func (f *flowChecker) checkAdvance(owner int) {
	for _, call := range f.calls {
		if !slices.Contains(call.accesses, owner) {
			call.accesses = append(call.accesses, owner)
			f.grow()
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
func (f *flowChecker) advance(cursor, input flowValue, env flowEnv, site flowSite, scopes []int) flowValue {
	result := flowValue{unknown: cursor.unknown}
	for _, ref := range cursor.refs {
		o := f.objects[ref]
		if o.kind != "coroutine" {
			continue
		}
		owner := o.owner
		f.store(input, owner, "coroutine reply")
		f.merge(&f.owners[owner].reply, input)
		f.requireAdvance(owner)
		if f.active[owner] != 0 {
			continue
		}
		f.active[owner]++
		// A synchronous pull handles its producer's suspension. Do not export
		// that suspension as an obligation on the caller driving the cursor.
		saved, calls := f.synchronous, f.suspensionCalls
		f.pulls = append(f.pulls, flowPull{owner: owner, synchronous: saved, calls: calls})
		f.synchronous, f.suspensionCalls = nil, nil
		producerEnv := env.clone()
		// A queue can bring distinct owners to the same advance site. Pause
		// authority belongs to the selected owner, not to that call site.
		pause := f.alloc(site.within(fmt.Sprintf("pause%d", owner)).allocation(), flowObject{kind: "pause", owner: owner})
		body := f.apply(o.fields[0], []flowValue{{refs: []int{pause}, caps: []int{owner}}}, producerEnv, site.within("factory"), scopes)
		answer := f.apply(body, []flowValue{f.owners[owner].reply}, producerEnv, site.within("advance"), scopes)
		f.crossing(answer, owner, "coroutine result")
		f.merge(&f.owners[owner].finished, answer)
		f.synchronous, f.suspensionCalls = saved, calls
		f.pulls = f.pulls[:len(f.pulls)-1]
		f.active[owner]--
		result = joinFlow(result, f.owners[owner].yielded)
	}
	return result
}

func (f *flowChecker) syncEval(phase string, n *types.CaptureFlow, env flowEnv, ctx string, scopes []int, resumes []int) flowValue {
	f.synchronous = append(f.synchronous, synchronousFlow{phase: phase, span: f.location, enclosing: slices.Clone(scopes)})
	result := f.eval(n, env, ctx, scopes, resumes)
	f.synchronous = f.synchronous[:len(f.synchronous)-1]
	return result
}

// A suspension summary retains the ordinary effects through which it was
// reached. Replaying a recursive summary must not invent a source Suspension
// requirement on callers whose handler evidence already selects its transport.
type flowSuspension struct {
	owner   int
	effects []int
}

func (f *flowChecker) suspend(target int) { f.suspendThrough(target, nil) }
func (f *flowChecker) suspendThrough(target int, through []int) {
	effects := append(slices.Clone(through), f.effectHandlers...)
	slices.Sort(effects)
	effects = slices.Compact(effects)
	calls, synchronous := f.suspensionCalls, f.synchronous
	for i := len(f.pulls) - 1; i >= 0; i-- {
		pull := f.pulls[i]
		if target != 0 && pull.owner == target {
			break
		}
		calls = append(slices.Clone(pull.calls), calls...)
		synchronous = append(slices.Clone(pull.synchronous), synchronous...)
	}
	for _, call := range calls {
		if target == 0 && f.collectControlNeed != nil || target != 0 && f.owners[target].code.Kind == "coroutine" {
			f.requireControl(call, target, "suspend", effects...)
		}
		known := slices.ContainsFunc(call.suspensions, func(s flowSuspension) bool { return s.owner == target && slices.Equal(s.effects, effects) })
		if !known {
			call.suspensions = append(call.suspensions, flowSuspension{target, effects})
			f.grow()
		}
	}
	if len(synchronous) == 0 {
		return
	}
	obligation := synchronous[len(synchronous)-1]
	span := f.location
	if span.File == nil {
		span = obligation.span
	}
	err := SuspensionError{Span: span, In: f.root, Phase: obligation.phase}
	f.errors[err.Error()] = err
}

func (f *flowChecker) crossing(value flowValue, owner int, description string) {
	for _, id := range f.captures(value) {
		if !slices.Contains(f.owners[owner].parent, id) || f.recursiveOwner(id) {
			f.escape(value, id, description, "the coroutine driver")
		}
	}
}
