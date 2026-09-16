package core

import (
	"fmt"
	"testing"

	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/types"
)

func testFlowChecker() *flowChecker {
	return &flowChecker{shape: &captureAnalyzer{}, defs: map[string]*Def{}, objects: []*flowObject{nil}, objectIDs: map[string]int{}, owners: []*flowOwner{nil}, ownerIDs: map[string]int{}, contexts: map[string]*flowContext{}, errors: map[string]error{}, root: "graph", active: map[int]int{}}
}

// Each local helper calls its predecessor twice; the first closes the cycle
// through the enclosing worker. Scalar computation is erased in this contract.
func flowDiamond(depth int) *types.CaptureFlow {
	id := 0
	node := func(kind, name string, children ...*types.CaptureFlow) *types.CaptureFlow {
		id++
		return &types.CaptureFlow{ID: id, Kind: kind, Name: name, Children: children}
	}
	call := func(name string) *types.CaptureFlow {
		return node("call", "", node("global", name), node("scalar", ""))
	}
	body := call(fmt.Sprintf("h%d", depth))
	for i := depth; i >= 0; i-- {
		previous := "graph"
		if i > 0 {
			previous = fmt.Sprintf("h%d", i-1)
		}
		helper := node("lambda", "token", node("choice", "", call(previous), call(previous)))
		body = node("let", fmt.Sprintf("h%d", i), helper, body)
	}
	return body
}

func TestFlowDiamondScaling(t *testing.T) {
	for _, depth := range []int{2, 4, 6, 12} {
		f := testFlowChecker()
		body := flowDiamond(depth)
		f.defs["graph"] = &Def{Name: "graph", Params: []string{"token"}, CaptureContract: &types.CaptureContract{Params: []string{"token"}, Body: body}}
		for pass := 0; ; pass++ {
			f.generation++
			f.changed = false
			f.callDef("graph", []flowValue{{}}, nil, emptyFlowEnv(), rootFlowSite, nil)
			if !f.changed {
				break
			}
			if pass > 30 {
				t.Fatal("did not converge")
			}
		}
		t.Logf("depth %d: contexts=%d objects=%d", depth, len(f.contexts), len(f.objects)-1)
		if len(f.contexts) != depth+2 || len(f.objects)-1 > 2*(depth+2) {
			t.Fatal("graph grew with path count")
		}
		assertFlowEvaluations(t, f)
	}
}

func TestFlowCapabilitySharing(t *testing.T) {
	f := testFlowChecker()
	f.generation = 1
	body := &types.CaptureFlow{Kind: "var", Name: "x"}
	call := func(v flowValue, site string) flowValue {
		env := emptyFlowEnv()
		env.values["x"] = v
		return f.invoke("identity", "identity", body, env, flowSite{phase: site}, nil, 0)
	}
	call(flowValue{caps: []int{1}}, "left")
	call(flowValue{caps: []int{1}}, "right")
	if len(f.contexts) != 1 {
		t.Fatal("incoming paths split an equivalent state")
	}
	call(flowValue{caps: []int{2}}, "right")
	call(flowValue{unknown: true}, "right")
	call(flowValue{}, "right")
	if len(f.contexts) != 4 {
		t.Fatal("distinct owners, unknown, and empty states must remain distinct")
	}
	assertFlowEvaluations(t, f)
}

func assertFlowEvaluations(t *testing.T, f *flowChecker) {
	t.Helper()
	for id, generations := range f.statistics().evaluations {
		for generation, count := range generations {
			if count != 1 {
				t.Fatalf("%s generation %d evaluated %d times", id, generation, count)
			}
		}
	}
}

