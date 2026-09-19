package check

import (
	"fmt"
	"reflect"
	"sort"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/infer"
	"github.com/waj/fango/internal/meta"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/staging"
	"github.com/waj/fango/internal/types"
)

// InstallObject interns a decoded module object into ck and installs its stage
// Core without replaying source inference or elaboration. The mutation is
// transactional; the fresh supply intentionally remains advanced on failure.
//
// A decoded object's stage Core is deferred rather than installed, because
// nothing needs it until some module is checked from source. The deferral
// holds this remapper, so the stage half is interned against the same
// installed declarations and the same shared structure as the rest.
func InstallObject(ck *infer.Checker, stage *staging.Session, object *ModuleObject) error {
	if object == nil || object.State == nil {
		return fmt.Errorf("module object has no state")
	}
	newTemplateBase := ck.Templates.Len()
	pending := object.Pending
	r := newRemapper(ck, object, newTemplateBase)
	if err := r.remap(); err != nil {
		return err
	}
	*object = *r.object
	object.Pending = pending
	state := object.State
	compatBase := &infer.ModuleState{Instances: append([]*infer.InstanceInfo(nil), ck.Instances...), Derivers: ck.Derivers}
	if errs := infer.ValidateModuleStates([]*infer.ModuleState{compatBase, state}); len(errs) != 0 {
		return fmt.Errorf("module compatibility: %s: %s", errs[0].Title, errs[0].Body)
	}
	restore := ck.Checkpoint()
	committed := false
	defer func() {
		if !committed {
			restore()
		}
	}()
	if err := validateInstall(ck, state); err != nil {
		return err
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
	for name, op := range state.Operations {
		ck.Operations[name] = op
	}
	for name, class := range state.Classes {
		ck.Classes[name] = class
	}
	for name, method := range state.Methods {
		ck.Methods[name] = method
	}
	refs := map[infer.DeclRef]int{}
	for i, instance := range ck.Instances {
		refs[instance.Ref] = i + 1
	}
	for _, instance := range state.Instances {
		refs[instance.Ref] = len(ck.Instances) + 1
		limit := 0
		for _, ref := range instance.Cutoff {
			n, ok := refs[ref]
			if !ok {
				return fmt.Errorf("instance %s has unknown cutoff reference %v", instance.Name, ref)
			}
			if n > limit {
				limit = n
			}
		}
		instance.Limit = limit
		ck.Instances = append(ck.Instances, instance)
		refs[instance.Ref] = len(ck.Instances)
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
			scheme.CaptureVars, scheme.Captures, scheme.CaptureContract = append([]types.CaptureVar(nil), summary.Vars...), summary.Captures, summary.Contract
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
		stage.Defer(state.Name, func() ([]core.Def, []staging.Group, int, error) { return r.stage(pending) })
	} else {
		stage.InstallCore(object.Stage, object.StageGroups)
	}
	committed = true
	return nil
}

// stage reads and interns a deferred stage section. Decoding it through the
// object's own decoder returns the same pointers for structure the installed
// half already holds, and this remapper's memo maps those to the copies it
// installed, so the two halves cannot acquire disagreeing identities.
func (r *remapper) stage(pending *PendingStage) ([]core.Def, []staging.Group, int, error) {
	size := pending.size()
	payload, err := pending.load()
	if err != nil {
		return nil, nil, 0, err
	}
	r.extend(reflect.ValueOf(payload))
	v, err := r.rewrite(reflect.ValueOf(payload))
	if err != nil {
		return nil, nil, 0, err
	}
	if r.err != nil {
		return nil, nil, 0, r.err
	}
	out := v.Interface().(*stagePayload)
	return out.Stage, out.Groups, size, nil
}

func validateInstall(ck *infer.Checker, state *infer.ModuleState) error {
	checks := []struct {
		label              string
		incoming, existing any
	}{
		{"type", state.Types, ck.TypeNames}, {"constructor", state.Ctors, ck.Ctors}, {"effect", state.Effects, ck.Effects},
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
	ownedADTs                map[*types.ADTInfo]bool
	ownedCtors               map[*types.CtorInfo]bool
	ownedEffects             map[*types.EffectInfo]bool
	ownedOps                 map[*types.EffectOp]bool
	ownedClasses             map[*types.ClassInfo]bool
	err                      error
}

func newRemapper(ck *infer.Checker, object *ModuleObject, templateBase int) *remapper {
	r := &remapper{ck: ck, object: object, templateOld: object.TemplateBase, templateNew: templateBase,
		unique: map[int]int{}, effect: map[int]int{}, vars: map[int]int{}, captures: map[types.CaptureVar]types.CaptureVar{}, scopes: map[types.ScopeID]types.ScopeID{}, resumes: map[types.ResumeID]types.ResumeID{}, memo: map[uintptr]reflect.Value{},
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
	// Preserve the defining session's effect order: evidence parameter slices
	// are positional ABI, even though their identities are freshly allocated.
	sort.Slice(effects, func(i, j int) bool { return effects[i].Unique < effects[j].Unique })
	for _, effect := range effects {
		r.effect[effect.Unique] = ck.Sup.NextUnique()
	}
	var nominalIDs []int
	for id := range object.Nominals {
		nominalIDs = append(nominalIDs, id)
	}
	sort.Ints(nominalIDs)
	for _, id := range nominalIDs {
		if _, mapped := r.unique[id]; mapped {
			continue
		}
		name := object.Nominals[id]
		for _, builtin := range []*types.TCon{ck.B.Int, ck.B.Float, ck.B.String, ck.B.Char, ck.B.Bool, ck.B.Unit} {
			if builtin.Name == name {
				r.unique[id] = builtin.Unique
			}
		}
		for _, ty := range ck.TypeNames {
			if con, ok := ty.(*types.TCon); ok && con.Name == name {
				r.unique[id] = con.Unique
			}
		}
		for _, adt := range ck.ADTs {
			if adt.Con.Name == name {
				r.unique[id] = adt.Con.Unique
			}
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
	r.extend(reflect.ValueOf(object))
	return r
}

// extend gives a decoded value the identities it needs: foreign declarations
// keep the installed parameters they describe, and everything the value
// allocates for itself is renamed in a deterministic order. A deferred stage
// section goes through it too, so it cannot disagree with the half of the
// object already installed.
func (r *remapper) extend(v reflect.Value) {
	ck := r.ck
	ids := &remapIDs{vars: map[int]*types.TVar{}, captures: map[types.CaptureVar]bool{}, scopes: map[types.ScopeID]bool{}, resumes: map[types.ResumeID]bool{}}
	collectRemapIDs(v, map[uintptr]bool{}, ids)
	r.alignForeignParams(ids)
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
		r.captures[types.CaptureVar(id)] = ck.Sup.FreshCapture()
	}
	var scopeIDs []int
	for id := range scopes {
		if id != 0 {
			scopeIDs = append(scopeIDs, int(id))
		}
	}
	sort.Ints(scopeIDs)
	for _, id := range scopeIDs {
		r.scopes[types.ScopeID(id)] = ck.Sup.FreshScope()
	}
	var resumeIDs []int
	for id := range resumes {
		if id != 0 {
			resumeIDs = append(resumeIDs, int(id))
		}
	}
	sort.Ints(resumeIDs)
	for _, id := range resumeIDs {
		ck.ResumeGen++
		r.resumes[types.ResumeID(id)] = ck.ResumeGen
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
	vars     map[int]*types.TVar
	captures map[types.CaptureVar]bool
	scopes   map[types.ScopeID]bool
	resumes  map[types.ResumeID]bool
	adts     []*types.ADTInfo
	effects  []*types.EffectInfo
	classes  []*types.ClassInfo
}

func collectRemapIDs(v reflect.Value, seen map[uintptr]bool, ids *remapIDs) {
	if !v.IsValid() || v.Type() == reflect.TypeOf(source.Span{}) {
		return
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
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).Tag.Get("object") != "omit" {
				collectRemapIDs(v.Field(i), seen, ids)
			}
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

func (r *remapper) remap() error {
	v, err := r.rewrite(reflect.ValueOf(r.object))
	if err != nil {
		return err
	}
	r.object = v.Interface().(*ModuleObject)
	return r.err
}

var (
	tVarPtr        = reflect.TypeOf((*types.TVar)(nil))
	tConPtr        = reflect.TypeOf((*types.TCon)(nil))
	adtPtr         = reflect.TypeOf((*types.ADTInfo)(nil))
	ctorPtr        = reflect.TypeOf((*types.CtorInfo)(nil))
	effectPtr      = reflect.TypeOf((*types.EffectInfo)(nil))
	opPtr          = reflect.TypeOf((*types.EffectOp)(nil))
	classPtr       = reflect.TypeOf((*types.ClassInfo)(nil))
	methodPtr      = reflect.TypeOf((*types.MethodInfo)(nil))
	nativePtr      = reflect.TypeOf((*types.NativeInfo)(nil))
	reprPtr        = reflect.TypeOf((*meta.TypeRepr)(nil))
	captureVarType = reflect.TypeOf(types.CaptureVar(0))
	scopeType      = reflect.TypeOf(types.ScopeID(0))
	resumeType     = reflect.TypeOf(types.ResumeID(0))
)

func (r *remapper) rewrite(v reflect.Value) (reflect.Value, error) {
	if !v.IsValid() {
		return v, nil
	}
	if v.Type() == reflect.TypeOf(source.Span{}) {
		return v, nil
	}
	if v.Type() == captureVarType {
		old := types.CaptureVar(v.Int())
		if old == 0 {
			return v, nil
		}
		n := r.captures[old]
		if n == 0 {
			n = r.ck.Sup.FreshCapture()
			r.captures[old] = n
		}
		out := reflect.New(v.Type()).Elem()
		out.SetInt(int64(n))
		return out, nil
	}
	if v.Type() == scopeType {
		old := types.ScopeID(v.Int())
		if old == 0 {
			return v, nil
		}
		n := r.scopes[old]
		if n == 0 {
			n = r.ck.Sup.FreshScope()
			r.scopes[old] = n
		}
		out := reflect.New(v.Type()).Elem()
		out.SetInt(int64(n))
		return out, nil
	}
	if v.Type() == resumeType {
		old := types.ResumeID(v.Int())
		if old == 0 {
			return v, nil
		}
		n := r.resumes[old]
		if n == 0 {
			r.ck.ResumeGen++
			n = r.ck.ResumeGen
			r.resumes[old] = n
		}
		out := reflect.New(v.Type()).Elem()
		out.SetInt(int64(n))
		return out, nil
	}
	if v.Kind() == reflect.Interface {
		if v.IsNil() {
			return reflect.Zero(v.Type()), nil
		}
		x, err := r.rewrite(v.Elem())
		if err != nil {
			return reflect.Value{}, err
		}
		out := reflect.New(v.Type()).Elem()
		out.Set(x)
		return out, nil
	}
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return reflect.Zero(v.Type()), nil
		}
		key := v.Pointer()
		if got := r.memo[key]; got.IsValid() {
			return got, nil
		}
		if replacement := r.intern(v); replacement.IsValid() {
			r.memo[key] = replacement
			return replacement, nil
		}
		out := reflect.New(v.Type().Elem())
		r.memo[key] = out
		x, err := r.rewrite(v.Elem())
		if err != nil {
			return reflect.Value{}, err
		}
		out.Elem().Set(x)
		r.remapPointer(out)
		return out, nil
	}
	switch v.Kind() {
	case reflect.Struct:
		out := reflect.New(v.Type()).Elem()
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).Tag.Get("object") == "omit" {
				continue
			}
			x, err := r.rewrite(v.Field(i))
			if err != nil {
				return reflect.Value{}, err
			}
			out.Field(i).Set(x)
		}
		r.remapStruct(out)
		return out, nil
	case reflect.Slice:
		if v.IsNil() {
			return reflect.Zero(v.Type()), nil
		}
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := 0; i < v.Len(); i++ {
			x, err := r.rewrite(v.Index(i))
			if err != nil {
				return reflect.Value{}, err
			}
			out.Index(i).Set(x)
		}
		return out, nil
	case reflect.Map:
		if v.IsNil() {
			return reflect.Zero(v.Type()), nil
		}
		out := reflect.MakeMapWithSize(v.Type(), v.Len())
		it := v.MapRange()
		for it.Next() {
			k, err := r.rewrite(it.Key())
			if err != nil {
				return reflect.Value{}, err
			}
			x, err := r.rewrite(it.Value())
			if err != nil {
				return reflect.Value{}, err
			}
			out.SetMapIndex(k, x)
		}
		return out, nil
	default:
		return v, nil
	}
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
	if n, ok := r.unique[c.Unique]; ok {
		return n
	}
	builtins := []*types.TCon{r.ck.B.Int, r.ck.B.Float, r.ck.B.String, r.ck.B.Char, r.ck.B.Bool, r.ck.B.Unit}
	for _, b := range builtins {
		if c.Name == b.Name {
			r.unique[c.Unique] = b.Unique
			return b.Unique
		}
	}
	for _, ty := range r.ck.TypeNames {
		if installed, ok := ty.(*types.TCon); ok && installed.Name == c.Name {
			r.unique[c.Unique] = installed.Unique
			return installed.Unique
		}
	}
	for _, adt := range r.ck.ADTs {
		if adt.Con.Name == c.Name {
			r.unique[c.Unique] = adt.Con.Unique
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
			if t.Rigid {
				n = r.ck.Sup.FreshRigid(t.Kind).ID
			} else {
				n = r.ck.Sup.FreshVar(t.Kind).ID
			}
			r.vars[old] = n
		}
		t.ID = n
	case tConPtr:
		c := v.Interface().(*types.TCon)
		if n := r.uniqueForCon(c); n >= 0 {
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
	case *types.CaptureContract:
		for i, id := range x.Effects {
			if n, ok := r.effect[id]; ok {
				x.Effects[i] = n
			} else if r.err == nil {
				r.err = fmt.Errorf("unknown effect identity %d in capture contract", id)
			}
		}
		for i, id := range x.RowEffects {
			if n, ok := r.effect[id]; ok {
				x.RowEffects[i] = n
			} else if r.err == nil {
				r.err = fmt.Errorf("unknown row effect identity %d in capture contract", id)
			}
		}
		for i, id := range x.TypeParams {
			if n, ok := r.vars[id]; ok {
				x.TypeParams[i] = n
			}
		}
	case *types.CaptureFlow:
		for i, id := range x.Effects {
			if n, ok := r.effect[id]; ok {
				x.Effects[i] = n
			} else if r.err == nil {
				r.err = fmt.Errorf("unknown effect identity %d in capture flow", id)
			}
		}
	case *types.CaptureRow:
		for i, id := range x.Effects {
			if n, ok := r.effect[id]; ok {
				x.Effects[i] = n
			} else if r.err == nil {
				r.err = fmt.Errorf("unknown effect identity %d in capture row", id)
			}
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
