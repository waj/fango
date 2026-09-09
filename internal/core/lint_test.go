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
		Effect: EffectInstance{Unique: eff.Unique, Name: eff.Name},
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