func TestFlowFingerprintCyclesAliasesAndGrowth(t *testing.T) {
	f := testFlowChecker()
	f.generation = 1
	body := &types.CaptureFlow{ID: 1, Kind: "var", Name: "self"}
	lambda := &types.CaptureFlow{ID: 2, Kind: "lambda", Name: "argument", Children: []*types.CaptureFlow{body}}
	makeCycle := func(site string, unrelated int) int {
		env := emptyFlowEnv()
		env.values["argument"] = flowValue{caps: []int{unrelated}}
		env.values["unrelated"] = flowValue{caps: []int{unrelated}}
		ref := f.alloc(site, flowObject{kind: "lambda", def: "cycle", code: lambda, env: env})
		env.values["self"] = flowValue{refs: []int{ref}}
		f.mergeEnv(&f.objects[ref].env, env)
		return ref
	}
	a, b := makeCycle("a", 10), makeCycle("b", 20)
	envA, envB := emptyFlowEnv(), emptyFlowEnv()
	envA.values["x"] = flowValue{refs: []int{a}}
	envB.values["x"] = flowValue{refs: []int{b}}
	if f.flowFingerprint(envA) != f.flowFingerprint(envB) {
		t.Fatal("cyclic closures retain invocation or unrelated bindings")
	}
	envA.values["y"] = flowValue{refs: []int{a}}
	envB.values["y"] = flowValue{refs: []int{a}}
	if f.flowFingerprint(envA) == f.flowFingerprint(envB) {
		t.Fatal("fingerprint erased alias relationships")
	}
	delete(envA.values, "y")
	delete(envB.values, "y")
	identity := &types.CaptureFlow{ID: 3, Kind: "var", Name: "x"}
	f.invoke("identity", "identity", identity, envA, flowSite{phase: "first"}, nil, 0)
	before := f.contextFingerprint(f.contexts["c1"])
	grown := f.objects[a].env.clone()
	grown.values["self"] = joinFlow(grown.values["self"], flowValue{caps: []int{9}})
	f.mergeEnv(&f.objects[a].env, grown)
	if before == f.contextFingerprint(f.contexts["c1"]) {
		t.Fatal("heap growth left a stale fingerprint")
	}
	f.invoke("identity", "identity", identity, envA, flowSite{phase: "second"}, nil, 0)
	if len(f.contexts) != 1 {
		t.Fatal("heap growth lost the stable context")
	}
	f.invoke("identity", "identity", identity, envB, flowSite{phase: "third"}, nil, 0)
	if len(f.contexts) != 2 {
		t.Fatal("old heap state reused a changed context")
	}
	assertFlowEvaluations(t, f)
}

func TestFlowRowsTypesAndDynamicBoundaries(t *testing.T) {
	f := testFlowChecker()
	f.generation = 1
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	body := &types.CaptureFlow{ID: 1, Kind: "var", Name: "x", Row: &types.CaptureRow{From: 7, Effects: []int{8}}}
	env := emptyFlowEnv()
	env.values["x"] = flowValue{unknown: true}
	invoke := func() { f.invoke("same", "same", body, env, flowSite{phase: "call"}, nil, 0) }
	invoke()
	env.rows[7] = flowRow{unknown: true}
	invoke()
	env.rows[7] = flowRow{evidence: map[int][]int{8: {1}}}
	invoke()
	env.rows[7] = flowRow{evidence: map[int][]int{8: {2}}}
	invoke()
	env.types[1] = b.Int
	invoke()
	env.types[1] = &types.TVar{ID: 99, Rigid: true}
	invoke()
	f.active[1] = 1
	invoke()
	f.synchronous = []synchronousFlow{{phase: "acquisition"}}
	invoke()
	f.synchronous = []synchronousFlow{{phase: "release"}}
	invoke()
	f.pulls = []flowPull{{owner: 2}}
	invoke()
	if len(f.contexts) != 10 {
		t.Fatalf("row/type/boundary states collapsed: %d contexts", len(f.contexts))
	}
	assertFlowEvaluations(t, f)
}

