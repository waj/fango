package machine

import (
	"strings"
	"testing"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/core/coretest"
	"github.com/waj/fango/internal/types"
)

func TestLowerSplitsSuspensionsAndComputesMinimalFrame(t *testing.T) {
	_, b := testBuiltins()
	x := &core.VarRef{Name: "x", Local: true, Ty: b.Int}
	body := &core.Let{Name: "x", Rhs: suspend(b, intLit(b, 1)), Ty: b.Int,
		Body: &core.Let{Name: "y", Rhs: suspend(b, x), Ty: b.Int, Body: x}}
	p := &core.Prog{Defs: []core.Def{{Name: "main", Type: b.Int,
		Control: types.Control{Transport: types.Machine}, Body: body}}}

	mp, errs := Lower(p, b)
	if len(errs) != 0 {
		t.Fatalf("Lower rejected checked machine Core:\n%s", errorsText(errs))
	}
	if len(mp.Workers) != 1 {
		t.Fatalf("got %d workers, want 1", len(mp.Workers))
	}
	w := mp.Workers[0]
	if w.Entry != 3 || len(w.Blocks) != 4 {
		t.Fatalf("entry/blocks = %d/%d, want 3/4", w.Entry, len(w.Blocks))
	}
	if got := localNames(w.Frame); len(got) != 1 || got[0] != "x" {
		t.Fatalf("frame locals = %v, want [x]", got)
	}
	first, ok := w.Blocks[w.Entry].Term.(*Suspend)
	if !ok || first.Bind.Name != "x" || first.Next != 2 {
		t.Fatalf("entry terminator = %#v, want suspension defining x -> block 2", w.Blocks[w.Entry].Term)
	}
	second, ok := w.Blocks[2].Term.(*Suspend)
	if !ok || second.Bind.Name != "y" || second.Next != 1 {
		t.Fatalf("second terminator = %#v, want suspension defining y -> block 1", w.Blocks[2].Term)
	}
	if got := w.Blocks[2].LiveOut; len(got) != 1 || got[0] != "x" {
		t.Fatalf("second suspension LiveOut = %v, want [x]", got)
	}
}

func TestLowerSharesBranchContinuation(t *testing.T) {
	_, b := testBuiltins()
	body := &core.Let{Name: "x", Rhs: &core.If{
		Cond: &core.BoolLit{Val: true, Ty: b.Bool},
		Then: suspend(b, intLit(b, 1)), Else: suspend(b, intLit(b, 2)), Ty: b.Int,
	}, Body: &core.VarRef{Name: "x", Local: true, Ty: b.Int}, Ty: b.Int}
	p := &core.Prog{Defs: []core.Def{{Name: "main", Type: b.Int,
		Control: types.Control{Transport: types.Machine}, Body: body}}}

	mp, errs := Lower(p, b)
	if len(errs) != 0 {
		t.Fatalf("Lower: %s", errorsText(errs))
	}
	w := mp.Workers[0]
	branch, ok := w.Blocks[w.Entry].Term.(*Branch)
	if !ok {
		t.Fatalf("entry = %T, want Branch", w.Blocks[w.Entry].Term)
	}
	thenSuspend := w.Blocks[branch.Then].Term.(*Suspend)
	elseSuspend := w.Blocks[branch.Else].Term.(*Suspend)
	if thenSuspend.Next != elseSuspend.Next {
		t.Fatalf("branch continuations differ: %d and %d", thenSuspend.Next, elseSuspend.Next)
	}
}

func TestLowerClosesMachineIslandThroughPolymorphicWorker(t *testing.T) {
	sup, b := testBuiltins()
	poly := types.Control{Polymorphic: true}
	helperTy := &types.TFun{Arg: b.Int, Eff: types.Row{}, Ret: b.Int, Control: poly}
	helper := core.Def{Name: "helper", Type: helperTy, Params: []string{"n"},
		ParamCaptures: []types.CaptureVar{sup.FreshCapture()}, Control: poly,
		Body: suspend(b, &core.VarRef{Name: "n", Local: true, Ty: b.Int})}
	call := &core.App{CalleeKind: core.Worker, Callee: &core.VarRef{Name: "helper", Ty: helperTy},
		Args: []core.Expr{intLit(b, 7)}, Ty: b.Int, Control: poly}
	main := core.Def{Name: "main", Type: b.Int, Control: types.Control{Transport: types.Machine}, Body: call}
	p := &core.Prog{Defs: []core.Def{helper, main}}

	mp, errs := Lower(p, b)
	if len(errs) != 0 {
		t.Fatalf("Lower: %s", errorsText(errs))
	}
	if len(mp.Workers) != 2 || mp.Workers[0].Name != "helper" || mp.Workers[1].Name != "main" {
		t.Fatalf("selected workers = %v, want [helper main]", workerNames(mp))
	}
	mainWorker := mp.Workers[1]
	callTerm, ok := mainWorker.Blocks[mainWorker.Entry].Term.(*Call)
	if !ok || callTerm.Callee != "helper" || !callTerm.Tail {
		t.Fatalf("main entry = %#v, want tail call to helper", mainWorker.Blocks[mainWorker.Entry].Term)
	}
}

