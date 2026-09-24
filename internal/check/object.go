package check

import (
	"bytes"
	"fmt"
	"reflect"

	"github.com/waj/fango/internal/artifactframe"
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

const (
	moduleObjectSchema = 8
	moduleObjectKind   = "module-object"
	stageSection       = "stage"
)

// ModuleObject is the typed installable and persistent checked-module boundary.
type ModuleObject struct {
	State                  *infer.ModuleState
	Resolver               modules.Interface
	Semantic               string
	ABI                    string
	Implementation         string
	StageImplementation    string
	StageFingerprint       string
	StageDependencies      []string
	CheckStageDependencies []string
	Nominals               map[int]string
	EffectNames            map[int]string
	ScopeNames             map[types.ScopeID]string
	Runtime                []core.Def
	Stage                  []core.Def
	StageGroups            []staging.Group
	TemplateBase           int
	Templates              []*meta.Template
	Pending                *PendingStage `object:"omit"`
}

// PendingStage is a decoded object whose stage Core has not been read yet.
// Installation defers it, because nothing needs stage Core until some module
// is checked from source, and it is the largest part of an object.
// head is the object as decoded, kept because installation replaces the
// object's fields with interned copies and the recorded fingerprint describes
// the decoded form.
type PendingStage struct {
	decoder *objectcodec.Decoder
	head    *ModuleObject
}

type stagePayload struct {
	Stage  []core.Def
	Groups []staging.Group
}

// size is the encoded byte size of the undecoded stage section.
func (p *PendingStage) size() int { return p.decoder.SectionSize(stageSection) }

// load decodes the stage section through the decoder that produced the rest of
// the object, so structure shared with it comes back as the same pointers.
func (p *PendingStage) load() (*stagePayload, error) {
	var payload *stagePayload
	if err := p.decoder.Section(stageSection, &payload); err != nil {
		return nil, err
	}
	if payload == nil {
		payload = &stagePayload{}
	}
	// Reading the section is where the object's recorded stage fingerprint is
	// checked against its contents. Installation does not recompute it, so
	// this is what keeps a section from disagreeing with the object it
	// belongs to.
	if want := p.head.StageImplementation; want != "" && ownStageFingerprint(p.head, payload.Stage) != want {
		return nil, fmt.Errorf("stage Core does not match the fingerprint recorded for it")
	}
	return payload, nil
}

// EncodeObject frames the object behind the record of what it was built from.
// The record leads the payload instead of sitting among the object's own
// sections, so a slot holding a superseded artifact is rejected without
// interning its types or rebinding its sources.
func EncodeObject(object *ModuleObject, record []byte) ([]byte, error) {
	if bytes.ContainsRune(record, '\n') {
		return nil, fmt.Errorf("a validity record cannot span lines")
	}
	head := *object
	head.Stage, head.StageGroups, head.Pending = nil, nil, nil
	sections, err := objectcodec.EncodeSections(
		objectcodec.Section{Value: &head},
		objectcodec.Section{Name: stageSection, Value: &stagePayload{Stage: object.Stage, Groups: object.StageGroups}},
	)
	if err != nil {
		return nil, err
	}
	payload := make([]byte, 0, len(record)+1+len(sections))
	payload = append(payload, record...)
	payload = append(payload, '\n')
	payload = append(payload, sections...)
	framed := artifactframe.Wrap(moduleObjectKind, moduleObjectSchema, payload)
	if framed == nil {
		return nil, fmt.Errorf("cannot frame module object")
	}
	return framed, nil
}

// SplitObject separates an artifact's validity record from the object behind
// it, so a reader can reject a superseded slot before paying to decode one.
func SplitObject(data []byte) (record, sections []byte, err error) {
	payload, ok := artifactframe.Unwrap(moduleObjectKind, moduleObjectSchema, data)
	if !ok {
		return nil, nil, fmt.Errorf("unsupported module-object frame")
	}
	record, sections, ok = bytes.Cut(payload, []byte{'\n'})
	if !ok {
		return nil, nil, fmt.Errorf("module object carries no validity record")
	}
	return record, sections, nil
}

func DecodeObject(data []byte, sources map[string]*source.File) (*ModuleObject, error) {
	_, sections, err := SplitObject(data)
	if err != nil {
		return nil, err
	}
	return DecodeObjectSections(sections, sources)
}

// DecodeObjectSections decodes the object itself, once its record has been
// accepted.
func DecodeObjectSections(sections []byte, sources map[string]*source.File) (*ModuleObject, error) {
	decoder, err := objectcodec.NewDecoder(sections, objectcodec.Context{Types: objectTypes(), Sources: sources})
	if err != nil {
		return nil, err
	}
	var object *ModuleObject
	if err := decoder.Section("", &object); err != nil {
		return nil, err
	}
	if object != nil {
		head := *object
		object.Pending = &PendingStage{decoder: decoder, head: &head}
	}
	return object, nil
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
		(*core.CoroutineScope)(nil), (*core.CoroutineAdvance)(nil), (*core.Work)(nil), (*core.FailureInspect)(nil), (*core.Completion)(nil), (*core.Handle)(nil), (*core.ResumeTail)(nil),
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
	)
}