func TestFlowLateInputGrowthWaitsForNextGeneration(t *testing.T) {
	f := testFlowChecker()
	f.generation = 1
	body := &types.CaptureFlow{ID: 1, Kind: "var", Name: "x"}
	env := emptyFlowEnv()
	f.invoke("identity", "identity", body, env, rootFlowSite, nil, 0)
	env.values["x"] = flowValue{caps: []int{1}}
	got := f.invoke("identity", "identity", body, env, rootFlowSite, nil, 0)
	if len(got.caps) != 0 || !f.changed {
		t.Fatal("late growth must schedule, not reevaluate, the context")
	}
	f.generation++
	got = f.invoke("identity", "identity", body, env, rootFlowSite, nil, 0)
	if len(got.caps) != 1 {
		t.Fatal("late input was lost")
	}
	assertFlowEvaluations(t, f)
}

func TestFlowCachedObligationsReplay(t *testing.T) {
	for _, busy := range []bool{false, true} {
		t.Run(fmt.Sprint(busy), func(t *testing.T) {
			f := testFlowChecker()
			f.generation = 1
			owner := f.owner(&types.CaptureFlow{ID: 1, Kind: "iterator"}, emptyFlowEnv(), "outside", nil)
			f.active[owner] = 1
			f.synchronous = []synchronousFlow{{phase: "release"}}
			body := &types.CaptureFlow{ID: 2, Kind: "scalar"}
			f.invoke("helper", "helper", body, emptyFlowEnv(), flowSite{phase: "left"}, nil, 0)
			c := f.contexts["c1"]
			c.accesses = []int{owner}
			c.suspensions = []int{0}
			c.busy = busy
			f.location = source.Span{File: source.NewFile("caller.fango", []byte("call")), End: 4}
			f.invoke("helper", "helper", body, emptyFlowEnv(), flowSite{phase: "right"}, nil, 0)
			if len(f.errors) != 2 {
				t.Fatalf("cached access/suspension obligations were lost: %v", f.errors)
			}
			for _, err := range f.errors {
				switch e := err.(type) {
				case CursorAccessError:
					if e.Span != f.location {
						t.Fatal("cached access used the old diagnostic origin")
					}
				case SuspensionError:
					if e.Span != f.location {
						t.Fatal("cached suspension used the old diagnostic origin")
					}
				}
			}
			assertFlowEvaluations(t, f)
		})
	}
}

func TestFlowDiamondEvidenceScaling(t *testing.T) {
	for _, concrete := range []bool{false, true} {
		for _, depth := range []int{3, 8, 12} {
			t.Run(fmt.Sprintf("concrete=%t/depth=%d", concrete, depth), func(t *testing.T) {
				f := testFlowChecker()
				body := flowDiamond(depth)
				var addEvidence func(*types.CaptureFlow)
				addEvidence = func(n *types.CaptureFlow) {
					if n.Kind == "call" {
						n.Row = &types.CaptureRow{Effects: []int{1}}
					}
					for _, c := range n.Children {
						addEvidence(c)
					}
				}
				addEvidence(body)
				f.defs["graph"] = &Def{Name: "graph", Params: []string{"token"}, CaptureContract: &types.CaptureContract{Params: []string{"token"}, Effects: []int{1}, Body: body}}
				caller := emptyFlowEnv()
				caller.evidence[1] = []int{0}
				scalar := &types.CaptureFlow{ID: 10000, Kind: "scalar"}
				call := &types.CaptureFlow{ID: 10001, Kind: "call", Row: &types.CaptureRow{Effects: []int{1}}, Children: []*types.CaptureFlow{{ID: 10002, Kind: "global", Name: "graph"}, scalar}}
				handler := &types.CaptureFlow{ID: 10003, Kind: "handle", Effects: []int{1}, Children: []*types.CaptureFlow{scalar, call, nil}}
				for pass := 0; ; pass++ {
					f.generation++
					f.changed = false
					if concrete {
						f.invoke("run", "run", handler, emptyFlowEnv(), rootFlowSite, nil, 0)
					} else {
						f.callDef("graph", []flowValue{{}}, nil, caller, rootFlowSite, nil)
					}
					if !f.changed {
						break
					}
					if pass == 30 {
						t.Fatal("did not converge")
					}
				}
				stats := f.statistics()
				if stats.contexts > depth+3 || stats.objects > 2*(depth+3) {
					t.Fatalf("evidence graph grew by path count: %+v", stats)
				}
				if concrete && stats.owners != 1 {
					t.Fatalf("handler owner was duplicated: %d", stats.owners)
				}
				assertFlowEvaluations(t, f)
			})
		}
	}
}