func TestLowerLeavesDirectProgramOutsideMachineIR(t *testing.T) {
	_, b := testBuiltins()
	p := &core.Prog{Defs: []core.Def{{Name: "main", Type: b.Int, Body: intLit(b, 1)}}}
	mp, errs := Lower(p, b)
	if len(errs) != 0 {
		t.Fatalf("Lower: %s", errorsText(errs))
	}
	if len(mp.Workers) != 0 {
		t.Fatalf("lowered %d direct workers", len(mp.Workers))
	}
}

func TestLowerEmitsFactoryClosuresIndependentlyOfMachineConsumers(t *testing.T) {
	sup, b := testBuiltins()
	callback := &types.TFun{Arg: b.Int, Ret: b.Int, Control: types.Control{Polymorphic: true}}
	factory := core.Def{Name: "Library.factory", Owner: "Library", Type: &types.TFun{Arg: b.Int, Ret: callback},
		Params: []string{"value"}, ParamCaptures: []types.CaptureVar{sup.FreshCapture()},
		Body: &core.Lambda{Param: "ignored", ParamCapture: sup.FreshCapture(), Ty: callback,
			Body: &core.VarRef{Name: "value", Local: true, Ty: b.Int}}}
	entry := core.Def{Name: "main", Type: b.Int, Control: types.Control{Transport: types.Machine}, Body: suspend(b, intLit(b, 1))}
	p := &core.Prog{Defs: []core.Def{factory, entry}}
	if errs := core.InferCaptures(p, b); len(errs) != 0 {
		t.Fatal(errs)
	}
	mp, errs := Lower(p, b)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	if len(mp.Workers) != 2 || mp.Workers[0].Name != "Library.factory_machine_lambda1" || len(mp.Closures) != 1 {
		t.Fatalf("factory lacks its module-owned Machine closure: %v, %v", workerNames(mp), mp.Closures)
	}
	p.Defs = p.Defs[:1]
	onlyFactory, errs := Lower(p, b)
	if len(errs) != 0 || len(onlyFactory.Workers) != 1 || onlyFactory.Workers[0].Name != mp.Workers[0].Name || len(onlyFactory.Closures) != 1 {
		t.Fatalf("factory depends on downstream Machine consumer: %v, %v", workerNames(onlyFactory), errs)
	}
}

func TestLowerRootsMachineLambdaInsideDirectCoroutineOwnerCall(t *testing.T) {
	p, b := coretest.SynchronousCursorScope()
	main := &p.Defs[2]
	call := main.Body.(*core.App)
	factory := call.Args[0].(*core.Lambda)
	producer := factory.Body.(*core.Lambda)
	producer.Body.(*core.Seq).First = coretest.Pause(factory, &core.VarRef{Name: "captured", Local: true, Ty: b.Int})
	main.Body = &core.Let{Name: "captured", Rhs: &core.IntLit{Val: 7, Ty: b.Int}, Body: call, Ty: main.Type}
	if errs := core.InferCaptures(p, b); len(errs) != 0 {
		t.Fatal(errs)
	}
	mp, errs := Lower(p, b)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	found := false
	for _, closure := range mp.Closures {
		if closure.Expr != producer {
			continue
		}
		found = true
		captures := map[string]bool{}
		for _, capture := range closure.Captures {
			captures[capture.Name] = true
		}
		if !captures["captured"] || !captures[factory.Param] {
			t.Fatalf("producer lost value or pause capture: %+v", closure)
		}
		foundWorker := false
		for _, worker := range mp.Workers {
			foundWorker = foundWorker || worker.Name == closure.Worker
		}
		if !foundWorker {
			t.Fatal("producer has no Machine worker")
		}
	}
	if !found {
		t.Fatal("Machine producer inside Direct factory was not rooted")
	}
}

