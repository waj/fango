package check

import (
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/infer"
	"github.com/waj/fango/internal/meta"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/staging"
	"github.com/waj/fango/internal/types"
)

// InstallObject interns a decoded module object into ck and installs its stage
// Core without replaying source inference or elaboration. Everything that can
// reject the object is checked before ck is mutated, so a failure leaves ck as
// it was; the fresh supply intentionally remains advanced.
//
// A decoded object's stage Core is deferred rather than installed, because
// nothing needs it until some module is checked from source. The deferral
// holds this remapper, so the stage half is interned against the same
// installed declarations and the same shared structure as the rest.
func InstallObject(ck *infer.Checker, stage *staging.Session, object *ModuleObject) error {
	return installObject(ck, stage, object, nil, false)
}

// installObject also takes the object's identities if they are already
// collected, and whether to leave an unread runtime section unread, for the
// caller to load once something needs it. Interning it is then no longer among
// the checks that can reject the object, so a failure is a violated invariant
// like a stage section's, not a miss.
func installObject(ck *infer.Checker, stage *staging.Session, object *ModuleObject, ids *remapIDs, deferRuntime bool) error {
	if object == nil || object.State == nil {
		return fmt.Errorf("module object has no state")
	}
	newTemplateBase := ck.Templates.Len()
	pending := object.Pending
	r := newRemapper(ck, object, ids, newTemplateBase)
	if err := r.remap(); err != nil {
		return err
	}
	if runtime := object.PendingRuntime; runtime != nil {
		runtime.remapper = r
		if !deferRuntime {
			runtime.Read()
			runtime.Allocate()
			runtime.Intern()
			if err := runtime.finish(object); err != nil {
				return err
			}
		}
	}
	state := object.State
	compatBase := &infer.ModuleState{Instances: append([]*infer.InstanceInfo(nil), ck.Instances...), Derivers: ck.Derivers}
	if errs := infer.ValidateModuleStates([]*infer.ModuleState{compatBase, state}); len(errs) != 0 {
		return fmt.Errorf("module compatibility: %s: %s", errs[0].Title, errs[0].Body)
	}
	if err := validateInstall(ck, state); err != nil {
		return err
	}
	// Each instance's cutoff names instances before it, installed or in this
	// object, and freezes the instance environment at the latest of them.
	refs := map[infer.DeclRef]int{}
	for i, instance := range ck.Instances {
		refs[instance.Ref] = i + 1
	}
	limits := make([]int, len(state.Instances))
	for i, instance := range state.Instances {
		position := len(ck.Instances) + i + 1
		refs[instance.Ref] = position
		for _, ref := range instance.Cutoff {
			n, ok := refs[ref]
			if !ok {
				return fmt.Errorf("instance %s has unknown cutoff reference %v", instance.Name, ref)
			}
			limits[i] = max(limits[i], n)
		}
	}
	for name, alias := range state.Aliases {
		ck.Aliases[name] = alias
	}
	for name, ty := range state.Types {
		ck.TypeNames[name] = ty
	}
	for _, adt := range state.ADTs {
		ck.ADTs[adt.Con.Unique] = adt
		ck.ADTOrder = append(ck.ADTOrder, adt)
	}
	for name, ctor := range state.Ctors {
		ck.Ctors[name] = ctor
	}
	for name, effect := range state.Effects {
		ck.Effects[name], ck.EffectsByUnique[effect.Unique] = effect, effect
		if types.SurfaceName(effect.Name) == "IO" {
			ck.IO = effect
		}
	}
	for name, alias := range state.EffectAliases {
		ck.EffectAliases[name] = alias
	}
	for name, alias := range state.TypeAliases {
		ck.TypeAliases[name] = alias
	}
	for name, op := range state.Operations {
		ck.Operations[name] = op
	}
	for name, class := range state.Classes {
		ck.Classes[name] = class
	}
	for name, method := range state.Methods {
		ck.Methods[name] = method
	}
	for i, instance := range state.Instances {
		instance.Limit = limits[i]
		ck.Instances = append(ck.Instances, instance)
	}
	for name, scheme := range state.Schemes {
		ck.Env.Bind(name, scheme)
	}
	for name, arity := range state.Workers {
		ck.Workers[name] = arity
	}
	for name, native := range state.Natives {
		ck.Natives[name] = native
	}
	for name, scheme := range state.Intrinsics {
		ck.Intrinsics[name] = scheme
	}
	for name, deriver := range state.Derivers {
		ck.Derivers[name] = deriver
	}
	for name, summary := range state.Captures {
		ck.CaptureSummaries[name] = summary
		if scheme, ok := ck.Env.Lookup(name); ok {
			scheme.CaptureVars, scheme.Captures = append([]types.CaptureVar(nil), summary.Vars...), summary.Captures
			ck.Env.Bind(name, scheme)
		}
	}
	if ck.InstanceImports == nil {
		ck.InstanceImports = map[string]map[string]bool{}
	}
	visible := map[string]bool{}
	for name, yes := range state.Visibility {
		visible[name] = yes
	}
	ck.InstanceImports[state.Name] = visible
	ck.Templates.Append(object.Templates)
	if pending != nil {
		pending.remapper = r
		stage.Defer(state.Name, pending)
	} else {
		stage.InstallCore(object.Stage, object.StageGroups)
	}
	return nil
}

