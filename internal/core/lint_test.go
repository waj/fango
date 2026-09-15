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

func TestLintRejectsRetaggedCallbackBinding(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	actual := &types.TFun{Arg: b.Unit, Ret: b.Int}
	wrong := &types.TFun{Arg: b.Unit, Ret: b.String}
	callback := &Lambda{Param: "_", Body: &IntLit{Val: 1, Ty: b.Int}, Ty: actual, ParamCapture: sup.FreshCapture()}
	ref := &VarRef{Name: "callback", Local: true, Ty: actual}
	app := &App{CalleeKind: Value, Callee: ref, Args: []Expr{&UnitLit{Ty: b.Unit}}, Ty: b.Int}
	body := &Let{Name: "callback", Rhs: callback, Body: app, Ty: b.Int}
	p := &Prog{Defs: []Def{{Name: "main", Type: b.Int, Body: body}}}
	InferCaptures(p, b)
	if got := lintText(p, b); got != "" {
		t.Fatalf("valid callback: %s", got)
	}
	ref.Ty = wrong
	if got := lintText(p, b); !strings.Contains(got, "changes its binding type") {
		t.Fatalf("retagged callback: %s", got)
	}
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
	op := &types.EffectOp{Owner: eff, Index: 0, Name: "fail", Arity: 1, ParamTypes: []types.Type{b.Int}, ResultType: b.Int, Abort: true}
	eff.Ops = []*types.EffectOp{op}
	ev := EffectInstance{Unique: eff.Unique, Name: eff.Name, Captures: types.ScopeCapture(1), Control: types.Control{Transport: types.Exit}}
	exit := &ControlExit{Effect: ev, Op: op, Payload: []Expr{&IntLit{Val: 7, Ty: b.Int}}, Ty: b.Int}
	h := &Handle{
		Body: exit, Effect: ev, Scope: 1, Ty: b.Int,
		Clauses: []HandlerClause{{Op: op, Params: []string{"x"}, ParamTypes: []types.Type{b.Int}, ResultType: b.Int,
			Body: &IntLit{Val: 0, Ty: b.Int}}},
	}
	fn := &types.TFun{Arg: b.Unit, Ret: b.Int}
	p := &Prog{Effects: []*types.EffectInfo{eff}, Defs: []Def{{Name: "run", Type: fn, Params: []string{"_"}, ParamCaptures: []types.CaptureVar{1}, Body: h}}}
	InferCaptures(p, b)
	if got := lintText(p, b); got != "" {
		t.Fatalf("Lint rejected checked Exit Core:\n%s", got)
	}

	exit.Effect.Control = types.Control{}
	if got := lintText(p, b); !strings.Contains(got, "ControlExit evidence is not Exit transport") {
		t.Fatalf("Lint errors = %q, want malformed exit evidence diagnostic", got)
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

// scopeFixture builds the cleanup-scope intrinsic the way elaboration does:
// three callback parameters, and a body that acquires, uses, and releases.
func scopeFixture() (*Prog, *types.Builtins, *Bracket, *Def) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	acquireTy := &types.TFun{Arg: b.Unit, Ret: b.String}
	releaseTy := &types.TFun{Arg: b.String, Ret: b.Unit}
	result := &types.TVar{ID: sup.NextUnique(), Rigid: true}
	useTy := &types.TFun{Arg: b.String, Ret: result}
	defTy := &types.TFun{Arg: acquireTy, Ret: &types.TFun{Arg: releaseTy, Ret: &types.TFun{Arg: useTy, Ret: result}}}
	call := func(fn string, fnTy *types.TFun, arg Expr) Expr {
		return &App{CalleeKind: Value, Callee: &VarRef{Name: fn, Local: true, Ty: fnTy},
			Args: []Expr{arg}, Ty: fnTy.Ret, Control: types.FunctionControl(fnTy)}
	}
	resource := func() Expr { return &VarRef{Name: "_resource", Local: true, Ty: b.String} }
	scope := &Bracket{
		Scope: 1, Resource: "_resource", ResourceTy: b.String,
		Acquire: call("_acquire", acquireTy, &UnitLit{Ty: b.Unit}),
		Release: call("_release", releaseTy, resource()),
		Body:    call("_use", useTy, resource()),
		Ty:      result,
	}
	def := Def{Name: types.ScopeBracketName, Type: defTy, TyParams: []*types.TVar{result},
		Params:        []string{"_acquire", "_release", "_use"},
		ParamCaptures: []types.CaptureVar{1, 2, 3},
		Body:          scope,
	}
	p := &Prog{Defs: []Def{def}}
	return p, b, scope, &p.Defs[0]
}

func TestLintAcceptsCleanupScopeCore(t *testing.T) {
	p, b, _, _ := scopeFixture()
	InferCaptures(p, b)
	if got := lintText(p, b); got != "" {
		t.Fatalf("Lint rejected a checked cleanup scope:\n%s", got)
	}
}

func TestDumpCleanupScope(t *testing.T) {
	p, b, _, _ := scopeFixture()
	InferCaptures(p, b)
	got := Dump(p)
	want := "(bracket 1 _resource String"
	if !strings.Contains(got, want) {
		t.Fatalf("Dump = %q, want it to contain %q", got, want)
	}
}

func TestLintRejectsMalformedCleanupScopeCore(t *testing.T) {
	tests := []struct {
		name, want string
		damage     func(*Bracket, *Def, *types.Builtins)
	}{
		{"no scope identity", "invalid or reused scope identity",
			func(s *Bracket, _ *Def, _ *types.Builtins) { s.Scope = 0 }},
		{"outside the intrinsic", "cleanup scope outside",
			func(_ *Bracket, d *Def, _ *types.Builtins) { d.Name = "Elsewhere.bracket" }},
		{"release is not Unit", "want ()",
			func(s *Bracket, _ *Def, b *types.Builtins) { s.Release = &IntLit{Val: 1, Ty: b.Int} }},
		{"acquire disagrees with the resource", "want resource",
			func(s *Bracket, _ *Def, b *types.Builtins) { s.Acquire = &IntLit{Val: 1, Ty: b.Int} }},
		{"body disagrees with the scope", "type differs from its body",
			func(s *Bracket, _ *Def, b *types.Builtins) { s.Body = &StringLit{Val: "x", Ty: b.String} }},
		{"control disagrees with its children", "disagrees with its children",
			func(s *Bracket, _ *Def, _ *types.Builtins) { s.Control = types.Control{Transport: types.Exit} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, b, scope, def := scopeFixture()
			InferCaptures(p, b)
			tt.damage(scope, def, b)
			if got := lintText(p, b); !strings.Contains(got, tt.want) {
				t.Fatalf("Lint errors = %q, want one containing %q", got, tt.want)
			}
		})
	}
}

func TestCleanupScopeResultRetainsItsResource(t *testing.T) {
	p, b, _, def := scopeFixture()
	InferCaptures(p, b)
	// The intrinsic's own result conservatively retains whatever its acquire
	// and use callbacks retain; the borrowed resource scope is discharged at
	// the boundary rather than escaping into the summary.
	if def.ResultCaptures.HasScope(1) {
		t.Fatalf("result captures = %v, want the scope discharged", def.ResultCaptures)
	}
	if len(def.ResultCaptures.Vars) == 0 {
		t.Fatalf("result captures = %v, want the callback captures retained", def.ResultCaptures)
	}
}

func TestLintReconstructsCaptureContracts(t *testing.T) {
	for _, damage := range []string{"missing", "forged"} {
		t.Run(damage, func(t *testing.T) {
			p, b, _, _ := scopeFixture()
			if errs := InferCaptures(p, b); len(errs) > 0 {
				t.Fatal(errs)
			}
			if damage == "missing" {
				p.Defs[0].CaptureContract = nil
			} else {
				p.Defs[0].CaptureContract.Body.Kind = "scalar"
			}
			if got := lintText(p, b); !strings.Contains(got, "capture contract is stale") {
				t.Fatalf("accepted %s contract: %s", damage, got)
			}
		})
	}
}
