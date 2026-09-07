package infer

import (
	"fmt"
	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/types"
)

// Deriving produces ordinary instance methods, checked and elaborated through
// exactly the same path as handwritten implementations.
func (ck *Checker) DeriveDecl(td *ast.TypeDecl) ([]DeclInfo, []diag.Error) {
	var infos []DeclInfo
	var errs []diag.Error
	adt := ck.ADTs[ck.TypeNames[td.Name].(*types.TCon).Unique]
	for _, derive := range td.Deriving {
		cl := ck.Classes[derive.Name]
		if cl == nil || (cl.Name != "Basics.Eq" && cl.Name != "Basics.Show") {
			errs = append(errs, diag.Errorf(derive.Sp, "CANNOT DERIVE", "Only the standard Eq and Show classes can be derived."))
			continue
		}
		var ctx []ast.PredExpr
		// Keep polymorphic field evidence intact, so the caller selects
		// specializations. Direct recursive fields use the instance itself.
		for i, c := range adt.Ctors {
			for j, f := range c.Fields {
				if !hasTypeVars(f) {
					continue
				}
				if self, ok := f.(*types.TCon); ok && self.Unique == adt.Con.Unique {
					continue
				}
				var te ast.TypeExpr
				if adt.IsRecord() {
					te = td.RecordFields[j].Type
				} else {
					te = td.Ctors[i].Args[j]
				}
				ctx = append(ctx, ast.PredExpr{Class: cl.Name, Ty: te, Sp: derive.Sp})
			}
		}
		var args []ast.TypeExpr
		for _, p := range td.Params {
			v := &ast.TVarName{Name: p.Name, Sp: p.Sp}
			args = append(args, v)
		}
		var head ast.TypeExpr = &ast.TName{Name: td.Name, Sp: td.NameSpan}
		if len(args) > 0 {
			head = &ast.TApp{Name: td.Name, NameSp: td.NameSpan, Args: args}
		}
		sp := td.NameSpan
		ref := func(n string) ast.Expr { return &ast.Var{Name: n, Sp: sp} }
		call := func(n string, args ...ast.Expr) ast.Expr {
			var e ast.Expr = ref(n)
			for _, a := range args {
				e = &ast.App{Fn: e, Arg: a}
			}
			return e
		}
		str := func(s string) ast.Expr { return &ast.StringLit{Value: s, Sp: sp} }
		boolean := func(b bool) ast.Expr {
			n := "False"
			if b {
				n = "True"
			}
			return &ast.Ctor{Name: n, Sp: sp}
		}
		pattern := func(c *types.CtorInfo, prefix string) ast.Pattern {
			p := &ast.PCtor{Name: c.Name, NameSpan: sp}
			for i := range c.Fields {
				p.Args = append(p.Args, &ast.PVar{Name: fmt.Sprintf("%s%d", prefix, i), Sp: sp})
			}
			return p
		}
		method := &ast.ValueDecl{Name: types.SurfaceName(cl.Methods[0].Name), NameSpan: sp, Params: []ast.Param{{Name: "_derivedX", Sp: sp}}}
		body := &ast.Case{Scrutinee: ref("_derivedX"), Sp: sp}
		if cl.Name == "Basics.Eq" {
			method.Params = append(method.Params, ast.Param{Name: "_derivedY", Sp: sp})
		}
		for _, c := range adt.Ctors {
			var branch ast.Expr
			if cl.Name == "Basics.Show" {
				if adt.IsRecord() {
					branch = str(types.SurfaceName(adt.Con.Name) + " {")
					for i, field := range adt.RecordFields {
						sep := " "
						if i > 0 {
							sep = ", "
						}
						prefix := sep + field.Name + " = "
						branch = call("Basics.append", branch, call("Basics.append", str(prefix), call("Basics.show", ref(fmt.Sprintf("_fieldX%d", i)))))
					}
					branch = call("Basics.append", branch, str(" }"))
				} else {
					branch = str(types.SurfaceName(c.Name))
					for i := range c.Fields {
						branch = call("Basics.append", branch, call("Basics.append", str(" "), call("Basics.show", ref(fmt.Sprintf("_fieldX%d", i)))))
					}
				}
			} else {
				inner := &ast.Case{Scrutinee: ref("_derivedY"), Sp: sp}
				for _, other := range adt.Ctors {
					result := boolean(c.Index == other.Index)
					if c.Index == other.Index {
						for i := len(c.Fields) - 1; i >= 0; i-- {
							result = &ast.If{Cond: call("Basics.eq", ref(fmt.Sprintf("_fieldX%d", i)), ref(fmt.Sprintf("_fieldY%d", i))), Then: result, Else: boolean(false), Sp: sp}
						}
					}
					pat := pattern(other, "_fieldY")
					inner.Branches = append(inner.Branches, ast.CaseBranch{Pattern: pat, Body: result})
				}
				branch = inner
			}
			body.Branches = append(body.Branches, ast.CaseBranch{Pattern: pattern(c, "_fieldX"), Body: branch})
		}
		method.Body = body
		in := &ast.InstanceDecl{Head: ast.PredExpr{Class: cl.Name, Ty: head, Sp: derive.Sp}, Preds: ctx, Methods: []*ast.ValueDecl{method}, Owner: symbolModule(td.Name)}
		ds, es := ck.InstanceDecl(in)
		infos = append(infos, ds...)
		errs = append(errs, es...)
	}
	return infos, errs
}