// A deferred section is interned through this remapper in the steps
// staging.InternAll runs. Decoding through the object's own decoder returns the
// same pointers for structure the installed half already holds, and the memo
// recognizes those as already remapped, so the halves cannot acquire
// disagreeing identities. Read and Intern touch this object's nodes and read the
// checker, which nothing writes while sections are being forced.

// Read implements staging.Section.
func (p *PendingStage) Read() {
	p.decode()
	if p.err == nil {
		p.ids = p.remapper.collect(reflect.ValueOf(p.decoded))
	}
}

// Allocate implements staging.Section.
func (p *PendingStage) Allocate() {
	if p.ids != nil {
		p.remapper.allocate(p.ids)
	}
}

// Intern implements staging.Section.
func (p *PendingStage) Intern() {
	if p.ids != nil {
		p.remapper.pointer(reflect.ValueOf(p.decoded))
	}
}

// Load implements staging.Section.
func (p *PendingStage) Load() ([]core.Def, []staging.Group, int, error) {
	if p.err != nil {
		return nil, nil, 0, p.err
	}
	if p.remapper.err != nil {
		return nil, nil, 0, p.remapper.err
	}
	return p.decoded.Stage, p.decoded.Groups, p.size(), nil
}

// Read implements staging.Interning.
func (p *PendingRuntime) Read() {
	p.decode()
	if p.err == nil {
		p.ids = p.remapper.collect(reflect.ValueOf(p.decoded))
	}
}

// Allocate implements staging.Interning.
func (p *PendingRuntime) Allocate() {
	if p.ids != nil {
		p.remapper.allocate(p.ids)
	}
}

// Intern implements staging.Interning.
func (p *PendingRuntime) Intern() {
	if p.ids != nil {
		p.remapper.pointer(reflect.ValueOf(p.decoded))
	}
}

// finish moves an interned runtime section into its object.
func (p *PendingRuntime) finish(object *ModuleObject) error {
	if p.err != nil {
		return p.err
	}
	if p.remapper.err != nil {
		return p.remapper.err
	}
	object.Runtime, object.PendingRuntime = p.decoded.Defs, nil
	return nil
}

func validateInstall(ck *infer.Checker, state *infer.ModuleState) error {
	checks := []struct {
		label              string
		incoming, existing any
	}{
		{"type", state.Types, ck.TypeNames}, {"constructor", state.Ctors, ck.Ctors}, {"effect", state.Effects, ck.Effects}, {"effect alias", state.EffectAliases, ck.EffectAliases}, {"type alias", state.TypeAliases, ck.TypeAliases},
		{"operation", state.Operations, ck.Operations}, {"class", state.Classes, ck.Classes}, {"method", state.Methods, ck.Methods},
		{"value", state.Schemes, nil}, {"native", state.Natives, ck.Natives}, {"intrinsic", state.Intrinsics, ck.Intrinsics}, {"deriver", state.Derivers, ck.Derivers},
	}
	for _, check := range checks {
		incoming := reflect.ValueOf(check.incoming)
		if !incoming.IsValid() {
			continue
		}
		for _, key := range incoming.MapKeys() {
			name := key.String()
			if check.label == "value" {
				if ck.Env.Has(name) {
					return fmt.Errorf("install %s %q: already defined", check.label, name)
				}
				continue
			}
			existing := reflect.ValueOf(check.existing)
			if existing.IsValid() && existing.MapIndex(key).IsValid() {
				return fmt.Errorf("install %s %q: already defined", check.label, name)
			}
		}
	}
	return nil
}