func TestFlowRecursiveAllocationAncestry(t *testing.T) {
	f := testFlowChecker()
	f.generation = 1
	c := &flowContext{id: "activation", recursive: true}
	f.contexts[c.id] = c
	before := f.alloc("before", flowObject{kind: "ctor"})
	f.calls = []*flowContext{c}
	inside := f.alloc("inside", flowObject{kind: "ctor"})
	owner := f.owner(&types.CaptureFlow{ID: 1, Kind: "scope", Scoped: true}, emptyFlowEnv(), c.id, nil)
	previous, next := emptyFlowEnv(), emptyFlowEnv()
	next.values["x"] = flowValue{refs: []int{before}}
	if f.recursiveInputs(c.id, previous, next) {
		t.Fatal("folded a pre-existing value")
	}
	next.values["x"] = flowValue{refs: []int{inside}}
	if !f.recursiveInputs(c.id, previous, next) {
		t.Fatal("failed to fold a growing recursive input")
	}
	nested := f.owner(&types.CaptureFlow{ID: 2, Kind: "scope", Scoped: true}, emptyFlowEnv(), c.id, []int{owner})
	f.store(flowValue{caps: []int{owner}}, owner, "same")
	f.store(flowValue{caps: []int{owner}}, nested, "nested")
	if len(f.errors) != 2 {
		t.Fatalf("folded owner equality or ancestry proved retention safe: %v", f.errors)
	}
}

func TestFlowRecursiveTypeWideningPreservesResources(t *testing.T) {
	f := testFlowChecker()
	f.generation = 1
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	f.shape.b = b
	body := &types.CaptureFlow{ID: 1, Kind: "var", Name: "x", Type: &types.TVar{ID: 1, Rigid: true}}
	env := emptyFlowEnv()
	env.values["x"] = flowValue{caps: []int{7}}
	env.types[1] = b.Int
	if got := f.invoke("identity", "identity", body, env, rootFlowSite, nil, 0); len(got.caps) != 0 {
		t.Fatal("scalar retained captures")
	}
	env.types[1] = &types.TFun{Arg: b.Unit, Ret: b.Unit}
	f.invoke("identity", "identity", body, env, rootFlowSite, nil, 0)
	f.generation++
	if got := f.invoke("identity", "identity", body, env, rootFlowSite, nil, 0); len(got.caps) != 1 {
		t.Fatal("earlier scalar instantiation erased a later resource")
	}
	if len(f.contexts["c1"].env.types) != 0 {
		t.Fatal("disagreeing substitution was not widened")
	}
	assertFlowEvaluations(t, f)
}

func TestFlowCachedAllocationAncestry(t *testing.T) {
	f := testFlowChecker()
	f.generation = 1
	scalar := &types.CaptureFlow{ID: 1, Kind: "scalar"}
	body := &types.CaptureFlow{ID: 2, Kind: "scope", Children: []*types.CaptureFlow{scalar, scalar, scalar}}
	f.invoke("factory", "factory", body, emptyFlowEnv(), flowSite{phase: "outside"}, nil, 0)
	if f.recursiveOwner(1) {
		t.Fatal("nonrecursive owner was folded")
	}
	enclosing := &flowContext{id: "enclosing", recursive: true}
	f.contexts[enclosing.id] = enclosing
	f.calls = []*flowContext{enclosing}
	f.invoke("factory", "factory", body, emptyFlowEnv(), flowSite{phase: "inside"}, nil, 0)
	if !f.recursiveOwner(1) {
		t.Fatal("cached allocation lost its recursive invocation ancestry")
	}
}
