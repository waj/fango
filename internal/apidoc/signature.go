package apidoc

import (
	"sort"
	"strings"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/infer"
	"github.com/waj/fango/internal/types"
)

// Signature renders a named value's checked scheme as `name : type`, with
// constraints and the effect rows its arrows perform. Operators are
// parenthesized as they are written in an annotation.
func Signature(name string, sch types.Scheme, p *types.Printer) string {
	return ast.Spelling(types.SurfaceName(name)) + " : " + p.Scheme(sch)
}

// Typed renders a monomorphic member type, such as a constructor's value
// type or a record field's, as `name : type`.
func Typed(name string, t types.Type, p *types.Printer) string {
	return ast.Spelling(types.SurfaceName(name)) + " : " + p.Type(t)
}

// ScopedSignature is Signature for a scoped runner, preceded by the pragma
// that binds its callback row. The row is shown wherever it occurs, as it is
// in the runner's annotation.
func ScopedSignature(name string, sch types.Scheme, p *types.Printer) string {
	if sch.ScopedRow == nil {
		return Signature(name, sch, p)
	}
	p.Bind(sch.ScopedRow, "s")
	return "{-# scoped s #-}\n" + Signature(name, sch, p)
}

// Head renders a declaration head such as `type Result error value`.
func Head(keyword, name string, params []ast.Param) string {
	text := keyword + " " + types.SurfaceName(name)
	for _, p := range params {
		text += " " + p.Name
	}
	return text
}

// bindParams names a declaration's checked parameters after its source ones.
func bindParams(p *types.Printer, vars []*types.TVar, params []ast.Param) {
	for i, v := range vars {
		if i < len(params) && v != nil {
			p.Bind(v, params[i].Name)
		}
	}
}

// InstanceHead renders a checked instance as it is declared, context first:
// `Show a => Show (Maybe a)`. Derived instances read the same way.
func InstanceHead(instance *infer.InstanceInfo) string {
	return instanceHead(instance, types.NewPrinter())
}

func instanceHead(instance *infer.InstanceInfo, p *types.Printer) string {
	head := p.Pred(instance.Class.Name, instance.Head)
	if len(instance.Preds) == 0 {
		return head
	}
	parts := make([]string, len(instance.Preds))
	for i, pred := range instance.Preds {
		parts[i] = p.Pred(pred.Class, pred.Ty)
	}
	context := strings.Join(parts, ", ")
	if len(parts) > 1 {
		context = "(" + context + ")"
	}
	return context + " => " + head
}

// instancesOf collects the heads of instances accepted by keep, rendered by
// printers from fresh, sorted by their text and without repeats. When params
// are given, a head applying the documented type to variables names them
// after its declared parameters. text spells a rendered head.
func instancesOf(ck *infer.Checker, keep func(*infer.InstanceInfo) bool, params []ast.Param, typeName func(string) string, fresh func() *types.Printer, text func(string) string) []string {
	seen := map[string]bool{}
	var out []string
	for _, instance := range ck.Instances {
		if instance.Class == nil || !keep(instance) {
			continue
		}
		p := fresh()
		p.TypeName = typeName
		if tc, ok := instance.Head.(*types.TCon); ok && len(params) > 0 {
			for i, arg := range tc.Args {
				if v, ok := arg.(*types.TVar); ok && i < len(params) {
					p.Bind(v, params[i].Name)
				}
			}
		}
		head := instanceHead(instance, p)
		if !seen[text(head)] {
			seen[text(head)] = true
			out = append(out, head)
		}
	}
	sort.Slice(out, func(a, b int) bool { return text(out[a]) < text(out[b]) })
	return out
}

// qualifier spells a type by its canonical name when another loaded type
// shares its surface name, so a list spanning modules keeps them apart.
func qualifier(ck *infer.Checker) func(string) string {
	owners := map[string]map[string]bool{}
	for _, adt := range ck.ADTs {
		surface := types.SurfaceName(adt.Con.Name)
		if owners[surface] == nil {
			owners[surface] = map[string]bool{}
		}
		owners[surface][adt.Con.Name] = true
	}
	return func(name string) string {
		surface := types.SurfaceName(name)
		if len(owners[surface]) > 1 {
			return name
		}
		return surface
	}
}
