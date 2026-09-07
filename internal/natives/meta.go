package natives

// The `Meta` module's primitives. They fall into three groups: lifting a
// scalar into code, reading a reflected type's schema, and building the
// traversal skeleton `Meta.match` and `Meta.construct` assemble
// (doc/design.md, "Compile-time metaprogramming").
//
// Every one of them is a pure function of its arguments, and none of them
// constructs a fango ADT value: lists and records are built by `Meta`'s own
// fango code from the counts and indices these return. That is what keeps
// this package below the interpreter in the graph.

import (
	"fmt"
	"hash/fnv"
	"strings"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/meta"
	"github.com/waj/fango/internal/types"
)

func installMeta(t map[string]Spec) {
	t["Meta.liftInt"] = liftSpec(func(v any) ast.Expr { return &ast.IntLit{Value: v.(int64), Raw: true} })
	t["Meta.liftFloat"] = liftSpec(func(v any) ast.Expr { return &ast.FloatLit{Value: v.(float64)} })
	t["Meta.liftString"] = liftSpec(func(v any) ast.Expr { return &ast.StringLit{Value: v.(string)} })
	t["Meta.liftChar"] = liftSpec(func(v any) ast.Expr { return &ast.CharLit{Value: v.(rune)} })
	t["Meta.liftBool"] = liftSpec(func(v any) ast.Expr {
		if v.(bool) {
			return &ast.Ctor{Name: "True"}
		}
		return &ast.Ctor{Name: "False"}
	})
	t["Meta.liftUnit"] = liftSpec(func(any) ast.Expr { return &ast.UnitLit{} })

	// --- reflection ---------------------------------------------------

	t["Meta.sameType"] = pure2(func(a, b any) any {
		return types.Equal(repr(a).Type, repr(b).Type)
	})
	t["Meta.head"] = pure1(func(a any) any {
		r := repr(a)
		if c := r.Con(); c != nil && len(c.Args) > 0 {
			return r.Derive(&types.TCon{Unique: c.Unique, Name: c.Name})
		}
		return r
	})
	t["Meta.isVar"] = pure1(func(a any) any {
		_, ok := repr(a).Type.(*types.TVar)
		return ok
	})
	t["Meta.typeName"] = pure1(func(a any) any { return types.Show(repr(a).Type) })
	t["Meta.argCount"] = pure1(func(a any) any {
		if c := repr(a).Con(); c != nil {
			return int64(len(c.Args))
		}
		return int64(0)
	})
	t["Meta.argAt"] = pure2(func(a, i any) any {
		r := repr(a)
		c := r.Con()
		n := int(i.(int64))
		if c == nil || n < 0 || n >= len(c.Args) {
			return r
		}
		return r.Derive(c.Args[n])
	})
	// A schema is readable only where the reflection site could have read it
	// by hand: `exposing (T)` reflects opaque, `exposing (T(..))` reflects in
	// full, the same rule that already governs constructors and record fields.
	t["Meta.schemaVisible"] = pure1(func(a any) any { return repr(a).ADT() != nil })
	t["Meta.typeSurfaceName"] = pure1(func(a any) any {
		if c := repr(a).Con(); c != nil {
			return types.SurfaceName(c.Name)
		}
		return ""
	})
	t["Meta.typeModuleName"] = pure1(func(a any) any {
		if c := repr(a).Con(); c != nil {
			if i := strings.LastIndexByte(c.Name, '.'); i >= 0 {
				return c.Name[:i]
			}
		}
		return ""
	})
	t["Meta.isRecordType"] = pure1(func(a any) any {
		adt := repr(a).ADT()
		return adt != nil && adt.IsRecord()
	})
	t["Meta.ctorCount"] = pure1(func(a any) any {
		if adt := repr(a).ADT(); adt != nil {
			return int64(len(adt.Ctors))
		}
		return int64(0)
	})
	t["Meta.ctorSymbol"] = pure2(func(a, i any) any {
		if c := ctorAt(repr(a), i); c != nil {
			return c.Name
		}
		return ""
	})
	t["Meta.ctorName"] = pure2(func(a, i any) any {
		r := repr(a)
		c := ctorAt(r, i)
		if c == nil {
			return ""
		}
		if adt := r.ADT(); adt != nil && adt.IsRecord() {
			return types.SurfaceName(adt.Con.Name)
		}
		return types.SurfaceName(c.Name)
	})
	t["Meta.fieldCount"] = pure2(func(a, i any) any {
		if c := ctorAt(repr(a), i); c != nil {
			return int64(len(c.Fields))
		}
		return int64(0)
	})
	// A union constructor's fields are positional, so their name is empty.
	t["Meta.fieldName"] = pure3(func(a, i, j any) any {
		r := repr(a)
		adt := r.ADT()
		n := int(j.(int64))
		if adt == nil || !adt.IsRecord() || n < 0 || n >= len(adt.RecordFields) {
			return ""
		}
		return adt.RecordFields[n].Name
	})
	// Field types come back instantiated at the reflected type's arguments,
	// so a deriver sees `Int`, not the declaration's parameter.
	t["Meta.fieldType"] = pure3(func(a, i, j any) any {
		r := repr(a)
		adt, c := r.ADT(), ctorAt(repr(a), i)
		n := int(j.(int64))
		if adt == nil || c == nil || n < 0 || n >= len(c.Fields) {
			return r
		}
		return r.Derive(types.SubstRigid(c.Fields[n], adt.ParamSubst(argsOf(r))))
	})

	// --- code construction --------------------------------------------

	// Binder names are compiler-owned: a deriver never invents one. Deriving
	// them from the scrutinee keeps nested traversals — `Eq`'s two scrutinees
	// — from colliding, without a mutable counter that would make expansion
	// order-dependent.
	t["Meta.binderName"] = Spec{Arity: 3, Eval: func(rt *Runtime, args []any) (any, error) {
		tree := rt.Expand(args[0].(*meta.Code))
		if tree == nil {
			return nil, fmt.Errorf("Meta.match needs a scrutinee that is an expression")
		}
		text := ast.DumpExpr(tree)
		h := fnv.New32a()
		h.Write([]byte(text))
		return fmt.Sprintf("_d%s%08x_%d_%d", sanitize(text), h.Sum32(), args[1].(int64), args[2].(int64)), nil
	}}
	t["Meta.varCode"] = pure1(func(a any) any {
		return &meta.Code{Template: -1, Direct: &ast.Var{Name: a.(string)}}
	})
	t["Meta.matchStart"] = expand1(func(scrutinee ast.Expr) (any, error) {
		return &meta.Code{Template: -1, Direct: &ast.Case{Scrutinee: scrutinee}}, nil
	})
	t["Meta.patternStart"] = pure1(func(a any) any {
		return &meta.Code{Template: -1, Pattern: &ast.PCtor{Name: a.(string)}}
	})
	t["Meta.patternBind"] = Spec{Arity: 2, Eval: func(_ *Runtime, args []any) (any, error) {
		p, ok := args[0].(*meta.Code).Pattern.(*ast.PCtor)
		if !ok {
			return nil, fmt.Errorf("Meta.match built a malformed pattern")
		}
		next := *p
		next.Args = append(append([]ast.Pattern(nil), p.Args...), &ast.PVar{Name: args[1].(string)})
		return &meta.Code{Template: -1, Pattern: &next}, nil
	}}
	t["Meta.matchBranch"] = Spec{Arity: 3, Eval: func(rt *Runtime, args []any) (any, error) {
		sofar, ok := args[0].(*meta.Code).Direct.(*ast.Case)
		pat, isPat := args[1].(*meta.Code).Pattern.(*ast.PCtor)
		body := rt.Expand(args[2].(*meta.Code))
		if !ok || !isPat || body == nil {
			return nil, fmt.Errorf("Meta.match built a malformed branch")
		}
		next := *sofar
		next.Branches = append(append([]ast.CaseBranch(nil), sofar.Branches...),
			ast.CaseBranch{Pattern: meta.CopyPattern(pat), Body: body})
		return &meta.Code{Template: -1, Direct: &next}, nil
	}}
	// Construction is the other direction. A record's sole constructor is an
	// internal representation detail, so building one produces a record
	// literal and Pending tracks the field names still to be filled.
	t["Meta.constructStart"] = pure2(func(a, i any) any {
		r := repr(a)
		c := ctorAt(r, i)
		if c == nil {
			return &meta.Code{Template: -1, Direct: &ast.Var{Name: "_metaUnknownConstructor"}}
		}
		if adt := r.ADT(); adt != nil && adt.IsRecord() {
			names := make([]string, len(adt.RecordFields))
			for i, f := range adt.RecordFields {
				names[i] = f.Name
			}
			return &meta.Code{Template: -1, Direct: &ast.RecordLit{Name: adt.Con.Name}, Pending: names}
		}
		return &meta.Code{Template: -1, Direct: &ast.Ctor{Name: c.Name}}
	})
	t["Meta.constructPush"] = Spec{Arity: 2, Eval: func(rt *Runtime, args []any) (any, error) {
		partial := args[0].(*meta.Code)
		value := rt.Expand(args[1].(*meta.Code))
		if partial.Direct == nil || value == nil {
			return nil, fmt.Errorf("Meta.construct built a malformed value")
		}
		if lit, ok := partial.Direct.(*ast.RecordLit); ok {
			if len(partial.Pending) == 0 {
				return nil, fmt.Errorf("Meta.construct was given more values than the record has fields")
			}
			next := *lit
			next.Fields = append(append([]ast.RecordExprField(nil), lit.Fields...),
				ast.RecordExprField{Name: partial.Pending[0], Value: value})
			return &meta.Code{Template: -1, Direct: &next, Pending: partial.Pending[1:]}, nil
		}
		return &meta.Code{Template: -1, Direct: &ast.App{Fn: rt.Expand(partial), Arg: value}}, nil
	}}
	t["Meta.fail"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) {
		return nil, fmt.Errorf("%s", args[0].(string))
	}}
}

