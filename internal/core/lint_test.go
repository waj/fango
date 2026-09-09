package core

import (
	"strings"
	"testing"

	"github.com/waj/fango/internal/types"
)

func resumeFixture(body func(*types.Builtins) Expr) (*Prog, *types.Builtins) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	eff := &types.EffectInfo{Unique: sup.NextUnique(), Name: "Ask"}
	op := &types.EffectOp{Owner: eff, Index: 0, Name: "ask", Arity: 1,
		ParamTypes: []types.Type{b.Unit}, ResultType: b.Int}
	eff.Ops = []*types.EffectOp{op}
	h := &Handle{
		Body:   &IntLit{Val: 0, Ty: b.Int},
		Effect: EffectInstance{Unique: eff.Unique, Name: eff.Name, Captures: types.ScopeCapture(1)},
		Scope:  1,
		Clauses: []HandlerClause{{Op: op, ResumeID: 1, Params: []string{"()"},
			ParamTypes: []types.Type{b.Unit}, ResultType: b.Int, Body: body(b)}},
		Ty: b.Int,
	}
	return &Prog{Effects: []*types.EffectInfo{eff}, Defs: []Def{{Name: "main", Type: b.Int, Body: h}}}, b
}

func lintText(p *Prog, b *types.Builtins) string {
	var out strings.Builder
	for _, err := range Lint(p, b) {
		out.WriteString(err.Error())
		out.WriteByte('\n')
	}
	return out.String()
}

func TestLintAcceptsBranchDependentTailResumes(t *testing.T) {
	p, b := resumeFixture(func(b *types.Builtins) Expr {
		return &If{Cond: &BoolLit{Val: true, Ty: b.Bool},
			Then: &ResumeTail{Owner: 1, Value: &IntLit{Val: 1, Ty: b.Int}, ClauseResult: b.Int},
			Else: &ResumeTail{Owner: 1, Value: &IntLit{Val: 2, Ty: b.Int}, ClauseResult: b.Int}, Ty: b.Int}
	})
	if got := lintText(p, b); got != "" {
		t.Fatalf("Lint rejected valid branch-dependent resumes:\n%s", got)
	}
}

func TestLintChecksExitControlContractAndDescriptor(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	eff := &types.EffectInfo{Unique: sup.NextUnique(), Name: "Fail"}
	op := &types.EffectOp{Owner: eff, Index: 0, Name: "fail", Arity: 1, ParamTypes: []types.Type{b.Int}, ResultType: b.Int}
	eff.Ops = []*types.EffectOp{op}
	exit := &ControlExit{Target: 1, Op: op, Payload: []Expr{&IntLit{Val: 7, Ty: b.Int}}, Ty: b.Int}
	h := &Handle{
		Body: exit, Effect: EffectInstance{Unique: eff.Unique, Name: eff.Name, Captures: types.ScopeCapture(1)}, Scope: 1, Ty: b.Int,
		Clauses: []HandlerClause{{Op: op, ResumeID: 1, Params: []string{"x"}, ParamTypes: []types.Type{b.Int}, ResultType: b.Int,
			Body: &ResumeTail{Owner: 1, Value: &IntLit{Val: 0, Ty: b.Int}, ClauseResult: b.Int}}},
		Control: types.Control{Transport: types.Exit},
	}
	fn := &types.TFun{Arg: b.Unit, Ret: b.Int, Control: types.Control{Transport: types.Exit}}
	p := &Prog{Effects: []*types.EffectInfo{eff}, Defs: []Def{{Name: "run", Type: fn, Params: []string{"_"}, ParamCaptures: []types.CaptureVar{1}, Control: types.Control{Transport: types.Exit}, Body: h}}}
	InferCaptures(p, b)
	if got := lintText(p, b); got != "" {
		t.Fatalf("Lint rejected checked Exit Core:\n%s", got)
	}

	p.Defs[0].Control = types.Control{}
	if got := lintText(p, b); !strings.Contains(got, "declared control direct disagrees") || !strings.Contains(got, "body control exit") {
		t.Fatalf("Lint errors = %q, want erased Exit contract diagnostics", got)
	}
}

