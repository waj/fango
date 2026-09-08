package infer

// Deriving. `deriving (C)` runs C's *deriver* — a compile-time generator
// written in ordinary fango — and installs the instance it produces. The
// generated methods are checked and elaborated through exactly the same path
// as handwritten ones, so nothing downstream needs to know a method was
// derived (doc/design.md, "Compile-time metaprogramming").
//
// The compiler owns two halves of the arrangement and the deriver owns the
// third. The compiler owns the instance head, so derived instances land in
// the same whole-graph overlap check as handwritten ones; it owns the method
// parameters, which it hands to the deriver as `Code`; and it owns the
// instance context, which it reads back off the generated body rather than
// approximating from the declaration. The deriver owns only the method
// bodies.

import (
	"fmt"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/meta"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/types"
)

// DeriverInfo is one `deriver C` declaration: the compile-time symbol that
// generates each of C's methods.
type DeriverInfo struct {
	Class   *types.ClassInfo
	Owner   string
	Span    source.Span
	Methods map[string]string // class method surface name -> generator symbol
}

// deriverSymbol names a deriver method. The spelling cannot collide with an
// ordinary definition, which is the point: a deriver method is reachable only
// through the reference the compiler itself builds.
func deriverSymbol(owner, class, method string) string {
	name := "_deriver_" + types.SurfaceName(class) + "_" + method
	if owner != "" {
		return owner + "." + name
	}
	return name
}

// DeriverDecl checks one `deriver C` block. Each method's type is dictated by
// the class: a method with n arrows generates from a TypeInfo and n Code
// arguments, and produces Code. There is no new type-system machinery here —
// a deriver is an ordinary fango function with a compiler-supplied signature.
func (ck *Checker) DeriverDecl(d *ast.DeriverDecl) []diag.Error {
	cl := ck.Classes[d.Class]
	if cl == nil {
		return []diag.Error{diag.Errorf(d.ClassSpan, "UNKNOWN CLASS", "I don't know class `%s`.", d.Class)}
	}
	if old := ck.Derivers[cl.Name]; old != nil {
		return []diag.Error{diag.Errorf(d.ClassSpan, "DUPLICATE DERIVER",
			"Class `%s` already has a deriver, declared at %v.", types.SurfaceName(cl.Name), old.Span.StartPos())}
	}
	code, errs := ck.codeType(d.ClassSpan)
	if len(errs) > 0 {
		return errs
	}
	infoType := ck.TypeNames[TypeInfoName]
	if infoType == nil {
		return []diag.Error{diag.Errorf(d.ClassSpan, "STAGE ERROR",
			"I cannot find `Meta.TypeInfo`, so I cannot type this deriver.")}
	}
	given := map[string]*ast.ValueDecl{}
	for _, m := range d.Methods {
		if given[m.Name] != nil {
			errs = append(errs, diag.Errorf(m.NameSpan, "DUPLICATE METHOD", "Deriver method `%s` is defined twice.", m.Name))
		}
		given[m.Name] = m
	}
	info := &DeriverInfo{Class: cl, Owner: d.Owner, Span: d.ClassSpan, Methods: map[string]string{}}
	for _, cm := range cl.Methods {
		surface := types.SurfaceName(cm.Name)
		m := given[surface]
		if m == nil {
			errs = append(errs, diag.Errorf(d.ClassSpan, "MISSING METHOD", "A deriver for `%s` requires method `%s`.", types.SurfaceName(cl.Name), surface))
			continue
		}
		delete(given, surface)
		want := code
		for i := methodArity(cm.Type); i > 0; i-- {
			want = &types.TFun{Arg: code, Ret: want}
		}
		want = &types.TFun{Arg: infoType, Ret: want}
		m.Name = deriverSymbol(d.Owner, cl.Name, surface)
		info.Methods[surface] = m.Name
		errs = append(errs, ck.StageDecl(m)...)
		declInfo, es := ck.annotatedDecl(m, want)
		errs = append(errs, es...)
		ck.BindDecl(declInfo)
		ck.Checked = append(ck.Checked, declInfo)
	}
	for _, m := range given {
		errs = append(errs, diag.Errorf(m.NameSpan, "UNKNOWN METHOD", "Class `%s` has no method `%s`.", types.SurfaceName(cl.Name), m.Name))
	}
	if len(errs) > 0 {
		return errs
	}
	ck.Derivers[cl.Name] = info
	return nil
}

// methodArity counts a class method's arrows. It is the number of Code
// arguments its deriver receives, one per runtime parameter.
func methodArity(t types.Type) int {
	n := 0
	for {
		fn, ok := t.(*types.TFun)
		if !ok {
			return n
		}
		n++
		t = fn.Ret
	}
}