type remapper struct {
	ck                       *infer.Checker
	object                   *ModuleObject
	templateOld, templateNew int
	unique, effect           map[int]int
	vars                     map[int]int
	captures                 map[types.CaptureVar]types.CaptureVar
	scopes                   map[types.ScopeID]types.ScopeID
	resumes                  map[types.ResumeID]types.ResumeID
	memo                     map[uintptr]reflect.Value
	// written holds the identity each remapped nominal was written with.
	// Remapping in place overwrites TCon.Unique, and a later lookup through
	// the same node must still ask about the identity it was written with;
	// a node not yet remapped still carries it.
	written      map[*types.TCon]int
	ownedADTs    map[*types.ADTInfo]bool
	ownedCtors   map[*types.CtorInfo]bool
	ownedEffects map[*types.EffectInfo]bool
	ownedOps     map[*types.EffectOp]bool
	ownedClasses map[*types.ClassInfo]bool
	err          error
}

func newRemapper(ck *infer.Checker, object *ModuleObject, ids *remapIDs, templateBase int) *remapper {
	r := &remapper{ck: ck, object: object, templateOld: object.TemplateBase, templateNew: templateBase,
		unique: map[int]int{}, effect: map[int]int{}, vars: map[int]int{}, captures: map[types.CaptureVar]types.CaptureVar{}, scopes: map[types.ScopeID]types.ScopeID{}, resumes: map[types.ResumeID]types.ResumeID{}, memo: map[uintptr]reflect.Value{}, written: map[*types.TCon]int{},
		ownedADTs: map[*types.ADTInfo]bool{}, ownedCtors: map[*types.CtorInfo]bool{}, ownedEffects: map[*types.EffectInfo]bool{}, ownedOps: map[*types.EffectOp]bool{}, ownedClasses: map[*types.ClassInfo]bool{}}
	for _, adt := range object.State.ADTs {
		r.ownedADTs[adt] = true
		r.unique[adt.Con.Unique] = ck.Sup.NextUnique()
		for _, ctor := range adt.Ctors {
			r.ownedCtors[ctor] = true
		}
	}
	var effects []*types.EffectInfo
	for _, effect := range object.State.Effects {
		if !r.ownedEffects[effect] {
			r.ownedEffects[effect] = true
			effects = append(effects, effect)
			for _, op := range effect.Ops {
				r.ownedOps[op] = true
			}
		}
	}
	// Evidence is ordered by name first, so this only keeps same-named
	// effects, and so generated names, in the defining session's order.
	sort.Slice(effects, func(i, j int) bool { return effects[i].Unique < effects[j].Unique })
	for _, effect := range effects {
		r.effect[effect.Unique] = ck.Sup.NextUnique()
	}
	var nominalIDs []int
	for id := range object.Nominals {
		nominalIDs = append(nominalIDs, id)
	}
	sort.Ints(nominalIDs)
	// An object names every nominal its session knew of, so this index beats
	// searching the installed declarations once for each.
	installedNominals := make(map[string]int, len(ck.TypeNames)+len(ck.ADTs))
	for _, builtin := range []*types.TCon{ck.B.Int, ck.B.Float, ck.B.String, ck.B.Char, ck.B.Bool, ck.B.Unit} {
		installedNominals[builtin.Name] = builtin.Unique
	}
	for _, ty := range ck.TypeNames {
		if con, ok := ty.(*types.TCon); ok {
			installedNominals[con.Name] = con.Unique
		}
	}
	for _, adt := range ck.ADTs {
		installedNominals[adt.Con.Name] = adt.Con.Unique
	}
	for _, id := range nominalIDs {
		if _, mapped := r.unique[id]; mapped {
			continue
		}
		if unique, ok := installedNominals[object.Nominals[id]]; ok {
			r.unique[id] = unique
		}
	}
	var effectIDs []int
	for id := range object.EffectNames {
		effectIDs = append(effectIDs, id)
	}
	sort.Ints(effectIDs)
	for _, id := range effectIDs {
		if _, mapped := r.effect[id]; !mapped {
			if installed := ck.Effects[object.EffectNames[id]]; installed != nil {
				r.effect[id] = installed.Unique
			}
		}
	}
	for _, class := range object.State.Classes {
		r.ownedClasses[class] = true
	}
	// A written scope is named `contract#index` after the installed capture
	// contract that declares it. Any alias resolves: importing another module
	// can add an earlier alphabetical name for the same durable scope.
	for id, name := range object.ScopeNames {
		if installed := installedScope(ck, name); installed != 0 {
			r.scopes[id] = installed
		} else {
			r.err = fmt.Errorf("unknown imported capture scope %s", name)
		}
	}
	if ids == nil {
		ids = collectIDs(reflect.ValueOf(object), nil)
	}
	r.allocate(ids)
	return r
}

