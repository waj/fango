package core

import (
	"strings"
	"testing"

	"github.com/waj/fango/internal/types"
)

func iteratorProofFixture() (*Prog, *types.Builtins, *CoroutineScope, *CoroutineAdvance) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	control := types.Control{Transport: types.Machine}
	cursor := &types.TCon{Unique: sup.NextUnique(), Name: types.CoroutineTypeName, Args: []types.Type{b.Int, b.Unit, b.Unit, b.Unit}}
	pause := &types.TFun{Arg: b.Int, Ret: b.Unit, Control: control}
	producer := &types.TFun{Arg: pause, Ret: &types.TFun{Arg: b.Unit, Ret: b.Unit, Control: control}}
	consumer := &types.TFun{Arg: cursor, Ret: b.Unit, Control: control}
	a, z := sup.FreshRigid(types.General), sup.FreshRigid(types.General)
	con := &types.TCon{Unique: sup.NextUnique(), Name: types.CoroutineStepName, Args: []types.Type{a, z}}
	result := &types.ADTInfo{Con: con, Params: []*types.TVar{a, z}, Ctors: []*types.CtorInfo{
		{Name: "Runtime.Coroutine.Suspended", Index: 0, Fields: []types.Type{a}, Result: con},
		{Name: "Runtime.Coroutine.Finished", Index: 1, Fields: []types.Type{z}, Result: con},
		{Name: "Runtime.Coroutine.Closed", Index: 2, Result: con}}}
	step := &types.TCon{Unique: con.Unique, Name: con.Name, Args: []types.Type{b.Int, b.Unit}}
	owner := sup.FreshScope()
	suspension := EffectInstance{Unique: sup.NextUnique(), Name: types.CoroutineSuspensionName, Captures: types.ScopeCapture(owner), Control: control}
	drive := EffectInstance{Unique: sup.NextUnique(), Name: types.CoroutineDriveName, Captures: types.ScopeCapture(owner), Control: control}
	scope := &CoroutineScope{Scope: owner, Yield: suspension, Traversal: drive, Producer: &VarRef{Name: "producer", Local: true, Ty: producer}, Consumer: &VarRef{Name: "consumer", Local: true, Ty: consumer}, CursorTy: cursor, Ty: b.Unit}
	each := &CoroutineAdvance{Row: &RowArgument{}, Access: types.ExclusiveAdvance, Cursor: &VarRef{Name: "cursor", Local: true, Ty: cursor}, Reply: &VarRef{Name: "reply", Local: true, Ty: b.Unit}, Result: result, Ty: step}
	p := &Prog{ADTs: []*types.ADTInfo{result}, Intrinsics: map[string]bool{types.CoroutineWithName: true, types.CoroutineAdvanceName: true}, Effects: []*types.EffectInfo{{Unique: suspension.Unique, Name: suspension.Name, Suspension: true}, {Unique: drive.Unique, Name: drive.Name, Suspension: true}}, Defs: []Def{
		{Name: types.CoroutineWithName, Type: &types.TFun{Arg: producer, Ret: &types.TFun{Arg: consumer, Ret: b.Unit}}, Params: []string{"producer", "consumer"}, ParamCaptures: []types.CaptureVar{sup.FreshCapture(), sup.FreshCapture()}, Body: scope},
		{Name: types.CoroutineAdvanceName, Type: &types.TFun{Arg: cursor, Ret: &types.TFun{Arg: b.Unit, Ret: step, Control: control}}, Control: control, Params: []string{"cursor", "reply"}, ParamCaptures: []types.CaptureVar{sup.FreshCapture(), sup.FreshCapture()}, Body: each}}}
	return p, b, scope, each
}

func TestIteratorCoreRequiresOwnershipAndAccessProofs(t *testing.T) {
	for _, test := range []struct {
		name   string
		damage func(*Prog, *CoroutineScope, *CoroutineAdvance)
		want   string
	}{
		{"valid", func(*Prog, *CoroutineScope, *CoroutineAdvance) {}, ""},
		{"missing owner", func(_ *Prog, s *CoroutineScope, _ *CoroutineAdvance) { s.Scope = 0 }, "invalid or reused coroutine owner"},
		{"missing access", func(_ *Prog, _ *CoroutineScope, e *CoroutineAdvance) { e.Access = 0 }, "lacks exclusive access proof"},
		{"unknown access", func(_ *Prog, _ *CoroutineScope, e *CoroutineAdvance) { e.Access = 99 }, "lacks exclusive access proof"},
		{"stale scope contract", func(p *Prog, _ *CoroutineScope, _ *CoroutineAdvance) { p.Defs[0].CaptureContract.Body.Scope++ }, "capture contract is stale"},
		{"stale access contract", func(p *Prog, _ *CoroutineScope, _ *CoroutineAdvance) { p.Defs[1].CaptureContract.Body.Access = 0 }, "capture contract is stale"},
		{"missing contract", func(p *Prog, _ *CoroutineScope, _ *CoroutineAdvance) { p.Defs[1].CaptureContract = nil }, "capture contract is stale"},
		{"undeclared intrinsic", func(p *Prog, _ *CoroutineScope, _ *CoroutineAdvance) {
			delete(p.Intrinsics, types.CoroutineAdvanceName)
		}, "outside its declared"},
	} {
		t.Run(test.name, func(t *testing.T) {
			p, b, scope, each := iteratorProofFixture()
			if errs := InferCaptures(p, b); len(errs) != 0 {
				t.Fatal(errs)
			}
			test.damage(p, scope, each)
			var messages []string
			for _, err := range Lint(p, b) {
				messages = append(messages, err.Error())
			}
			got := strings.Join(messages, "\n")
			if test.want == "" && got != "" || test.want != "" && !strings.Contains(got, test.want) {
				t.Fatalf("errors = %s, want %q", got, test.want)
			}
		})
	}
}