// DeriveDecl runs the deriver for each class in a `deriving` clause and
// installs the instances it produces.
func (ck *Checker) DeriveDecl(td *ast.TypeDecl) ([]DeclInfo, []diag.Error) {
	var infos []DeclInfo
	var errs []diag.Error
	con, ok := ck.TypeNames[td.Name].(*types.TCon)
	if !ok {
		return nil, errs
	}
	adt := ck.ADTs[con.Unique]
	for _, derive := range td.Deriving {
		cl := ck.Classes[derive.Name]
		if cl == nil {
			errs = append(errs, diag.Errorf(derive.Sp, "CANNOT DERIVE", "I don't know a class named `%s`.", derive.Name))
			continue
		}
		deriver := ck.Derivers[cl.Name]
		if deriver == nil {
			errs = append(errs, diag.Errorf(derive.Sp, "CANNOT DERIVE",
				"There is no deriver for `%s`, so I cannot generate its methods.\nA class becomes derivable through a `deriver %s` declaration.",
				types.SurfaceName(cl.Name), types.SurfaceName(cl.Name)))
			continue
		}
		if !ck.deriverVisible(deriver) {
			errs = append(errs, diag.Errorf(derive.Sp, "CANNOT DERIVE",
				"The deriver for `%s` lives in `%s`, which this module does not depend on.",
				types.SurfaceName(cl.Name), deriver.Owner))
			continue
		}
		in, es := ck.derivedInstance(td, adt, cl, deriver, derive.Sp)
		errs = append(errs, derivedFrom(es, cl.Name, derive.Sp)...)
		if in == nil {
			continue
		}
		ds, es := ck.instanceWithInferredContext(in)
		infos = append(infos, ds...)
		errs = append(errs, derivedFrom(es, cl.Name, derive.Sp)...)
	}
	return infos, errs
}

// derivedFrom points a diagnostic back at the `deriving` clause that caused
// it. Generated code keeps the spans of the quotes it came from, which is
// what a deriver author needs; the reader of the type being derived needs the
// other end of that line.
func derivedFrom(errs []diag.Error, class string, sp source.Span) []diag.Error {
	if sp.File == nil {
		return errs
	}
	at := sp.StartPos()
	for i := range errs {
		if errs[i].Span.File == sp.File && errs[i].Span.Start == sp.Start {
			continue
		}
		errs[i].Notes = append(errs[i].Notes, fmt.Sprintf("This code was generated by `deriving (%s)` at %s:%d:%d.",
			types.SurfaceName(class), sp.File.Name, at.Line, at.Col))
	}
	return errs
}

// deriverVisible applies the ordinary instance-visibility rule to derivers.
// A module that derives depends on the module that supplies the generator,
// which the loader arranges for the bundled derivers.
func (ck *Checker) deriverVisible(d *DeriverInfo) bool {
	if ck.InstanceImports == nil || d.Owner == "" || d.Owner == ck.CurrentOwner {
		return true
	}
	return ck.InstanceImports[ck.CurrentOwner][d.Owner]
}

// derivedInstance runs the generator once per method and assembles the
// instance declaration. Nothing here is class-specific: the shape of the call
// is dictated by the class method's arity alone.
func (ck *Checker) derivedInstance(td *ast.TypeDecl, adt *types.ADTInfo, cl *types.ClassInfo, deriver *DeriverInfo, sp source.Span) (*ast.InstanceDecl, []diag.Error) {
	var errs []diag.Error
	repr := ck.Reflect(instantiatedHead(adt), nil)
	var methods []*ast.ValueDecl
	for _, cm := range cl.Methods {
		surface := types.SurfaceName(cm.Name)
		generator := deriver.Methods[surface]
		arity := methodArity(cm.Type)
		params := make([]ast.Pattern, arity)
		var operand ast.Expr = &ast.Var{Name: generator, Sp: sp}
		operand = &ast.App{Fn: operand, Arg: &ast.App{
			Fn:  &ast.Var{Name: InfoOfName, Sp: sp},
			Arg: &ast.MetaValue{Value: repr, Ty: ck.TypeNames[TypeReprName], Sp: sp},
		}}
		codeType, codeErrs := ck.codeType(sp)
		errs = append(errs, codeErrs...)
		for i := range params {
			name := fmt.Sprintf("_derived%d", i)
			params[i] = &ast.PVar{Name: name, Sp: sp}
			operand = &ast.App{Fn: operand, Arg: &ast.MetaValue{
				Value: &meta.Code{Template: -1, Direct: &ast.Var{Name: name, Sp: sp}},
				Ty:    codeType, Sp: sp,
			}}
		}
		code, es := ck.runSplice(operand, sp)
		errs = append(errs, es...)
		if code == nil {
			return nil, errs
		}
		body := ck.Templates.Expand(code)
		if body == nil {
			return nil, append(errs, diag.Errorf(sp, "CANNOT DERIVE",
				"The deriver for `%s` produced code I cannot expand.", types.SurfaceName(cl.Name)))
		}
		// The traversal skeleton has no source of its own; quoted fragments
		// keep the spans they were written with.
		meta.FillSpans(body, sp)
		methods = append(methods, &ast.ValueDecl{Name: surface, NameSpan: sp, Params: params, Body: body})
	}
	if len(errs) > 0 {
		return nil, errs
	}
	var args []ast.TypeExpr
	for _, p := range td.Params {
		args = append(args, &ast.TVarName{Name: p.Name, Sp: p.Sp})
	}
	var head ast.TypeExpr = &ast.TName{Name: td.Name, Sp: td.NameSpan}
	if len(args) > 0 {
		head = &ast.TApp{Name: td.Name, NameSp: td.NameSpan, Args: args}
	}
	return &ast.InstanceDecl{
		Head:    ast.PredExpr{Class: cl.Name, Ty: head, Sp: sp},
		Methods: methods,
		Owner:   symbolModule(td.Name),
	}, nil
}

