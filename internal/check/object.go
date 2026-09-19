package check

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"reflect"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/infer"
	"github.com/waj/fango/internal/meta"
	"github.com/waj/fango/internal/modules"
	"github.com/waj/fango/internal/objectcodec"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/staging"
	"github.com/waj/fango/internal/types"
)

const moduleObjectSchema = 1

type objectEnvelope struct {
	Schema        int             `json:"schema"`
	Kind          string          `json:"kind"`
	PayloadSHA256 string          `json:"payload_sha256"`
	Payload       json.RawMessage `json:"payload"`
}

// ModuleObject is the in-memory installable boundary. Persistence and lookup
// policy belong to M5; M4 owns its typed codec and transactional installation.
type ModuleObject struct {
	State        *infer.ModuleState
	Resolver     modules.Interface
	Nominals     map[int]string
	EffectNames  map[int]string
	Runtime      []core.Def
	Stage        []core.Def
	StageGroups  []staging.Group
	TemplateBase int
	Templates    []*meta.Template
}

func EncodeObject(object *ModuleObject) ([]byte, error) {
	payload, err := objectcodec.Encode(object)
	if err != nil {
		return nil, err
	}
	h := sha256.Sum256(payload)
	return json.Marshal(objectEnvelope{Schema: moduleObjectSchema, Kind: "module-object", PayloadSHA256: hex.EncodeToString(h[:]), Payload: payload})
}

func DecodeObject(data []byte, sources map[string]*source.File) (*ModuleObject, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var envelope objectEnvelope
	if err := dec.Decode(&envelope); err != nil {
		return nil, err
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("trailing module-object data")
	}
	if envelope.Schema != moduleObjectSchema || envelope.Kind != "module-object" || len(envelope.Payload) == 0 {
		return nil, fmt.Errorf("unsupported module-object envelope")
	}
	h := sha256.Sum256(envelope.Payload)
	if envelope.PayloadSHA256 != hex.EncodeToString(h[:]) {
		return nil, fmt.Errorf("module-object payload digest mismatch")
	}
	var object *ModuleObject
	err := objectcodec.Decode(envelope.Payload, &object, objectcodec.Context{Types: objectTypes(), Sources: sources})
	return object, err
}

func objectTypes() map[string]reflect.Type {
	return objectcodec.Registry(
		(*ModuleObject)(nil), (*infer.ModuleState)(nil), (*infer.InstanceInfo)(nil), (*infer.DeriverInfo)(nil),
		(*types.TVar)(nil), (*types.TCon)(nil), (*types.TFun)(nil), types.Row{},
		(*types.ClassInfo)(nil), (*types.MethodInfo)(nil), (*types.NativeInfo)(nil), (*types.FallibleShape)(nil),
		(*types.CtorInfo)(nil), (*types.ADTInfo)(nil), (*types.EffectInfo)(nil), (*types.EffectOp)(nil),
		(*types.CaptureContract)(nil), (*types.CaptureFlow)(nil), (*types.CaptureRow)(nil),
		(*meta.Template)(nil), (*meta.TypeRepr)(nil), (*meta.Code)(nil),
		(*core.IntLit)(nil), (*core.FloatLit)(nil), (*core.StringLit)(nil), (*core.CharLit)(nil), (*core.UnitLit)(nil), (*core.BoolLit)(nil),
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
		(*ast.TName)(nil), (*ast.TVarName)(nil), (*ast.TFunExpr)(nil), (*ast.TApp)(nil),
		(*ast.PVar)(nil), (*ast.PWildcard)(nil), (*ast.PUnit)(nil), (*ast.PInt)(nil), (*ast.PFloat)(nil),
		(*ast.PString)(nil), (*ast.PChar)(nil), (*ast.PPin)(nil), (*ast.PRecord)(nil), (*ast.PCtor)(nil),
	)
}