func TestLintRejectsMalformedResumeCore(t *testing.T) {
	tests := []struct {
		name, want string
		body       func(*types.Builtins) Expr
	}{
		{"missing", "MISSING RESUME", func(b *types.Builtins) Expr { return &IntLit{Val: 1, Ty: b.Int} }},
		{"wrong owner", "MISSING RESUME", func(b *types.Builtins) Expr {
			return &ResumeTail{Owner: 2, Value: &IntLit{Val: 1, Ty: b.Int}, ClauseResult: b.Int}
		}},
		{"resume in rhs", "NON-TAIL RESUME", func(b *types.Builtins) Expr {
			return &Let{Name: "x", Rhs: &ResumeTail{Owner: 1, Value: &IntLit{Val: 1, Ty: b.Int}, ClauseResult: b.Int},
				Body: &ResumeTail{Owner: 1, Value: &IntLit{Val: 2, Ty: b.Int}, ClauseResult: b.Int}, Ty: b.Int}
		}},
		{"resume in lambda", "NON-TAIL RESUME", func(b *types.Builtins) Expr {
			fn := &types.TFun{Arg: b.Unit, Eff: types.Row{}, Ret: b.Int}
			return &Let{Name: "f", Rhs: &Lambda{Param: "_", Body: &ResumeTail{Owner: 1, Value: &IntLit{Val: 1, Ty: b.Int}, ClauseResult: b.Int}, Ty: fn},
				Body: &ResumeTail{Owner: 1, Value: &IntLit{Val: 2, Ty: b.Int}, ClauseResult: b.Int}, Ty: b.Int}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, b := resumeFixture(tc.body)
			if got := lintText(p, b); !strings.Contains(got, tc.want) {
				t.Fatalf("Lint errors = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLintChecksParameterizedHandlerState(t *testing.T) {
	valid := func(b *types.Builtins) Expr {
		return &ResumeTail{
			Owner:        1,
			Value:        &IntLit{Val: 1, Ty: b.Int},
			NextState:    &IntLit{Val: 2, Ty: b.Int},
			ClauseResult: b.Int,
		}
	}
	p, b := resumeFixture(valid)
	h := p.Defs[0].Body.(*Handle)
	h.State = &HandlerState{Name: "current", Initial: &IntLit{Val: 0, Ty: b.Int}, Ty: b.Int}
	h.Scoped = true
	if got := lintText(p, b); got != "" {
		t.Fatalf("Lint rejected valid parameterized handler:\n%s", got)
	}

	h.Clauses[0].Body.(*ResumeTail).NextState = nil
	if got := lintText(p, b); !strings.Contains(got, "has no next state") {
		t.Fatalf("Lint errors = %q, want missing next-state error", got)
	}
}

func scopedCaptureFixture(capturing bool) (*Prog, *types.Builtins) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	eff := &types.EffectInfo{Unique: sup.NextUnique(), Name: "Borrow", Scoped: true}
	op := &types.EffectOp{Owner: eff, Index: 0, Name: "read", Arity: 1,
		ParamTypes: []types.Type{b.Unit}, ResultType: b.Int}
	eff.Ops = []*types.EffectOp{op}
	scope := types.ScopeID(1)
	fn := &types.TFun{Arg: b.Unit, Ret: b.Int}
	body := Expr(&Lambda{Param: "_", ParamCapture: 1, Ty: fn, Body: &IntLit{Val: 1, Ty: b.Int}})
	if capturing {
		body = &Lambda{Param: "_", ParamCapture: 1, Ty: fn, Body: &Perform{
			Op: op, Effect: EffectInstance{Unique: eff.Unique, Name: eff.Name, Captures: types.ScopeCapture(scope)},
			Args: []Expr{&UnitLit{Ty: b.Unit}}, Ty: b.Int}}
	}
	h := &Handle{Body: body, Effect: EffectInstance{Unique: eff.Unique, Name: eff.Name, Captures: types.ScopeCapture(scope)},
		Scope: scope, Scoped: true, Ty: fn,
		Clauses: []HandlerClause{{Op: op, ResumeID: 1, Params: []string{"()"}, ParamTypes: []types.Type{b.Unit}, ResultType: b.Int,
			Body: &ResumeTail{Owner: 1, Value: &IntLit{Val: 1, Ty: b.Int}, ClauseResult: fn}}}}
	return &Prog{Effects: []*types.EffectInfo{eff}, Defs: []Def{{Name: "main", Type: fn, Body: h}}}, b
}

func TestLintRejectsScopedEvidenceCapturedByResult(t *testing.T) {
	p, b := scopedCaptureFixture(true)
	InferCaptures(p, b)
	if got := lintText(p, b); !strings.Contains(got, "RESOURCE ESCAPES") {
		t.Fatalf("Lint error = %q, want RESOURCE ESCAPES", got)
	}
}

func TestLintAllowsReturnedPureClosureFromScopedHandler(t *testing.T) {
	p, b := scopedCaptureFixture(false)
	InferCaptures(p, b)
	if got := lintText(p, b); got != "" {
		t.Fatalf("Lint rejected pure returned closure:\n%s", got)
	}
}

func TestCaptureSummaryPropagatesThroughWorker(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	fn := &types.TFun{Arg: b.Unit, Ret: b.Int}
	idTy := &types.TFun{Arg: fn, Ret: fn}
	id := Def{Name: "id", Type: idTy, Params: []string{"x"}, ParamCaptures: []types.CaptureVar{1},
		Body: &VarRef{Name: "x", Local: true, Ty: fn}}
	eff := &types.EffectInfo{Unique: sup.NextUnique(), Name: "Borrow", Scoped: true}
	op := &types.EffectOp{Owner: eff, Index: 0, Name: "read", Arity: 1, ParamTypes: []types.Type{b.Unit}, ResultType: b.Int}
	eff.Ops = []*types.EffectOp{op}
	scope := types.ScopeID(1)
	callback := &Lambda{Param: "_", ParamCapture: 2, Ty: fn, Body: &Perform{Op: op,
		Effect: EffectInstance{Unique: eff.Unique, Name: eff.Name, Captures: types.ScopeCapture(scope)},
		Args:   []Expr{&UnitLit{Ty: b.Unit}}, Ty: b.Int}}
	call := &App{CalleeKind: Worker, Callee: &VarRef{Name: "id", Ty: idTy}, Args: []Expr{callback}, Ty: fn}
	h := &Handle{Body: call, Effect: EffectInstance{Unique: eff.Unique, Name: eff.Name, Captures: types.ScopeCapture(scope)},
		Scope: scope, Scoped: true, Ty: fn, Clauses: []HandlerClause{{Op: op, ResumeID: 1, Params: []string{"()"},
			ParamTypes: []types.Type{b.Unit}, ResultType: b.Int,
			Body: &ResumeTail{Owner: 1, Value: &IntLit{Val: 1, Ty: b.Int}, ClauseResult: fn}}}}
	p := &Prog{Effects: []*types.EffectInfo{eff}, Defs: []Def{id, {Name: "main", Type: fn, Body: h}}}
	InferCaptures(p, b)
	if !p.Defs[0].ResultCaptures.HasVar(1) {
		t.Fatalf("id summary = %#v, want parameter capture", p.Defs[0].ResultCaptures)
	}
	if got := lintText(p, b); !strings.Contains(got, "RESOURCE ESCAPES") {
		t.Fatalf("Lint error = %q, want propagated RESOURCE ESCAPES", got)
	}
}

func TestLintRejectsScopedCaptureStoredThroughOuterEvidence(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	borrow := &types.EffectInfo{Unique: sup.NextUnique(), Name: "Borrow", Scoped: true}
	read := &types.EffectOp{Owner: borrow, Index: 0, Name: "read", Arity: 1, ParamTypes: []types.Type{b.Unit}, ResultType: b.Int}
	borrow.Ops = []*types.EffectOp{read}
	fn := &types.TFun{Arg: b.Unit, Ret: b.Int}
	store := &types.EffectInfo{Unique: sup.NextUnique(), Name: "Store"}
	put := &types.EffectOp{Owner: store, Index: 0, Name: "put", Arity: 1, ParamTypes: []types.Type{fn}, ResultType: b.Unit, RetainsArguments: true}
	store.Ops = []*types.EffectOp{put}
	outerScope, innerScope := types.ScopeID(1), types.ScopeID(2)
	callback := &Lambda{Param: "_", ParamCapture: 1, Ty: fn, Body: &Perform{Op: read,
		Effect: EffectInstance{Unique: borrow.Unique, Name: borrow.Name, Captures: types.ScopeCapture(innerScope)},
		Args:   []Expr{&UnitLit{Ty: b.Unit}}, Ty: b.Int}}
	storeCall := &Perform{Op: put, Effect: EffectInstance{Unique: store.Unique, Name: store.Name, Captures: types.ScopeCapture(outerScope)},
		Args: []Expr{callback}, Ty: b.Unit}
	inner := &Handle{Body: storeCall, Effect: EffectInstance{Unique: borrow.Unique, Name: borrow.Name, Captures: types.ScopeCapture(innerScope)},
		Scope: innerScope, Scoped: true, Ty: b.Unit, Clauses: []HandlerClause{{Op: read, ResumeID: 1, Params: []string{"()"},
			ParamTypes: []types.Type{b.Unit}, ResultType: b.Int,
			Body: &ResumeTail{Owner: 1, Value: &IntLit{Val: 1, Ty: b.Int}, ClauseResult: b.Unit}}}}
	outer := &Handle{Body: inner, Effect: EffectInstance{Unique: store.Unique, Name: store.Name, Captures: types.ScopeCapture(outerScope)},
		Scope: outerScope, Ty: b.Unit, Clauses: []HandlerClause{{Op: put, ResumeID: 2, Params: []string{"f"},
			ParamTypes: []types.Type{fn}, ResultType: b.Unit,
			Body: &ResumeTail{Owner: 2, Value: &UnitLit{Ty: b.Unit}, ClauseResult: b.Unit}}}}
	p := &Prog{Effects: []*types.EffectInfo{borrow, store}, Defs: []Def{{Name: "main", Type: b.Unit, Body: outer}}}
	InferCaptures(p, b)
	if got := lintText(p, b); !strings.Contains(got, "RESOURCE ESCAPES") {
		t.Fatalf("Lint error = %q, want RESOURCE ESCAPES", got)
	}
}