// instantiatedHead is the type the deriver reflects: the declared type
// applied to its own parameters. Those parameters are rigid variables, so a
// field mentioning one reflects as a variable and `Meta.info` on it is
// `Opaque` — which is exactly the signal a deriver needs to emit a method
// call instead of recursing.
func instantiatedHead(adt *types.ADTInfo) types.Type {
	if len(adt.Params) == 0 {
		return adt.Con
	}
	args := make([]types.Type, len(adt.Params))
	for i, p := range adt.Params {
		args[i] = p
	}
	return &types.TCon{Unique: adt.Con.Unique, Name: adt.Con.Name, Args: args}
}

// instanceWithInferredContext installs a generated instance whose context is
// read off its own body. A first pass records the predicates the generated
// methods leave residual; a second pass declares exactly those. Deriving used
// to over-approximate the context by scanning every constructor field, which
// demanded evidence for a field the generator never touched and for a phantom
// parameter that appears in no field at all.
func (ck *Checker) instanceWithInferredContext(d *ast.InstanceDecl) ([]DeclInfo, []diag.Error) {
	probe := *d
	probe.Methods = copyMethods(d.Methods)
	rollback := ck.Checkpoint()
	first := len(ck.Instances)
	collected := []types.Pred{}
	ck.inferringContext = &collected
	_, probeErrs := ck.InstanceDecl(&probe)
	discovered := ck.NormalizePreds(collected)
	var probed types.Type
	if len(ck.Instances) > first {
		probed = ck.Instances[first].Head
	}
	ck.inferringContext = nil
	rollback()
	if len(probeErrs) > 0 {
		return nil, probeErrs
	}
	names, ok := headVarNames(probed, d.Head.Ty)
	for _, p := range discovered {
		var te ast.TypeExpr
		if ok {
			te, ok = renderTypeExpr(p.Ty, names, d.Head.Sp)
		}
		if !ok {
			return nil, []diag.Error{diag.Errorf(d.Head.Sp, "CANNOT DERIVE",
				"The generated methods require `%s %s`, which I cannot express as an\ninstance constraint on this head.",
				types.SurfaceName(p.Class), types.Show(p.Ty))}
		}
		d.Preds = append(d.Preds, ast.PredExpr{Class: p.Class, Ty: te, Sp: d.Head.Sp})
	}
	return ck.InstanceDecl(d)
}

func copyMethods(ms []*ast.ValueDecl) []*ast.ValueDecl {
	out := make([]*ast.ValueDecl, len(ms))
	for i, m := range ms {
		n := *m
		n.Params = append([]ast.Pattern(nil), m.Params...)
		n.Body = meta.Rewrite(m.Body, func(ast.Expr) ast.Expr { return nil })
		out[i] = &n
	}
	return out
}

// headVarNames aligns the probe's resolved head with the surface head the
// context has to be written against. A derived context can only constrain the
// parameters the head names, which is what keeps the head compiler-owned.
func headVarNames(head types.Type, te ast.TypeExpr) (map[int]string, bool) {
	names := map[int]string{}
	app, ok := te.(*ast.TApp)
	if !ok {
		return names, true // a monomorphic head has no parameters to name
	}
	con, isCon := head.(*types.TCon)
	if !isCon || len(con.Args) != len(app.Args) {
		return nil, false
	}
	for i, arg := range app.Args {
		v, isVar := arg.(*ast.TVarName)
		tv, isTVar := con.Args[i].(*types.TVar)
		if !isVar || !isTVar {
			return nil, false
		}
		names[tv.ID] = v.Name
	}
	return names, true
}

func renderTypeExpr(t types.Type, names map[int]string, sp source.Span) (ast.TypeExpr, bool) {
	switch t := t.(type) {
	case *types.TVar:
		if n, ok := names[t.ID]; ok {
			return &ast.TVarName{Name: n, Sp: sp}, true
		}
		return nil, false
	case *types.TCon:
		if len(t.Args) == 0 {
			return &ast.TName{Name: t.Name, Sp: sp}, true
		}
		args := make([]ast.TypeExpr, len(t.Args))
		for i, a := range t.Args {
			te, ok := renderTypeExpr(a, names, sp)
			if !ok {
				return nil, false
			}
			args[i] = te
		}
		return &ast.TApp{Name: t.Name, NameSp: sp, Args: args}, true
	default:
		return nil, false
	}
}