func installedScope(ck *infer.Checker, written string) types.ScopeID {
	at := strings.LastIndexByte(written, '#')
	if at < 0 {
		return 0
	}
	index, err := strconv.Atoi(written[at+1:])
	if err != nil || index < 0 || strconv.Itoa(index) != written[at+1:] {
		return 0
	}
	summary, ok := ck.CaptureSummaries[written[:at]]
	if !ok || index >= len(summary.Captures.Scopes) {
		return 0
	}
	return summary.Captures.Scopes[index]
}

// Giving a decoded value the identities it needs takes two steps. Foreign
// declarations keep the installed parameters they describe, and everything the
// value allocates for itself is renamed in a deterministic order. A deferred
// section goes through them too, so it cannot disagree with the half of the
// object already installed.
//
// collect finds the identities a decoded value needs. It reads only the value
// and this remapper's memo.
func (r *remapper) collect(v reflect.Value) *remapIDs {
	return collectIDs(v, r.memo)
}

func collectIDs(v reflect.Value, done map[uintptr]reflect.Value) *remapIDs {
	ids := &remapIDs{permissions: map[int]bool{}, vars: map[int]*types.TVar{}, captures: map[types.CaptureVar]bool{}, scopes: map[types.ScopeID]bool{}, resumes: map[types.ResumeID]bool{},
		done: done}
	collectRemapIDs(v, make(map[uintptr]bool, 1024), ids)
	return ids
}

// allocate gives collected identities their values from the shared supply, so
// sections take it one at a time and in order.
func (r *remapper) allocate(ids *remapIDs) {
	ck := r.ck
	r.alignForeignParams(ids)
	// Fresh permission labels have no module declaration to intern. Allocate
	// them in source identity order, preserving sharing across deferred sections.
	var permissionIDs []int
	for id := range ids.permissions {
		permissionIDs = append(permissionIDs, id)
	}
	sort.Ints(permissionIDs)
	for _, id := range permissionIDs {
		if _, ok := r.effect[id]; !ok {
			r.effect[id] = ck.Sup.NextUnique()
		}
	}
	vars, captures, scopes, resumes := ids.vars, ids.captures, ids.scopes, ids.resumes
	var varIDs []int
	for id := range vars {
		varIDs = append(varIDs, id)
	}
	sort.Ints(varIDs)
	for _, id := range varIDs {
		if _, aligned := r.vars[id]; aligned {
			continue
		}
		v := vars[id]
		if v.Rigid {
			r.vars[id] = ck.Sup.FreshRigid(v.Kind).ID
		} else {
			r.vars[id] = ck.Sup.FreshVar(v.Kind).ID
		}
	}
	var captureIDs []int
	for id := range captures {
		if id != 0 {
			captureIDs = append(captureIDs, int(id))
		}
	}
	sort.Ints(captureIDs)
	for _, id := range captureIDs {
		if _, mapped := r.captures[types.CaptureVar(id)]; !mapped {
			r.captures[types.CaptureVar(id)] = ck.Sup.FreshCapture()
		}
	}
	var scopeIDs []int
	for id := range scopes {
		if id != 0 {
			scopeIDs = append(scopeIDs, int(id))
		}
	}
	sort.Ints(scopeIDs)
	for _, id := range scopeIDs {
		if _, aligned := r.scopes[types.ScopeID(id)]; !aligned {
			r.scopes[types.ScopeID(id)] = ck.Sup.FreshScope()
		}
	}
	var resumeIDs []int
	for id := range resumes {
		if id != 0 {
			resumeIDs = append(resumeIDs, int(id))
		}
	}
	sort.Ints(resumeIDs)
	for _, id := range resumeIDs {
		if _, mapped := r.resumes[types.ResumeID(id)]; !mapped {
			ck.ResumeGen++
			r.resumes[types.ResumeID(id)] = ck.ResumeGen
		}
	}
}