func TestMachineLintRecomputesLivenessAndRejectsBadEdges(t *testing.T) {
	_, b := testBuiltins()
	x := &core.VarRef{Name: "x", Local: true, Ty: b.Int}
	body := &core.Let{Name: "x", Rhs: suspend(b, intLit(b, 1)), Ty: b.Int,
		Body: &core.Let{Name: "y", Rhs: suspend(b, x), Ty: b.Int, Body: x}}
	p := &core.Prog{Defs: []core.Def{{Name: "main", Type: b.Int,
		Control: types.Control{Transport: types.Machine}, Body: body}}}
	mp, errs := Lower(p, b)
	if len(errs) != 0 {
		t.Fatalf("Lower: %s", errorsText(errs))
	}

	brokenLive := *mp
	brokenLive.Workers = append([]Worker(nil), mp.Workers...)
	brokenLive.Workers[0].Blocks = append([]Block(nil), mp.Workers[0].Blocks...)
	brokenLive.Workers[0].Blocks[2].LiveOut = nil
	if got := errorsText(Lint(&brokenLive)); !strings.Contains(got, "LiveOut") {
		t.Fatalf("missing liveness error:\n%s", got)
	}

	brokenEdge := *mp
	brokenEdge.Workers = append([]Worker(nil), mp.Workers...)
	brokenEdge.Workers[0].Blocks = append([]Block(nil), mp.Workers[0].Blocks...)
	term := *brokenEdge.Workers[0].Blocks[3].Term.(*Suspend)
	term.Next = 99
	brokenEdge.Workers[0].Blocks[3].Term = &term
	if got := errorsText(Lint(&brokenEdge)); !strings.Contains(got, "invalid successor 99") {
		t.Fatalf("missing successor error:\n%s", got)
	}
}

func TestMachineLintChecksCleanupOwnership(t *testing.T) {
	_, b := testBuiltins()
	result := Local{Name: "result", Ty: b.Int}
	base := Worker{Name: "main", Result: b.Int, Entry: 0, Locals: []Local{result}, Blocks: []Block{
		{ID: 0, Term: &Return{Value: intLit(b, 1)}},
	}}
	underflow := Prog{Workers: []Worker{base}}
	underflow.Workers[0].Blocks[0].Term = &PopCleanup{Next: 1}
	underflow.Workers[0].Blocks = append(underflow.Workers[0].Blocks,
		Block{ID: 1, Term: &Return{Value: intLit(b, 1)}})
	analyze(&underflow.Workers[0])
	if got := errorsText(Lint(&underflow)); !strings.Contains(got, "cleanup stack underflow") {
		t.Fatalf("missing cleanup underflow error:\n%s", got)
	}

	pending := Prog{Workers: []Worker{base}}
	pending.Workers[0].Blocks[0].Term = &PushCleanup{
		Acquire: intLit(b, 1), Resource: result, Release: &core.UnitLit{Ty: b.Unit}, Next: 1,
	}
	pending.Workers[0].Blocks = append(pending.Workers[0].Blocks,
		Block{ID: 1, Term: &Return{Value: intLit(b, 1)}})
	analyze(&pending.Workers[0])
	if got := errorsText(Lint(&pending)); !strings.Contains(got, "returns with 1 pending cleanup") {
		t.Fatalf("missing pending cleanup error:\n%s", got)
	}
}

func TestOrdinaryCoreLintRejectsPrivateSuspension(t *testing.T) {
	_, b := testBuiltins()
	p := &core.Prog{Defs: []core.Def{{Name: "main", Type: b.Int,
		Control: types.Control{Transport: types.Machine}, Body: suspend(b, intLit(b, 1))}}}
	if got := errorsText(core.Lint(p, b)); !strings.Contains(got, "compiler-only suspension reached ordinary Core") {
		t.Fatalf("ordinary lint did not reject suspension:\n%s", got)
	}
	if got := core.LintMachineInput(p, b); len(got) != 0 {
		t.Fatalf("machine-input lint rejected fixture:\n%s", errorsText(got))
	}
}

func testBuiltins() (*types.Supply, *types.Builtins) {
	sup := &types.Supply{}
	return sup, types.NewBuiltins(sup)
}

func intLit(b *types.Builtins, value int64) core.Expr {
	return &core.IntLit{Val: value, Ty: b.Int}
}

func suspend(b *types.Builtins, request core.Expr) core.Expr {
	return &core.Suspend{Request: request, Ty: b.Int}
}

func errorsText(errs []error) string {
	var out strings.Builder
	for _, err := range errs {
		out.WriteString(err.Error())
		out.WriteByte('\n')
	}
	return out.String()
}

func workerNames(p *Prog) []string {
	out := make([]string, len(p.Workers))
	for i := range p.Workers {
		out[i] = p.Workers[i].Name
	}
	return out
}