// Cyclic callbacks and joined aliases are expressed directly at the contract
// boundary. This verifies the access proof independently of source spelling,
// parser restrictions, or which library worker performs the advancement.
func TestCursorAccessContractsSubstituteAliasesAndRecursiveHelpers(t *testing.T) {
	for _, mode := range []string{"same", "distinct", "possible alias"} {
		t.Run(mode, func(t *testing.T) {
			sup := &types.Supply{}
			b := types.NewBuiltins(sup)
			cursorTy := &types.TCon{Unique: sup.NextUnique(), Name: types.CoroutineTypeName}
			fnTy := &types.TFun{Arg: b.Unit, Ret: b.Unit, Control: types.Control{Transport: types.Machine}}
			scalar := &types.CaptureFlow{ID: 1, Kind: "scalar", Type: b.Unit}
			action := &types.CaptureFlow{ID: 2, Kind: "lambda", Name: "ignored", Type: fnTy, Children: []*types.CaptureFlow{scalar}}
			cursor := &types.CaptureFlow{ID: 3, Kind: "var", Name: "cursor", Type: cursorTy}
			read := &types.CaptureFlow{ID: 4, Kind: "advance", Access: types.ExclusiveAdvance, Type: b.Unit, Children: []*types.CaptureFlow{cursor, scalar}}
			global := &types.CaptureFlow{ID: 5, Kind: "global", Name: "helper", Type: fnTy}
			call := &types.CaptureFlow{ID: 6, Kind: "call", Type: b.Unit, Children: []*types.CaptureFlow{global, cursor}}
			body := &types.CaptureFlow{ID: 7, Kind: "branch", Type: b.Unit, Children: []*types.CaptureFlow{scalar, read, call}}
			def := Def{Name: "helper", Control: types.Control{Transport: types.Machine}, Params: []string{"cursor"}, CaptureContract: &types.CaptureContract{Params: []string{"cursor"}, Body: body}}
			p := &Prog{Defs: []Def{def}}
			shape := newCaptureAnalyzer(p, b)
			f := &flowChecker{shape: shape, defs: shape.defs, objects: []*flowObject{nil}, objectIDs: map[string]int{},
				owners: []*flowOwner{nil}, ownerIDs: map[string]int{}, contexts: map[string]*flowContext{}, errors: map[string]error{}, active: map[int]int{}, root: "test"}
			env := flowEnv{values: map[string]flowValue{}, evidence: map[int][]int{}, types: map[int]types.Type{}}
			ownerA := f.owner(&types.CaptureFlow{ID: 10, Kind: "coroutine", Scoped: true, Scope: sup.FreshScope()}, env, "outer", nil)
			ownerB := f.owner(&types.CaptureFlow{ID: 11, Kind: "coroutine", Scoped: true, Scope: sup.FreshScope()}, env, "inner", []int{ownerA})
			empty := f.alloc("empty producer", flowObject{kind: "lambda", code: &types.CaptureFlow{ID: 20, Kind: "lambda", Name: "pause", Type: &types.TFun{Arg: fnTy, Ret: fnTy}, Children: []*types.CaptureFlow{action}}, env: env})
			a := f.alloc("cursor A", flowObject{kind: "coroutine", owner: ownerA, fields: []flowValue{{}}})
			other := f.alloc("cursor B", flowObject{kind: "coroutine", owner: ownerB, fields: []flowValue{{refs: []int{empty}}}})
			actual := flowValue{refs: []int{a}, caps: []int{ownerA}}
			if mode == "distinct" {
				actual = flowValue{refs: []int{other}, caps: []int{ownerB}}
			} else if mode == "possible alias" {
				actual = joinFlow(actual, flowValue{refs: []int{other}, caps: []int{ownerB}})
			}
			producerEnv := env.clone()
			producerEnv.values["cursor"] = actual
			producer := f.alloc("producer A", flowObject{kind: "lambda", code: &types.CaptureFlow{ID: 21, Kind: "lambda", Name: "pause", Type: &types.TFun{Arg: fnTy, Ret: fnTy}, Children: []*types.CaptureFlow{{ID: 12, Kind: "lambda", Name: "ignored", Type: fnTy, Children: []*types.CaptureFlow{call}}}}, env: producerEnv})
			f.objects[a].fields[0] = flowValue{refs: []int{producer}}
			for {
				f.generation++
				f.changed = false
				f.advance(flowValue{refs: []int{a}}, flowValue{}, env, rootFlowSite, []int{ownerA, ownerB})
				if !f.changed {
					break
				}
			}
			if mode == "distinct" && len(f.errors) != 0 || mode != "distinct" && len(f.errors) == 0 {
				t.Fatalf("access conflicts = %v", f.errors)
			}
			if f.active[ownerA] != 0 || f.active[ownerB] != 0 {
				t.Fatal("advancement retained its borrow after completion")
			}
		})
	}
}