// alignForeignParams keeps a decoded copy of a foreign declaration on the
// installed declaration's parameter identities. Types reached only by value —
// capture-contract clause fields, for instance — would otherwise receive fresh
// variables and disagree with the interned declaration they describe.
func (r *remapper) alignForeignParams(ids *remapIDs) {
	align := func(old, installed []*types.TVar) {
		if len(old) != len(installed) {
			return
		}
		for i, v := range old {
			if v != nil && installed[i] != nil {
				r.vars[v.ID] = installed[i].ID
			}
		}
	}
	for _, adt := range ids.adts {
		if r.ownedADTs[adt] {
			continue
		}
		if n := r.uniqueForCon(adt.Con); n >= 0 {
			if installed := r.ck.ADTs[n]; installed != nil {
				align(adt.Params, installed.Params)
			}
		}
	}
	for _, effect := range ids.effects {
		if r.ownedEffects[effect] {
			continue
		}
		if installed := r.ck.Effects[effect.Name]; installed != nil {
			align(effect.Params, installed.Params)
		}
	}
	for _, class := range ids.classes {
		if r.ownedClasses[class] {
			continue
		}
		if installed := r.ck.Classes[class.Name]; installed != nil {
			align([]*types.TVar{class.Param}, []*types.TVar{installed.Param})
		}
	}
}

// remapIDs collects the identities a decoded object allocates afresh, plus
// the foreign nominals whose declared parameters must align with the
// installed declaration instead.
type remapIDs struct {
	permissions map[int]bool
	vars        map[int]*types.TVar
	captures    map[types.CaptureVar]bool
	scopes      map[types.ScopeID]bool
	resumes     map[types.ResumeID]bool
	adts        []*types.ADTInfo
	effects     []*types.EffectInfo
	classes     []*types.ClassInfo
	// done is the remapper's memo. A deferred section shares nodes with its
	// object, and those already carry this installation's identities, so the
	// walk does not mistake them for written ones.
	done map[uintptr]reflect.Value
}

func collectRemapIDs(v reflect.Value, seen map[uintptr]bool, ids *remapIDs) {
	if !v.IsValid() || !planFor(v.Type()).remaps {
		return
	}
	if v.Type() == effLabelReflectType {
		if label := v.Interface().(types.EffLabel); label.Scoped {
			ids.permissions[label.Unique] = true
		}
	}
	if v.Type() == captureVarType {
		ids.captures[types.CaptureVar(v.Int())] = true
		return
	}
	if v.Type() == scopeType {
		ids.scopes[types.ScopeID(v.Int())] = true
		return
	}
	if v.Type() == resumeType {
		ids.resumes[types.ResumeID(v.Int())] = true
		return
	}
	if v.Kind() == reflect.Interface {
		if !v.IsNil() {
			collectRemapIDs(v.Elem(), seen, ids)
		}
		return
	}
	if v.Kind() == reflect.Pointer {
		if v.IsNil() || seen[v.Pointer()] {
			return
		}
		seen[v.Pointer()] = true
		if _, done := ids.done[v.Pointer()]; done {
			return
		}
		if v.Type() == tVarPtr {
			tv := v.Interface().(*types.TVar)
			if old := ids.vars[tv.ID]; old == nil {
				ids.vars[tv.ID] = tv
			}
			return
		}
		switch v.Type() {
		case adtPtr:
			ids.adts = append(ids.adts, v.Interface().(*types.ADTInfo))
		case effectPtr:
			ids.effects = append(ids.effects, v.Interface().(*types.EffectInfo))
		case classPtr:
			ids.classes = append(ids.classes, v.Interface().(*types.ClassInfo))
		}
		collectRemapIDs(v.Elem(), seen, ids)
		return
	}
	switch v.Kind() {
	case reflect.Struct:
		for _, i := range planFor(v.Type()).fields {
			collectRemapIDs(v.Field(i), seen, ids)
		}
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			collectRemapIDs(v.Index(i), seen, ids)
		}
	case reflect.Map:
		it := v.MapRange()
		for it.Next() {
			collectRemapIDs(it.Key(), seen, ids)
			collectRemapIDs(it.Value(), seen, ids)
		}
	}
}