func repr(v any) *meta.TypeRepr { return v.(*meta.TypeRepr) }

func argsOf(r *meta.TypeRepr) []types.Type {
	if c := r.Con(); c != nil {
		return c.Args
	}
	return nil
}

func ctorAt(r *meta.TypeRepr, i any) *types.CtorInfo {
	adt := r.ADT()
	n := int(i.(int64))
	if adt == nil || n < 0 || n >= len(adt.Ctors) {
		return nil
	}
	return adt.Ctors[n]
}

// sanitize keeps a readable trace of the scrutinee in the binder name; the
// hash beside it is what makes the name unique.
func sanitize(text string) string {
	var b strings.Builder
	for _, r := range text {
		if b.Len() >= 12 {
			break
		}
		if r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func liftSpec(makeExpr func(any) ast.Expr) Spec {
	return Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) {
		return &meta.Code{Template: -1, Direct: makeExpr(args[0])}, nil
	}}
}

func pure1(f func(any) any) Spec {
	return Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) { return f(args[0]), nil }}
}

func pure2(f func(a, b any) any) Spec {
	return Spec{Arity: 2, Eval: func(_ *Runtime, args []any) (any, error) { return f(args[0], args[1]), nil }}
}

func pure3(f func(a, b, c any) any) Spec {
	return Spec{Arity: 3, Eval: func(_ *Runtime, args []any) (any, error) { return f(args[0], args[1], args[2]), nil }}
}

func expand1(f func(ast.Expr) (any, error)) Spec {
	return Spec{Arity: 1, Eval: func(rt *Runtime, args []any) (any, error) {
		tree := rt.Expand(args[0].(*meta.Code))
		if tree == nil {
			return nil, fmt.Errorf("this compile-time value is not an expression")
		}
		return f(tree)
	}}
}
