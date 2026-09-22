// Package execcodec serializes checked runtime Core for the interpreter
// worker. It deliberately drops source spans: checking and diagnostics stay in
// the compiler process, while the worker needs only executable identities.
package execcodec

import (
	"reflect"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/core"
	machineir "github.com/waj/fango/internal/machine"
	"github.com/waj/fango/internal/meta"
	"github.com/waj/fango/internal/objectcodec"
	"github.com/waj/fango/internal/types"
)

type Payload struct {
	Program *core.Prog
	Machine *machineir.Prog
	Expr    core.Expr
	Force   string
}

func Encode(p *Payload) ([]byte, error) { return objectcodec.Encode(p) }

func Decode(data []byte) (*Payload, error) {
	var p *Payload
	err := objectcodec.Decode(data, &p, objectcodec.Context{Types: registry(), DropSpans: true})
	return p, err
}

func registry() map[string]reflect.Type {
	return objectcodec.Registry(
		(*Payload)(nil), (*types.TVar)(nil), (*types.TCon)(nil), (*types.TFun)(nil), types.Row{},
		(*types.ClassInfo)(nil), (*types.MethodInfo)(nil), (*types.NativeInfo)(nil), (*types.FallibleShape)(nil),
		(*types.CtorInfo)(nil), (*types.ADTInfo)(nil), (*types.EffectInfo)(nil), (*types.EffectOp)(nil),
		(*types.CaptureContract)(nil), (*types.CaptureFlow)(nil), (*types.CaptureRow)(nil),
		(*meta.Template)(nil), (*meta.TypeRepr)(nil), (*meta.Code)(nil),
		(*core.Prog)(nil), (*core.IntLit)(nil), (*core.FloatLit)(nil), (*core.StringLit)(nil), (*core.CharLit)(nil), (*core.UnitLit)(nil), (*core.BoolLit)(nil),
		(*core.Neg)(nil), (*core.If)(nil), (*core.Perform)(nil), (*core.ControlExit)(nil), (*core.Suspend)(nil),
		(*core.IteratorScope)(nil), (*core.IteratorNext)(nil), (*core.FailureInspect)(nil), (*core.Handle)(nil), (*core.ResumeTail)(nil),
		(*core.Bracket)(nil), (*core.Seq)(nil), (*core.Let)(nil), (*core.Lambda)(nil), (*core.VarRef)(nil), (*core.Quote)(nil),
		(*core.TypeOf)(nil), (*core.NativeCall)(nil), (*core.App)(nil), (*core.Case)(nil),
		(*core.Guard)(nil), (*core.Unreachable)(nil), (*core.Leaf)(nil), (*core.SwitchCtor)(nil), (*core.SwitchLit)(nil),
		(*ast.IntLit)(nil), (*ast.FloatLit)(nil), (*ast.StringLit)(nil), (*ast.CharLit)(nil), (*ast.UnitLit)(nil),
		(*ast.Var)(nil), (*ast.Ctor)(nil), (*ast.RecordLit)(nil), (*ast.RecordGet)(nil), (*ast.RecordUpdate)(nil),
		(*ast.App)(nil), (*ast.Neg)(nil), (*ast.If)(nil), (*ast.Block)(nil), (*ast.Lambda)(nil),
		(*ast.BinOp)(nil), (*ast.OpChain)(nil), (*ast.OpRef)(nil), (*ast.Case)(nil), (*ast.Handle)(nil),
		(*ast.Resume)(nil), (*ast.Quote)(nil), (*ast.Splice)(nil), (*ast.TypeOf)(nil), (*ast.MetaValue)(nil),
		(*ast.TName)(nil), (*ast.TVarName)(nil), (*ast.TFunExpr)(nil), (*ast.TApp)(nil), (*ast.TRow)(nil),
		(*ast.PVar)(nil), (*ast.PWildcard)(nil), (*ast.PUnit)(nil), (*ast.PInt)(nil), (*ast.PFloat)(nil),
		(*ast.PString)(nil), (*ast.PChar)(nil), (*ast.PPin)(nil), (*ast.PRecord)(nil), (*ast.PCtor)(nil),
		(*machineir.Prog)(nil), (*machineir.Eval)(nil), (*machineir.Branch)(nil), (*machineir.SwitchCtor)(nil),
		(*machineir.SwitchLit)(nil), (*machineir.Suspend)(nil), (*machineir.CursorAdvance)(nil), (*machineir.Call)(nil),
		(*machineir.Handle)(nil), (*machineir.StateResume)(nil), (*machineir.PushCleanup)(nil), (*machineir.CursorOpen)(nil),
		(*machineir.CursorClose)(nil), (*machineir.PopCleanup)(nil), (*machineir.Return)(nil),
	)
}