// remap interns the decoded object in place. The decoder made it for this
// installation alone, so nothing else can observe the rewrite.
func (r *remapper) remap() error {
	r.pointer(reflect.ValueOf(r.object))
	return r.err
}

var (
	tVarPtr            = reflect.TypeOf((*types.TVar)(nil))
	tConPtr            = reflect.TypeOf((*types.TCon)(nil))
	adtPtr             = reflect.TypeOf((*types.ADTInfo)(nil))
	ctorPtr            = reflect.TypeOf((*types.CtorInfo)(nil))
	effectPtr          = reflect.TypeOf((*types.EffectInfo)(nil))
	opPtr              = reflect.TypeOf((*types.EffectOp)(nil))
	classPtr           = reflect.TypeOf((*types.ClassInfo)(nil))
	methodPtr          = reflect.TypeOf((*types.MethodInfo)(nil))
	nativePtr          = reflect.TypeOf((*types.NativeInfo)(nil))
	reprPtr            = reflect.TypeOf((*meta.TypeRepr)(nil))
	spanType           = reflect.TypeOf(source.Span{})
	effectInstanceType = reflect.TypeOf(core.EffectInstance{})
	quoteType          = reflect.TypeOf(core.Quote{})
	codeType           = reflect.TypeOf(meta.Code{})
	captureVarType     = reflect.TypeOf(types.CaptureVar(0))
	scopeType          = reflect.TypeOf(types.ScopeID(0))
	resumeType         = reflect.TypeOf(types.ResumeID(0))
)

// resumeID is the one mapping from a written resume identity to this
// installation's. All occurrences use the same mapping.
func (r *remapper) resumeID(old types.ResumeID) types.ResumeID {
	if old == 0 {
		return 0
	}
	n, ok := r.resumes[old]
	if !ok {
		r.identityMissing("resume", int(old))
	}
	return n
}

// identityMissing reports an identity allocate never saw. Remapping allocates
// nothing itself, because deferred sections remap concurrently, so this is
// collectRemapIDs and fix disagreeing about what a value holds.
func (r *remapper) identityMissing(kind string, old int) {
	if r.err == nil {
		r.err = fmt.Errorf("uncollected %s identity %d", kind, old)
	}
}

// pointer returns what a decoded pointer becomes: an installed declaration it
// denotes, or the same node with its contents remapped in place. The memo
// makes shared and cyclic structure, and a deferred section that shares nodes
// with its object, resolve to one answer.
func (r *remapper) pointer(v reflect.Value) reflect.Value {
	key := v.Pointer()
	if got, ok := r.memo[key]; ok {
		return got
	}
	if replacement := r.intern(v); replacement.IsValid() {
		r.memo[key] = replacement
		return replacement
	}
	// v may be a settable location the caller reuses; keep the pointer itself.
	v = v.Elem().Addr()
	r.memo[key] = v
	r.fix(v.Elem())
	r.remapPointer(v)
	return v
}

// fix remaps a settable decoded value in place.
func (r *remapper) fix(v reflect.Value) {
	t := v.Type()
	plan := planFor(t)
	if !plan.remaps {
		return
	}
	switch t {
	case captureVarType:
		if old := types.CaptureVar(v.Int()); old != 0 {
			n, ok := r.captures[old]
			if !ok {
				r.identityMissing("capture", int(old))
			}
			v.SetInt(int64(n))
		}
		return
	case scopeType:
		if old := types.ScopeID(v.Int()); old != 0 {
			n, ok := r.scopes[old]
			if !ok {
				r.identityMissing("scope", int(old))
			}
			v.SetInt(int64(n))
		}
		return
	case resumeType:
		v.SetInt(int64(r.resumeID(types.ResumeID(v.Int()))))
		return
	}
	switch v.Kind() {
	case reflect.Interface:
		if v.IsNil() {
			return
		}
		e := v.Elem()
		if e.Kind() == reflect.Pointer {
			if p := r.pointer(e); p.Pointer() != e.Pointer() {
				v.Set(p)
			}
			return
		}
		if !planFor(e.Type()).remaps {
			return
		}
		x := reflect.New(e.Type()).Elem()
		x.Set(e)
		r.fix(x)
		v.Set(x)
	case reflect.Pointer:
		if !v.IsNil() {
			if p := r.pointer(v); p.Pointer() != v.Pointer() {
				v.Set(p)
			}
		}
	case reflect.Struct:
		for _, i := range plan.fields {
			r.fix(v.Field(i))
		}
		r.remapStruct(v)
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			r.fix(v.Index(i))
		}
	case reflect.Map:
		if v.Len() == 0 {
			return
		}
		out := reflect.MakeMapWithSize(t, v.Len())
		k, x := reflect.New(t.Key()).Elem(), reflect.New(t.Elem()).Elem()
		it := v.MapRange()
		for it.Next() {
			k.Set(it.Key())
			x.Set(it.Value())
			r.fix(k)
			r.fix(x)
			out.SetMapIndex(k, x)
		}
		v.Set(out)
	}
}

