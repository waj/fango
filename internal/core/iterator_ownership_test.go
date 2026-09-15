package core

import (
	"strings"
	"testing"

	"github.com/waj/fango/internal/types"
)

func iteratorProofFixture() (*Prog, *types.Builtins, *IteratorScope, *IteratorNext) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	cursor := &types.TCon{Unique: sup.NextUnique(), Name: types.IteratorTypeName, Args: []types.Type{b.Int, b.Unit}}
	producer := &types.TFun{Arg: b.Unit, Ret: b.Unit, Control: types.Control{Transport: types.Machine}}
	consumer := &types.TFun{Arg: cursor, Ret: b.Unit}
	elem := sup.FreshRigid(types.General)
	con := &types.TCon{Unique: sup.NextUnique(), Name: "Maybe.Maybe", Args: []types.Type{elem}}
	result := &types.ADTInfo{Con: con, Params: []*types.TVar{elem}, Ctors: []*types.CtorInfo{
		{Name: "Maybe.Nothing", Index: 0, Result: con},
		{Name: "Maybe.Just", Index: 1, Fields: []types.Type{elem}, Result: con},
	}}
	maybe := &types.TCon{Unique: con.Unique, Name: con.Name, Args: []types.Type{b.Int}}
	scope := &IteratorScope{Scope: sup.FreshScope(), Producer: &VarRef{Name: "producer", Local: true, Ty: producer},
		Consumer: &VarRef{Name: "consumer", Local: true, Ty: consumer}, CursorTy: cursor, Ty: b.Unit}
	each := &IteratorNext{Access: types.ExclusiveAdvance,
		Cursor: &VarRef{Name: "cursor", Local: true, Ty: cursor}, Result: result, Ty: maybe}
	p := &Prog{ADTs: []*types.ADTInfo{result}, Intrinsics: map[string]bool{types.StreamWithProducerName: true, types.IteratorNextName: true}, Defs: []Def{
		{Name: types.StreamWithProducerName, Type: &types.TFun{Arg: producer, Ret: &types.TFun{Arg: consumer, Ret: b.Unit}},
			Params: []string{"producer", "consumer"}, ParamCaptures: []types.CaptureVar{sup.FreshCapture(), sup.FreshCapture()}, Body: scope},
		{Name: types.IteratorNextName, Type: &types.TFun{Arg: cursor, Ret: maybe, Control: types.Control{Transport: types.Machine}}, Control: types.Control{Transport: types.Machine},
			Params: []string{"cursor"}, ParamCaptures: []types.CaptureVar{sup.FreshCapture()}, Body: each},
	}}
	return p, b, scope, each
}

func TestIteratorCoreRequiresOwnershipAndAccessProofs(t *testing.T) {
	for _, test := range []struct {
		name   string
		damage func(*Prog, *IteratorScope, *IteratorNext)
		want   string
	}{
		{"valid", func(*Prog, *IteratorScope, *IteratorNext) {}, ""},
		{"missing owner", func(_ *Prog, s *IteratorScope, _ *IteratorNext) { s.Scope = 0 }, "invalid or reused scope identity"},
		{"missing access", func(_ *Prog, _ *IteratorScope, e *IteratorNext) { e.Access = 0 }, "lacks exclusive access proof"},
		{"unknown access", func(_ *Prog, _ *IteratorScope, e *IteratorNext) { e.Access = 99 }, "lacks exclusive access proof"},
		{"stale scope contract", func(p *Prog, _ *IteratorScope, _ *IteratorNext) { p.Defs[0].CaptureContract.Body.Scope++ }, "capture contract is stale"},
		{"stale access contract", func(p *Prog, _ *IteratorScope, _ *IteratorNext) { p.Defs[1].CaptureContract.Body.Access = 0 }, "capture contract is stale"},
		{"missing contract", func(p *Prog, _ *IteratorScope, _ *IteratorNext) { p.Defs[1].CaptureContract = nil }, "capture contract is stale"},
		{"undeclared intrinsic", func(p *Prog, _ *IteratorScope, _ *IteratorNext) { delete(p.Intrinsics, types.IteratorNextName) }, "outside the declared"},
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
			cursorTy := &types.TCon{Unique: sup.NextUnique(), Name: types.IteratorTypeName}
			fnTy := &types.TFun{Arg: b.Unit, Ret: b.Unit}
			scalar := &types.CaptureFlow{ID: 1, Kind: "scalar", Type: b.Unit}
			action := &types.CaptureFlow{ID: 2, Kind: "lambda", Name: "ignored", Type: fnTy, Children: []*types.CaptureFlow{scalar}}
			cursor := &types.CaptureFlow{ID: 3, Kind: "var", Name: "cursor", Type: cursorTy}
			read := &types.CaptureFlow{ID: 4, Kind: "next", Access: types.ExclusiveAdvance, Type: b.Unit, Children: []*types.CaptureFlow{cursor}}
			global := &types.CaptureFlow{ID: 5, Kind: "global", Name: "helper", Type: fnTy}
			call := &types.CaptureFlow{ID: 6, Kind: "call", Type: b.Unit, Children: []*types.CaptureFlow{global, cursor}}
			body := &types.CaptureFlow{ID: 7, Kind: "branch", Type: b.Unit, Children: []*types.CaptureFlow{scalar, read, call}}
			def := Def{Name: "helper", Params: []string{"cursor"}, CaptureContract: &types.CaptureContract{Params: []string{"cursor"}, Body: body}}
			p := &Prog{Defs: []Def{def}}
			shape := newCaptureAnalyzer(p, b)
			f := &flowChecker{shape: shape, defs: shape.defs, objects: []*flowObject{nil}, objectIDs: map[string]int{},
				owners: []*flowOwner{nil}, ownerIDs: map[string]int{}, contexts: map[string]*flowContext{}, errors: map[string]error{}, active: map[int]int{}, root: "test"}
			env := flowEnv{values: map[string]flowValue{}, evidence: map[int][]int{}, types: map[int]types.Type{}}
			ownerA := f.owner(&types.CaptureFlow{ID: 10, Kind: "iterator", Scoped: true, Scope: sup.FreshScope()}, env, "outer", nil)
			ownerB := f.owner(&types.CaptureFlow{ID: 11, Kind: "iterator", Scoped: true, Scope: sup.FreshScope()}, env, "inner", []int{ownerA})
			empty := f.alloc("empty producer", flowObject{kind: "lambda", code: action, env: env})
			a := f.alloc("cursor A", flowObject{kind: "cursor", owner: ownerA, fields: []flowValue{{}}})
			other := f.alloc("cursor B", flowObject{kind: "cursor", owner: ownerB, fields: []flowValue{{refs: []int{empty}}}})
			actual := flowValue{refs: []int{a}, caps: []int{ownerA}}
			if mode == "distinct" {
				actual = flowValue{refs: []int{other}, caps: []int{ownerB}}
			} else if mode == "possible alias" {
				actual = joinFlow(actual, flowValue{refs: []int{other}, caps: []int{ownerB}})
			}
			producerEnv := env.clone()
			producerEnv.values["cursor"] = actual
			producer := f.alloc("producer A", flowObject{kind: "lambda", code: &types.CaptureFlow{ID: 12, Kind: "lambda", Name: "ignored", Type: fnTy, Children: []*types.CaptureFlow{call}}, env: producerEnv})
			f.objects[a].fields[0] = flowValue{refs: []int{producer}}
			for {
				f.changed = false
				f.advance(flowValue{refs: []int{a}}, env, "root", []int{ownerA, ownerB})
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
