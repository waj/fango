package llvmgen

import (
	"fmt"
	goast "go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strconv"
	"strings"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

func (g *generator) native(call *core.NativeCall) string {
	n := g.p.Natives[call.Name]
	if n == nil {
		panic("unknown native " + call.Name)
	}
	var values []string
	for _, a := range call.Args {
		values = append(values, g.expr(a))
	}
	if n.Template != nil {
		return g.temp(call.Ty, g.template(call, *n.Template, values))
	}
	g.nativeUsed[n.Module] = true
	var args []string
	for i, value := range values {
		if n.Storage.Kind != "" && i == n.Storage.Payload {
			value = "fg_new(fg_box(" + value + "))"
		} else if i < len(n.ParamWrappers) && n.ParamWrappers[i] != nil {
			wrapper := n.ParamWrappers[i]
			value = g.member(value, g.adts[wrapper.Result.Unique], wrapper.Index, 0)
		} else if g.typ(call.Args[i].Type()) == "fg_unit" {
			continue
		}
		args = append(args, value)
	}
	resultType := call.Ty
	if n.Fallible != nil {
		resultType = call.Ty.(*types.TCon).Args[1]
	}
	wrapper := n.ResultWrapper
	rawType := g.typ(resultType)
	if wrapper != nil {
		con := resultType.(*types.TCon)
		a := g.adts[con.Unique]
		rawType = g.typ(a.InstFields(wrapper, con.Args)[0])
	}
	opaqueResult := n.Storage.Kind == "read"
	if opaqueResult {
		rawType = "void *"
	}
	invoke := func(args []string) string {
		return NativeSymbol(n.Module, types.SurfaceName(n.Name)) + "(" + strings.Join(args, ",") + ")"
	}
	var raw string
	if n.Fallible != nil {
		raw = g.fresh()
		if rawType != "fg_unit" {
			g.line("%s %s{};", rawType, raw)
			args = append(args, "&"+raw)
		} else {
			raw = "fg_unit{}"
		}
		failure := g.fresh()
		g.line("auto %s=%s;", failure, invoke(args))
		out := g.temp(call.Ty, "{}")
		shape := n.Fallible
		g.line("if(%s.failed){", failure)
		errType := call.Ty.(*types.TCon).Args[0].(*types.TCon)
		errorADT := g.adts[errType.Unique]
		fields := errorADT.InstFields(shape.Error, errType.Args)
		errorArgs := make([]string, len(fields))
		kindType := fields[shape.KindIdx].(*types.TCon)
		kindADT := g.adts[kindType.Unique]
		kind := g.temp(kindType, "{}")
		g.line("switch(%s.kind){", failure)
		for i, c := range shape.Kinds {
			g.line("case %d:%s=%s;break;", i, kind, g.ctor(c, kindType, nil))
		}
		g.line("default:%s=%s;break;}", kind, g.ctor(kindADT.Ctors[len(kindADT.Ctors)-1], kindType, nil))
		errorArgs[shape.KindIdx] = kind
		errorArgs[shape.LocationIdx] = failure + ".location"
		errorArgs[shape.MessageIdx] = failure + ".message"
		errorValue := g.ctor(shape.Error, errType, errorArgs)
		g.line("%s=%s;\n}else{", out, g.ctor(shape.Err, call.Ty, []string{errorValue}))
		if wrapper != nil {
			raw = g.ctor(wrapper, resultType, []string{raw})
		}
		g.line("%s=%s;\n}", out, g.ctor(shape.Ok, call.Ty, []string{raw}))
		return out
	}
	if rawType == "fg_unit" {
		g.line("%s;", invoke(args))
		return "fg_unit{}"
	}
	raw = g.fresh()
	g.line("%s %s=%s;", rawType, raw, invoke(args))
	if opaqueResult {
		raw = "fg_unbox<" + g.typ(resultType) + ">(*static_cast<fg_any *>(" + raw + "))"
	}
	if rawType == "fg_string" {
		g.line("if(!fango_valid_utf8(%s))fango_panic(\"native returned invalid UTF-8\");", raw)
	}
	if rawType == "uint32_t" {
		g.line("if(%s>0x10FFFF||(%s>=0xD800&&%s<=0xDFFF))fango_panic(\"native returned invalid Char\");", raw, raw, raw)
	}
	if wrapper != nil {
		raw = g.ctor(wrapper, resultType, []string{raw})
	}
	return g.temp(call.Ty, raw)
}

func (g *generator) template(call *core.NativeCall, tmpl string, args []string) string {
	s := strings.ReplaceAll(strings.ReplaceAll(tmpl, "$eq", "fg_eq"), "$show", "fg_show")
	for i := len(args); i >= 1; i-- {
		s = strings.ReplaceAll(s, fmt.Sprintf("$%d", i), fmt.Sprintf("fg_p%d", i))
	}
	root, err := parser.ParseExpr(s)
	if err != nil {
		panic(err)
	}
	var emit func(goast.Expr) string
	emit = func(e goast.Expr) string {
		switch e := e.(type) {
		case *goast.Ident:
			if strings.HasPrefix(e.Name, "fg_p") {
				i, _ := strconv.Atoi(strings.TrimPrefix(e.Name, "fg_p"))
				return args[i-1]
			}
			if e.Name == "nil" {
				return "nullptr"
			}
			return e.Name
		case *goast.BasicLit:
			if e.Kind == token.STRING {
				v, _ := strconv.Unquote(e.Value)
				return stringValue(v)
			}
			return e.Value
		case *goast.ParenExpr:
			return "(" + emit(e.X) + ")"
		case *goast.UnaryExpr:
			if e.Op == token.SUB {
				return "fg_sub(" + g.typ(call.Ty) + "(0)," + emit(e.X) + ")"
			}
			return "(" + e.Op.String() + emit(e.X) + ")"
		case *goast.BinaryExpr:
			left, right := emit(e.X), emit(e.Y)
			switch e.Op {
			case token.ADD:
				if g.typ(call.Ty) == "fg_string" {
					return "fg_append(" + left + "," + right + ")"
				}
				return "fg_add(" + left + "," + right + ")"
			case token.SUB:
				return "fg_sub(" + left + "," + right + ")"
			case token.MUL:
				return "fg_mul(" + left + "," + right + ")"
			case token.QUO:
				if g.typ(call.Ty) == "int64_t" {
					return "fg_quotient(" + left + "," + right + ")"
				}
			case token.REM:
				return "fg_remainder(" + left + "," + right + ")"
			case token.EQL:
				return "fg_eq(" + left + "," + right + ")"
			case token.NEQ:
				return "!fg_eq(" + left + "," + right + ")"
			}
			// String ordering is byte lexicographic, exactly as Go's valid UTF-8 strings.
			if len(call.Args) > 0 && g.typ(call.Args[0].Type()) == "fg_string" {
				return "(fg_compare(" + left + "," + right + ")" + e.Op.String() + "0)"
			}
			return "(" + left + e.Op.String() + right + ")"
		case *goast.CallExpr:
			var as []string
			for _, a := range e.Args {
				as = append(as, emit(a))
			}
			if id, ok := e.Fun.(*goast.Ident); ok {
				switch id.Name {
				case "float64":
					return "double(" + strings.Join(as, ",") + ")"
				case "fg_eq", "fg_show":
					return id.Name + "(" + strings.Join(as, ",") + ")"
				}
			}
			if sel, ok := e.Fun.(*goast.SelectorExpr); ok {
				return g.runtimeTemplate(sel.Sel.Name, as)
			}
		}
		panic(fmt.Sprintf("unsupported bundled LLVM template %s (%T)", tmpl, e))
	}
	return emit(root)
}
func (g *generator) runtimeTemplate(name string, a []string) string {
	call := func(name string) string { return name + "(" + strings.Join(a, ",") + ")" }
	switch name {
	case "ShowInt", "ShowFloat", "ShowBool", "ShowChar", "ShowString":
		return call("fg_show")
	case "StringFromList":
		return call("fg_string_from_list")
	case "StringConcat":
		return call("fg_string_concat")
	case "BytesLength":
		return "int64_t(" + a[0] + ".length)"
	case "BytesByteAt":
		return "((" + a[0] + ">=0&&uint64_t(" + a[0] + ")<" + a[1] + ".length)?int64_t(" + a[1] + ".data[" + a[0] + "]) : int64_t(-1))"
	case "BytesSlice":
		return call("fg_bytes_slice")
	case "BytesAppend":
		return call("fg_bytes_append")
	case "BytesConcat":
		return call("fg_bytes_concat")
	case "BytesIndexOf":
		return "fg_bytes_index(" + a[0] + ",0," + a[1] + ")"
	case "BytesIndexOfFrom":
		return "fg_bytes_index(" + a[1] + "," + a[0] + "," + a[2] + ")"
	case "BytesStartsWith":
		return "(" + a[0] + ".length<=" + a[1] + ".length&&(!" + a[0] + ".length||memcmp(" + a[0] + ".data," + a[1] + ".data," + a[0] + ".length)==0))"
	case "BytesFromList":
		return call("fg_bytes_from_list")
	case "BytesToList":
		return call("fg_bytes_to_list")
	case "BytesFromString":
		return "fg_bytes{" + a[0] + ".data," + a[0] + ".length}"
	case "BytesIsUtf8":
		return "fango_valid_utf8(fg_string{" + a[0] + ".data," + a[0] + ".length})"
	case "BytesUnvalidatedString":
		return "fg_string{" + a[0] + ".data," + a[0] + ".length}"
	case "BytesToStringLossy":
		return call("fg_string_lossy")
	case "BytesEq":
		return call("fg_eq")
	case "BytesShow":
		return call("fg_show")
	case "BytesLt", "BytesGt", "BytesLe", "BytesGe":
		op := map[string]string{"BytesLt": "<", "BytesGt": ">", "BytesLe": "<=", "BytesGe": ">="}[name]
		return "(fg_compare(fg_string{" + a[0] + ".data," + a[0] + ".length},fg_string{" + a[1] + ".data," + a[1] + ".length})" + op + "0)"
	case "ListEq":
		return call("fg_eq")
	case "ListShow":
		return call("fg_show")
	}
	panic("unsupported LLVM runtime primitive " + name)
}

// NativeHeaders generates declarations for the C sidecars in this graph. The
// compiler forces these headers into each translation unit, so definitions
// cannot silently disagree with a Fango annotation.
func NativeHeaderPath(module string) string { return "native/" + symbol(module) + "/fango_native.h" }

func NativeHeaders(p *core.Prog, modules map[string]bool) []File {
	byModule := map[string][]*types.NativeInfo{}
	for _, n := range p.Natives {
		if n.Template == nil && modules[n.Module] {
			byModule[n.Module] = append(byModule[n.Module], n)
		}
	}
	var result []File
	var includes strings.Builder
	var owners []string
	for owner := range byModule {
		owners = append(owners, owner)
	}
	sort.Strings(owners)
	for _, owner := range owners {
		ns := byModule[owner]
		sort.Slice(ns, func(i, j int) bool { return ns[i].Name < ns[j].Name })
		var out strings.Builder
		fmt.Fprintln(&out, "#include \"fango.h\"\n#ifdef __cplusplus\nextern \"C\" {\n#endif")
		fmt.Fprintf(&out, "#define FANGO_NATIVE(name) fg_native_m%s_##name\n", symbol(owner)[4:])
		for _, n := range ns {
			args, ret := core.PeelFun(n.Scheme.Body, n.Arity)
			var ps []string
			for i, t := range args {
				if n.Storage.Kind != "" && i == n.Storage.Payload {
					ps = append(ps, "fango_opaque")
					continue
				}
				if i < len(n.ParamWrappers) && n.ParamWrappers[i] != nil {
					t = n.ParamWrappers[i].Fields[0]
				}
				if nativeCType(t) == "void" {
					continue
				}
				ps = append(ps, nativeCType(t))
			}
			if n.Fallible != nil {
				ret = n.Fallible.Payload
			}
			if n.ResultWrapper != nil {
				ret = n.ResultWrapper.Fields[0]
			}
			resultType := nativeCType(ret)
			if n.Storage.Kind == "read" {
				resultType = "fango_opaque"
			}
			if n.Fallible != nil {
				if resultType != "void" {
					ps = append(ps, resultType+" *")
				}
				resultType = "fango_native_error"
			}
			if len(ps) == 0 {
				ps = []string{"void"}
			}
			fmt.Fprintf(&out, "%s %s(%s);\n", resultType, NativeSymbol(owner, types.SurfaceName(n.Name)), strings.Join(ps, ","))
		}
		fmt.Fprintln(&out, "#ifdef __cplusplus\n}\n#endif")
		path := NativeHeaderPath(owner)
		result = append(result, File{path, []byte(out.String())})
		fmt.Fprintf(&includes, "#include \"%s\"\n#undef FANGO_NATIVE\n", path)
	}
	result = append(result, File{"native.hpp", []byte(includes.String())})
	return result
}
func nativeCType(t types.Type) string {
	switch t := t.(type) {
	case *types.TVar:
		return "fango_opaque"
	case *types.TCon:
		switch t.Name {
		case "Int":
			return "int64_t"
		case "Float":
			return "double"
		case "Char":
			return "uint32_t"
		case "String":
			return "fango_string"
		case "Bool":
			return "bool"
		case "()":
			return "void"
		case "Bytes.Bytes":
			return "fango_bytes"
		case "Runtime.Native.Any":
			return "fango_opaque"
		}
	}
	panic("unsupported C native type " + types.Show(t))
}