// remapPlan is what installation needs to know about a decoded type: the
// fields the codec carries, and whether any value of the type can hold an
// identity to remap. Types that cannot — strings, plain numbers, spans — are
// skipped without being walked.
type remapPlan struct {
	fields []int
	remaps bool
}

var remapPlans sync.Map // reflect.Type → *remapPlan

func planFor(t reflect.Type) *remapPlan {
	if plan, ok := remapPlans.Load(t); ok {
		return plan.(*remapPlan)
	}
	plan := buildPlan(t, map[reflect.Type]bool{})
	remapPlans.Store(t, plan)
	return plan
}

func buildPlan(t reflect.Type, visiting map[reflect.Type]bool) *remapPlan {
	plan := &remapPlan{}
	if t.Kind() == reflect.Struct {
		for i := 0; i < t.NumField(); i++ {
			if t.Field(i).Tag.Get("object") != "omit" {
				plan.fields = append(plan.fields, i)
			}
		}
	}
	plan.remaps = typeRemaps(t, visiting)
	return plan
}

func typeRemaps(t reflect.Type, visiting map[reflect.Type]bool) bool {
	switch t {
	case spanType:
		return false
	case captureVarType, scopeType, resumeType, effLabelReflectType, effectInstanceType, quoteType, codeType:
		return true
	}
	switch t.Kind() {
	case reflect.Interface, reflect.Pointer:
		return true
	case reflect.Slice, reflect.Array:
		return typeRemaps(t.Elem(), visiting)
	case reflect.Map:
		return typeRemaps(t.Key(), visiting) || typeRemaps(t.Elem(), visiting)
	case reflect.Struct:
		if visiting[t] {
			return false
		}
		visiting[t] = true
		for i := 0; i < t.NumField(); i++ {
			if f := t.Field(i); f.Tag.Get("object") != "omit" && typeRemaps(f.Type, visiting) {
				return true
			}
		}
	}
	return false
}

func (r *remapper) intern(v reflect.Value) reflect.Value {
	switch v.Type() {
	case adtPtr:
		a := v.Interface().(*types.ADTInfo)
		if r.ownedADTs[a] {
			return reflect.Value{}
		}
		if n := r.uniqueForCon(a.Con); n >= 0 {
			return reflect.ValueOf(r.ck.ADTs[n])
		}
	case ctorPtr:
		c := v.Interface().(*types.CtorInfo)
		if r.ownedCtors[c] {
			return reflect.Value{}
		}
		if installed := r.ck.Ctors[c.Name]; installed != nil {
			return reflect.ValueOf(installed)
		}
		// Generated dictionary constructors are reachable only through their
		// ADT, so name lookup alone would keep a copy whose field variables no
		// longer match the installed type parameters.
		if n := r.uniqueForCon(c.Result); n >= 0 {
			if adt := r.ck.ADTs[n]; adt != nil {
				for _, ctor := range adt.Ctors {
					if ctor.Name == c.Name {
						return reflect.ValueOf(ctor)
					}
				}
			}
		}
	case effectPtr:
		e := v.Interface().(*types.EffectInfo)
		if r.ownedEffects[e] {
			return reflect.Value{}
		}
		if installed := r.ck.Effects[e.Name]; installed != nil {
			return reflect.ValueOf(installed)
		}
	case opPtr:
		o := v.Interface().(*types.EffectOp)
		if r.ownedOps[o] {
			return reflect.Value{}
		}
		if installed := r.ck.Operations[o.Name]; installed != nil {
			return reflect.ValueOf(installed)
		}
	case classPtr:
		c := v.Interface().(*types.ClassInfo)
		if r.ownedClasses[c] {
			return reflect.Value{}
		}
		if installed := r.ck.Classes[c.Name]; installed != nil {
			return reflect.ValueOf(installed)
		}
	case methodPtr:
		m := v.Interface().(*types.MethodInfo)
		if installed := r.ck.Methods[m.Name]; installed != nil {
			return reflect.ValueOf(installed)
		}
	case nativePtr:
		n := v.Interface().(*types.NativeInfo)
		if installed := r.ck.Natives[n.Name]; installed != nil {
			return reflect.ValueOf(installed)
		}
	}
	return reflect.Value{}
}

