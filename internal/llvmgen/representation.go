package llvmgen

import (
	"fmt"
	"strings"

	"github.com/waj/fango/internal/types"
)

func (g *generator) representations() {
	for _, a := range g.p.ADTs {
		g.pointer[a.Con.Unique] = g.pointerADT(a)
	}
	for _, e := range g.p.Effects {
		g.reset()
		templ, args := g.params(e.Params)
		g.line("%s struct %s;", templ, symbol(e.Name)+"_ev")
		_ = args
	}
	for _, a := range g.p.ADTs {
		if a.Repr != types.ReprADT || a.Con.Name == types.FailureTypeName {
			continue
		}
		g.reset()
		templ, args := g.params(a.Params)
		base := symbol(a.Con.Name) + "_ad"
		g.line("%s struct %s;", templ, base)
		target := typeApply(base, args)
		if g.pointer[a.Con.Unique] {
			target += " *"
		}
		g.line("%s using %s=%s;", templ, symbol(a.Con.Name)+"_t", target)
	}
	done := map[int]bool{}
	var define func(*types.ADTInfo)
	define = func(a *types.ADTInfo) {
		if done[a.Con.Unique] || a.Repr != types.ReprADT || a.Con.Name == types.FailureTypeName {
			return
		}
		done[a.Con.Unique] = true
		var visit func(types.Type)
		visit = func(t types.Type) {
			switch t := t.(type) {
			case *types.TCon:
				child := g.adts[t.Unique]
				if child != nil && !g.pointer[child.Con.Unique] {
					define(child)
				}
			case *types.TFun:
				visit(t.Arg)
				visit(t.Ret)
			}
		}
		for _, c := range a.Ctors {
			for _, t := range c.Fields {
				visit(t)
			}
		}
		g.adt(a)
	}
	for _, a := range g.p.ADTs {
		define(a)
	}
	for _, e := range g.p.Effects {
		g.effect(e)
	}
	for _, a := range g.p.ADTs {
		if a.Repr != types.ReprADT || a.Con.Name == types.FailureTypeName {
			continue
		}
		g.reset()
		templ, args := g.params(a.Params)
		metadataTemplate := templ
		if metadataTemplate == "" {
			metadataTemplate = "template<>"
		}
		metadataType := typeApply(symbol(a.Con.Name)+"_ad", args)
		if g.pointer[a.Con.Unique] {
			metadataType += " *"
		}
		g.line("%s struct fg_metadata<%s>{static void describe(fg_descriptor &descriptor){descriptor.name=%s;", metadataTemplate, metadataType, stringValue(a.Con.Name))
		if len(args) > 0 {
			var values []string
			for _, arg := range args {
				values = append(values, "fg_type<"+arg+">()")
			}
			g.line("static const fg_descriptor *arguments[]={%s};descriptor.count=%d;descriptor.arguments=arguments;", strings.Join(values, ","), len(args))
		}
		g.line("}};")
		if len(args) == 0 {
			continue
		}
		ty := typeApply(symbol(a.Con.Name)+"_ad", args)
		shapeArgs := make([]string, len(args))
		for i := range shapeArgs {
			shapeArgs[i] = "fg_any"
		}
		shape := typeApply(symbol(a.Con.Name)+"_ad", shapeArgs)
		if g.pointer[a.Con.Unique] {
			ty += " *"
			shape += " *"
		}
		g.line("%s struct fg_shape<%s>{using type=%s;};", templ, ty, shape)
	}
	for _, a := range g.p.ADTs {
		if a.Repr == types.ReprADT && a.Con.Name != types.FailureTypeName && len(a.Params) > 0 {
			g.adtConversion(a)
		}
	}
	for _, a := range g.p.ADTs {
		if a.Repr != types.ReprADT || a.Con.Name == types.FailureTypeName {
			continue
		}
		g.reset()
		templ, args := g.params(a.Params)
		if templ == "" {
			templ = "template<>"
		}
		ty := typeApply(symbol(a.Con.Name)+"_ad", args)
		if g.pointer[a.Con.Unique] {
			ty += " *"
		}
		condition := "false"
		if types.InspectionShapeSafe(&types.TCon{Unique: a.Con.Unique, Name: a.Con.Name, Args: varTypes(a.Params)}, g.adts) {
			condition = "true"
			for _, arg := range args {
				condition += " && fg_inspection<" + arg + ">::value"
			}
		}
		g.line("%s struct fg_inspection<%s>:std::bool_constant<%s>{};", templ, ty, condition)
	}
	// Declare every overload before defining bodies, so nested nominal fields
	// use their nominal equality/display rather than the scalar fallback.
	for _, a := range g.p.ADTs {
		if a.Repr == types.ReprADT && a.Con.Name != types.FailureTypeName {
			g.reset()
			templ, _ := g.params(a.Params)
			ty := g.typ(&types.TCon{Unique: a.Con.Unique, Name: a.Con.Name, Args: varTypes(a.Params)})
			g.line("%s bool fg_eq(%s a,%s b);", templ, ty, ty)
			g.line("%s fg_string fg_show(%s value);", templ, ty)
			g.line("%s fg_string fg_show_nested(%s value);", templ, ty)
		}
	}
	for _, a := range g.p.ADTs {
		if a.Repr == types.ReprADT && a.Con.Name != types.FailureTypeName {
			g.adtOps(a)
		}
	}
}
func (g *generator) adtConversion(a *types.ADTInfo) {
	g.reset()
	var declarations, to, from []string
	for i, v := range a.Params {
		if v.Kind == types.RowVar {
			g.names[v.ID] = "fg_unit"
			continue
		}
		t, f := fmt.Sprintf("fg_To%d", i), fmt.Sprintf("fg_From%d", i)
		declarations = append(declarations, "class "+t, "class "+f)
		to = append(to, t)
		from = append(from, f)
		g.names[v.ID] = t
	}
	if len(to) == 0 {
		return
	}
	base := symbol(a.Con.Name) + "_ad"
	toTy, fromTy := typeApply(base, to), typeApply(base, from)
	if g.pointer[a.Con.Unique] {
		toTy += " *"
		fromTy += " *"
	}
	g.line("template<%s> struct fg_conversion<%s,%s>{static %s apply(%s value){switch(%s){", strings.Join(declarations, ","), toTy, fromTy, toTy, fromTy, g.tag("value", a))
	for _, c := range a.Ctors {
		var args []string
		for i, t := range c.Fields {
			args = append(args, "fg_convert<"+g.typ(t)+">("+g.member("value", a, c.Index, i)+")")
		}
		g.line("case %d:return %s(%s);", c.Index, typeApply(symbol(c.Name), to), strings.Join(args, ","))
	}
	g.line("}fango_panic(\"invalid conversion tag\");return {};}};")
}
func varTypes(vs []*types.TVar) []types.Type {
	out := make([]types.Type, len(vs))
	for i, v := range vs {
		out[i] = v
	}
	return out
}
func (g *generator) pointerADT(a *types.ADTInfo) bool {
	if a.Repr != types.ReprADT {
		return false
	}
	if len(a.Ctors) > 1 && a.Con.Name != "Maybe.Maybe" && a.Con.Name != "Result.Result" {
		for _, c := range a.Ctors {
			if len(c.Fields) > 0 {
				return true
			}
		}
		return false
	}
	functionCount := 0
	for _, c := range a.Ctors {
		for _, t := range c.Fields {
			if _, ok := t.(*types.TFun); ok {
				functionCount++
			}
		}
	}
	if functionCount >= 3 {
		return true
	}
	visiting := map[int]bool{}
	var reaches func(types.Type) bool
	reaches = func(t types.Type) bool {
		c, ok := t.(*types.TCon)
		if !ok {
			return false
		}
		if c.Unique == a.Con.Unique {
			return true
		}
		child := g.adts[c.Unique]
		if child == nil || child.Repr != types.ReprADT || len(child.Ctors) > 1 && child.Con.Name != "Maybe.Maybe" && child.Con.Name != "Result.Result" || visiting[c.Unique] {
			return false
		}
		visiting[c.Unique] = true
		defer delete(visiting, c.Unique)
		for _, ctor := range child.Ctors {
			for _, f := range child.InstFields(ctor, c.Args) {
				if reaches(f) {
					return true
				}
			}
		}
		return false
	}
	for _, c := range a.Ctors {
		for _, t := range c.Fields {
			if reaches(t) {
				return true
			}
		}
	}
	return false
}
func field(c, i int) string { return fmt.Sprintf("f%d_%d", c, i) }
func (g *generator) member(value string, a *types.ADTInfo, c, i int) string {
	sep := "."
	if g.pointer[a.Con.Unique] {
		sep = "->"
	}
	if len(a.Ctors) > 1 {
		return fmt.Sprintf("(%s)%spayload.c%d.%s", value, sep, c, field(c, i))
	}
	return "(" + value + ")" + sep + field(c, i)
}
func (g *generator) tag(value string, a *types.ADTInfo) string {
	sep := "."
	if g.pointer[a.Con.Unique] {
		sep = "->"
	}
	return "(" + value + ")" + sep + "tag"
}
func (g *generator) adt(a *types.ADTInfo) {
	g.reset()
	templ, args := g.params(a.Params)
	base := symbol(a.Con.Name) + "_ad"
	g.line("%s struct %s {", templ, base)
	g.line("uint32_t tag=0;")
	union := len(a.Ctors) > 1
	hasPayload := false
	for _, c := range a.Ctors {
		hasPayload = hasPayload || len(c.Fields) > 0
	}
	if union && hasPayload {
		g.line("union payload_u {")
	}
	for _, c := range a.Ctors {
		if union && len(c.Fields) > 0 {
			g.line("struct ctor%d {", c.Index)
		}
		for i, t := range c.Fields {
			g.line("%s %s{};", g.typ(t), field(c.Index, i))
		}
		if union && len(c.Fields) > 0 {
			g.line("} c%d;", c.Index)
		}
	}
	if union && hasPayload {
		g.line("payload_u(){}\n} payload;")
	}
	g.line("};")
	ty := g.typ(&types.TCon{Unique: a.Con.Unique, Name: a.Con.Name, Args: varTypes(a.Params)})
	for _, c := range a.Ctors {
		var ps []string
		for i, t := range c.Fields {
			ps = append(ps, g.typ(t)+fmt.Sprintf(" fg_a%d", i))
		}
		g.line("%s %s %s(%s){", templ, ty, symbol(c.Name), strings.Join(ps, ","))
		g.line("%s fg_value{};fg_value.tag=%d;", typeApply(base, args), c.Index)
		if union && len(c.Fields) > 0 {
			g.line("new(&fg_value.payload.c%d) decltype(fg_value.payload.c%d){};", c.Index, c.Index)
		}
		for i := range c.Fields {
			if union {
				g.line("fg_value.payload.c%d.%s=fg_a%d;", c.Index, field(c.Index, i), i)
			} else {
				g.line("fg_value.%s=fg_a%d;", field(c.Index, i), i)
			}
		}
		if g.pointer[a.Con.Unique] {
			g.line("return fg_new(fg_value);")
		} else {
			g.line("return fg_value;")
		}
		g.line("}")
	}
}
func (g *generator) adtOps(a *types.ADTInfo) {
	g.reset()
	templ, _ := g.params(a.Params)
	ty := g.typ(&types.TCon{Unique: a.Con.Unique, Name: a.Con.Name, Args: varTypes(a.Params)})
	g.line("%s bool fg_eq(%s a,%s b){if(%s!=%s)return false;switch(%s){", templ, ty, ty, g.tag("a", a), g.tag("b", a), g.tag("a", a))
	for _, c := range a.Ctors {
		var xs []string
		for i := range c.Fields {
			xs = append(xs, "fg_eq("+g.member("a", a, c.Index, i)+","+g.member("b", a, c.Index, i)+")")
		}
		if len(xs) == 0 {
			xs = []string{"true"}
		}
		g.line("case %d:return %s;", c.Index, strings.Join(xs, "&&"))
	}
	g.line("}fango_panic(\"invalid constructor tag\");return false;}")
	name := symbol(a.Con.Name) + "_show"
	g.line("%s fg_string %s(%s value,bool nested){switch(%s){", templ, name, ty, g.tag("value", a))
	for _, c := range a.Ctors {
		g.line("case %d:{fg_string s=%s;", c.Index, stringValue(types.SurfaceName(c.Name)))
		for i := range c.Fields {
			g.line("s=fg_append(fg_append(s,%s),fg_show_nested(%s));", stringValue(" "), g.member("value", a, c.Index, i))
		}
		if len(c.Fields) > 0 {
			g.line("if(nested)s=fg_append(fg_append(%s,s),%s);", stringValue("("), stringValue(")"))
		}
		g.line("return s;}")
	}
	g.line("}fango_panic(\"invalid constructor tag\");return {};}")
	g.line("%s fg_string fg_show(%s value){return %s(value,false);}", templ, ty, name)
	g.line("%s fg_string fg_show_nested(%s value){return %s(value,true);}", templ, ty, name)
}
func (g *generator) effect(e *types.EffectInfo) {
	g.reset()
	templ, _ := g.params(e.Params)
	g.line("%s struct %s:fg_evidence {", templ, symbol(e.Name)+"_ev")
	for _, op := range e.Ops {
		if op.Abort {
			continue
		}
		for _, v := range op.LocalVars {
			g.names[v.ID] = "fg_any"
		}
		var args []string
		if len(op.LocalVars) > 0 {
			args = append(args, "const fg_descriptor **")
		}
		for _, t := range op.ParamTypes {
			args = append(args, g.typ(t))
		}
		g.line("fg_fn<%s(%s)> op%d{};", g.typ(op.ResultType), strings.Join(args, ","), op.Index)
	}
	g.line("};")
}
func (g *generator) ctor(c *types.CtorInfo, t types.Type, args []string) string {
	a := g.adts[c.Result.Unique]
	if c.Result.Unique == g.b.Bool.Unique {
		if c.Name == "True" {
			return "true"
		}
		return "false"
	}
	switch c.Repr {
	case types.ReprList:
		if len(args) == 0 {
			return g.typ(t) + "{}"
		}
		return "fg_cons<" + g.typ(t.(*types.TCon).Args[0]) + ">(" + strings.Join(args, ",") + ")"
	case types.ReprBytes:
		return "fg_bytes{}"
	case types.ReprNativeAny:
		return "nullptr"
	}
	con := t.(*types.TCon)
	return typeApply(symbol(c.Name), g.adtArgs(a, con.Args)) + "(" + strings.Join(args, ",") + ")"
}
