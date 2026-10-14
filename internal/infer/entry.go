package infer

// Entry assembly (doc/reference/commands.md, "Entry points"). `main` may
// perform effects that declare default handlers. The program then runs an
// assembled entry instead: the splice of Runtime.Entry.assemble, given
// main's type and a reference to it. Everything about defaults, including
// which effects have them, lives in that Fango metaprogram; the checker
// knows only IO, which needs no runner.

import (
	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/meta"
	"github.com/waj/fango/internal/types"
)

// AssembleName is the generator the entry splices.
const AssembleName = "Runtime.Entry.assemble"

// AssembledEntryName is the program entry assembled around the `main` named
// entry. The name cannot be written in source, so it never collides.
func AssembledEntryName(entry string) string {
	if module := symbolModule(entry); module != "" {
		return module + ".main#entry"
	}
	return "main#entry"
}

// assembleEntry checks the assembled entry when the module defines a `main`
// that performs effects besides IO. A main that performs only IO is its own
// entry.
func (ck *Checker) assembleEntry(infos []DeclInfo) ([]DeclInfo, []diag.Error) {
	if ck.EntryName == "" || ck.assemblingEntry {
		return nil, nil
	}
	var main *DeclInfo
	for i := range infos {
		if infos[i].Name == ck.EntryName {
			main = &infos[i]
		}
	}
	if main == nil || len(main.Params) != 1 {
		return nil, nil
	}
	ty := ck.Sub.Apply(main.Scheme.Body)
	fn, ok := ty.(*types.TFun)
	if !ok {
		return nil, nil
	}
	var unhandled string
	for _, label := range fn.Eff.Labels {
		if ck.IO == nil || label.Unique != ck.IO.Unique {
			unhandled = types.SurfaceName(label.Name)
			break
		}
	}
	if unhandled == "" {
		return nil, nil
	}
	sp := main.NameSpan
	if _, ok := ck.Env.Lookup(AssembleName); !ok {
		// Without Runtime.Entry in the program no effect can declare a
		// default, so the first one is simply unhandled.
		return nil, []diag.Error{diag.Errorf(sp, "UNHANDLED EFFECT",
			"`main` performs `%s`, which is not handled, and it declares no default handler.\nInstall its runner inside `main`.", unhandled)}
	}
	codeType, errs := ck.codeType(sp)
	if len(errs) > 0 {
		return nil, errs
	}
	operand := &ast.App{
		Fn: &ast.App{
			Fn:  &ast.Var{Name: AssembleName, Sp: sp},
			Arg: &ast.MetaValue{Value: ck.Reflect(ty, nil), Ty: ck.TypeNames[TypeReprName], Sp: sp},
		},
		Arg: &ast.MetaValue{Value: &meta.Code{Template: -1, Direct: &ast.Var{Name: ck.EntryName, Sp: sp}}, Ty: codeType, Sp: sp},
	}
	code, errs := ck.runSplice(operand, sp)
	if code == nil || len(errs) > 0 {
		return nil, errs
	}
	body := ck.Templates.Expand(code)
	if body == nil {
		return nil, []diag.Error{diag.Errorf(sp, "STAGE ERROR", "Entry assembly produced code I cannot expand.")}
	}
	meta.FillSpans(body, sp)
	name := AssembledEntryName(ck.EntryName)
	decl := &ast.ValueDecl{Name: name, NameSpan: sp, Params: []ast.Pattern{&ast.PUnit{Sp: sp}}, Body: body}
	savedEntry := ck.EntryName
	ck.EntryName, ck.assemblingEntry = name, true
	info, errs := ck.DeclWhere(decl, DeclPure)
	ck.EntryName, ck.assemblingEntry = savedEntry, false
	if len(errs) > 0 {
		return nil, errs
	}
	ck.BindDecl(info)
	ck.RecordChecked([]DeclInfo{info})
	return []DeclInfo{info}, nil
}