func (r *remapper) uniqueForCon(c *types.TCon) int {
	if c == nil {
		return -1
	}
	written, ok := r.written[c]
	if !ok {
		written = c.Unique
	}
	if n, ok := r.unique[written]; ok {
		return n
	}
	builtins := []*types.TCon{r.ck.B.Int, r.ck.B.Float, r.ck.B.String, r.ck.B.Char, r.ck.B.Bool, r.ck.B.Unit}
	for _, b := range builtins {
		if c.Name == b.Name {
			r.unique[written] = b.Unique
			return b.Unique
		}
	}
	for _, ty := range r.ck.TypeNames {
		if installed, ok := ty.(*types.TCon); ok && installed.Name == c.Name {
			r.unique[written] = installed.Unique
			return installed.Unique
		}
	}
	for _, adt := range r.ck.ADTs {
		if adt.Con.Name == c.Name {
			r.unique[written] = adt.Con.Unique
			return adt.Con.Unique
		}
	}
	return -1
}

func (r *remapper) remapPointer(v reflect.Value) {
	switch v.Type() {
	case tVarPtr:
		t := v.Interface().(*types.TVar)
		old := t.ID
		n, ok := r.vars[old]
		if !ok {
			r.identityMissing("type variable", old)
		}
		t.ID = n
	case tConPtr:
		c := v.Interface().(*types.TCon)
		if n := r.uniqueForCon(c); n >= 0 {
			r.written[c] = c.Unique
			c.Unique = n
		} else if r.err == nil {
			r.err = fmt.Errorf("unknown nominal identity %q (%d)", c.Name, c.Unique)
		}
	case effectPtr:
		e := v.Interface().(*types.EffectInfo)
		if n, ok := r.effect[e.Unique]; ok {
			e.Unique = n
		} else if installed := r.ck.Effects[e.Name]; installed != nil {
			e.Unique = installed.Unique
		} else if r.err == nil {
			r.err = fmt.Errorf("unknown effect identity %q (%d)", e.Name, e.Unique)
		}
	case reprPtr:
		repr := v.Interface().(*meta.TypeRepr)
		visible := map[int]bool{}
		for old, yes := range repr.Visible {
			if n, ok := r.unique[old]; ok {
				visible[n] = yes
			} else if n, ok := r.effect[old]; ok {
				visible[n] = yes
			}
		}
		if repr.Visible != nil {
			repr.Visible = visible
		}
		repr.Schema = r.ck
	}
}

func (r *remapper) remapStruct(v reflect.Value) {
	switch x := v.Addr().Interface().(type) {
	case *types.EffLabel:
		old := x.Unique
		if n, ok := r.effect[old]; ok {
			x.Unique = n
		} else if installed := r.ck.Effects[x.Name]; installed != nil {
			x.Unique = installed.Unique
			r.effect[old] = installed.Unique
		} else if r.err == nil {
			r.err = fmt.Errorf("unknown effect identity %q (%d)", x.Name, old)
		}
	case *core.EffectInstance:
		old := x.Unique
		if n, ok := r.effect[old]; ok {
			x.Unique = n
		} else if installed := r.ck.Effects[x.Name]; installed != nil {
			x.Unique = installed.Unique
			r.effect[old] = installed.Unique
		} else if r.err == nil {
			r.err = fmt.Errorf("unknown effect identity %q (%d)", x.Name, old)
		}
	case *core.Quote:
		if x.Template >= r.templateOld {
			x.Template = r.templateNew + x.Template - r.templateOld
		}
	case *meta.Code:
		if x.Template >= r.templateOld {
			x.Template = r.templateNew + x.Template - r.templateOld
		}
	}
}
