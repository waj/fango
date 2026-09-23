package eval

import (
	"context"
	"io"
	"testing"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

func TestRecursiveSnapshotPreservesSurroundingMachineLocals(t *testing.T) {
	for _, tail := range []bool{false, true} {
		in := &interp{ctx: context.Background(), env: NewEnv(), out: io.Discard, evidence: map[int]*evidence{}}
		fr := &Frame{vars: map[string]Value{"saved": int64(17), "bodyOnly": int64(42)}, mutable: true}
		fn := &types.TFun{Arg: unitTy(), Ret: intTy()}
		lambda := &core.Lambda{Param: "unit", Ty: fn, Body: &core.VarRef{Name: "saved", Local: true, Ty: intTy()}}
		body := core.Expr(&core.VarRef{Name: "bodyOnly", Local: true, Ty: intTy()})
		if tail {
			body = &core.ResumeTail{Owner: 1, Value: body, ClauseResult: intTy()}
		}
		let := &core.Let{Name: "recursive", Rec: true, Rhs: lambda, Body: body, Ty: intTy()}
		var result Value
		var err error
		if tail {
			result, err = in.evalResumeTail(let, fr, 1, &evidence{})
		} else {
			result, err = in.eval(let, fr)
		}
		if err != nil || result != int64(42) {
			t.Fatalf("tail=%v surrounding local: %v %v", tail, result, err)
		}
		// Returning the recursive closure must still snapshot referenced values
		// independently of later Machine pruning and retain its self binding.
		let.Body, let.Ty = &core.VarRef{Name: "recursive", Local: true, Ty: fn}, fn
		value, err := in.eval(let, fr)
		if err != nil {
			t.Fatal(err)
		}
		closure := value.(*Closure)
		clear(fr.vars)
		result, err = in.callClosure(closure, struct{}{})
		if err != nil || result != int64(17) {
			t.Fatalf("tail=%v closure snapshot: %v %v", tail, result, err)
		}
		if _, found := closure.Env.lookup("bodyOnly"); found {
			t.Fatal("closure retained body-only local")
		}
		if self, found := closure.Env.lookup("recursive"); !found || self != closure {
			t.Fatal("recursive self binding was lost")
		}
	}
}
