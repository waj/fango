// Package llvmgen lowers checked Core to a typed Clang input. Clang produces
// verified LLVM IR before optimization and native linking; this package has no
// dependency on the Go emitter or its runtime representations.
package llvmgen

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"maps"
	"strings"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

type File struct {
	Path string
	Data []byte
}
type generator struct {
	p                  *core.Prog
	b                  *types.Builtins
	adts               map[int]*types.ADTInfo
	defs               map[string]*core.Def
	needed             map[string]bool
	pointer            map[int]bool
	names              map[int]string
	locals             map[string]string
	localTypes         map[string]types.Type
	evidence           map[types.EffectKey]string
	rows               map[types.CaptureVar]string
	descriptors        map[int]string
	mode               types.Transport
	buf                bytes.Buffer
	serial             *int
	pending, exitLabel string
	state              string
	nativeUsed         map[string]bool
}

func symbol(name string) string { return "fg_s" + hex.EncodeToString([]byte(name)) }
func NativeSymbol(module, name string) string {
	return "fg_native_m" + hex.EncodeToString([]byte(module)) + "_" + strings.ToUpper(name[:1]) + name[1:]
}
func literal(s string) string {
	var out strings.Builder
	out.WriteByte('"')
	for _, c := range []byte(s) {
		fmt.Fprintf(&out, "\\%03o", c)
	}
	out.WriteByte('"')
	return out.String()
}
func stringValue(s string) string {
	return fmt.Sprintf("fg_string{reinterpret_cast<const unsigned char *>(%s),%d}", literal(s), len(s))
}

// Emit validates Core and emits only definitions reachable from the entry.
// Unused Async APIs in imported modules do not make synchronous code unsupported.
func Emit(p *core.Prog, b *types.Builtins, printMain bool) (source []byte, natives map[string]bool, err error) {
	defer func() {
		if r := recover(); r != nil {
			source = nil
			natives = nil
			err = fmt.Errorf("LLVM lowering: %v", r)
		}
	}()
	if errs := core.Lint(p, b); len(errs) > 0 {
		return nil, nil, fmt.Errorf("LLVM Core lint: %v", errs[0])
	}
	counter := 0
	g := &generator{p: p, b: b, adts: map[int]*types.ADTInfo{}, defs: map[string]*core.Def{}, needed: map[string]bool{}, pointer: map[int]bool{}, serial: &counter, nativeUsed: map[string]bool{}}
	for _, adt := range p.ADTs {
		g.adts[adt.Con.Unique] = adt
	}
	for i := range p.Defs {
		g.defs[p.Defs[i].Name] = &p.Defs[i]
	}
	entry := p.Entry
	if entry == "" {
		entry = "main"
	}
	var reach func(string)
	reach = func(name string) {
		if g.needed[name] {
			return
		}
		d := g.defs[name]
		if d == nil {
			return
		}
		g.needed[name] = true
		core.Inspect(d.Body, func(e core.Expr) {
			switch e := e.(type) {
			case *core.AsyncLaunch, *core.AsyncRebase, *core.AsyncSupervise, *core.ParallelMap:
				panic(fmt.Sprintf("UNSUPPORTED LLVM FEATURE: Async is reached from %s", name))
			case *core.VarRef:
				if !e.Local {
					reach(e.Name)
				}
			case *core.NativeCall:
				if e.Module == "Async" || strings.HasPrefix(e.Name, "Async.") {
					panic("UNSUPPORTED LLVM FEATURE: Async native " + e.Name)
				}
			case *core.Perform:
				if e.Op.Native != nil && e.Op.Native.Module == "Async" {
					panic("UNSUPPORTED LLVM FEATURE: Async native " + e.Op.Name)
				}
			}
		})
	}
	reach(entry)
	if g.defs[entry] == nil {
		return nil, nil, fmt.Errorf("LLVM: program has no main")
	}
	if printMain && p.EntryDisplay != nil {
		core.Inspect(p.EntryDisplay, func(e core.Expr) {
			if v, ok := e.(*core.VarRef); ok && !v.Local {
				reach(v.Name)
			}
		})
	}
	g.line("#include \"fango.hpp\"")
	g.line("#include \"native.hpp\"")
	g.representations()
	for _, d := range p.Defs {
		if g.needed[d.Name] {
			g.worker(&d, true)
		}
	}
	for _, d := range p.Defs {
		if g.needed[d.Name] {
			g.worker(&d, false)
		}
	}
	g.reset()
	g.line("int main(int argc,char **argv) {")
	g.line("fango_runtime_init(argc,argv);")
	g.line("fg_exit *fg_pending=nullptr;")
	g.line("{")
	d := g.defs[entry]
	if len(d.Params) > 0 {
		fn := symbol(entry)
		if d.Control.Transport == types.Exit {
			fn += "_x"
		}
		g.line("%s(fg_unit{});", fn)
	} else {
		g.line("auto fg_main=%s();", symbol(entry))
		_, function := d.Type.(*types.TFun)
		unit := false
		if con, ok := d.Type.(*types.TCon); ok {
			unit = con.Unique == b.Unit.Unique
		}
		if printMain && p.EntryDisplay != nil && !function && !unit {
			v := g.expr(p.EntryDisplay)
			g.line("fango_write(fg_append(%s,%s));", v, stringValue("\n"))
		}
	}
	g.line("return 0;\n}\nfg_exit_label: fango_panic(\"unhandled effect exit\");\n}")
	return g.buf.Bytes(), g.nativeUsed, nil
}

func (g *generator) reset() {
	g.names = map[int]string{}
	g.locals = map[string]string{}
	g.localTypes = map[string]types.Type{}
	g.evidence = map[types.EffectKey]string{}
	g.rows = map[types.CaptureVar]string{}
	g.descriptors = map[int]string{}
	g.mode = types.Direct
	g.pending = "fg_pending"
	g.exitLabel = "fg_exit_label"
	g.state = ""
}
func (g *generator) clone() *generator {
	n := *g
	n.buf = bytes.Buffer{}
	n.names = maps.Clone(g.names)
	n.locals = maps.Clone(g.locals)
	n.localTypes = maps.Clone(g.localTypes)
	n.evidence = maps.Clone(g.evidence)
	n.rows = maps.Clone(g.rows)
	n.descriptors = maps.Clone(g.descriptors)
	return &n
}
func (g *generator) line(f string, args ...any) {
	fmt.Fprintf(&g.buf, f, args...)
	g.buf.WriteByte('\n')
}
func (g *generator) fresh() string { *g.serial++; return fmt.Sprintf("fg_t%d", *g.serial) }
func (g *generator) temp(t types.Type, value string) string {
	name := g.fresh()
	g.line("%s %s=%s;", g.typ(t), name, value)
	return name
}
func (g *generator) out(t types.Type, call string) string {
	name := g.fresh()
	g.line("auto %s=%s;", name, call)
	g.line("if(%s.exit){%s=%s.exit;goto %s;}", name, g.pending, name, g.exitLabel)
	return g.temp(t, name+".value")
}
func (g *generator) bind(name string, t types.Type, value string) {
	g.locals[name] = value
	g.localTypes[name] = t
}
func (g *generator) params(vs []*types.TVar) (string, []string) {
	var decls, args []string
	for _, v := range vs {
		if v.Kind == types.RowVar {
			g.names[v.ID] = "fg_unit"
			continue
		}
		name := fmt.Sprintf("fg_T%d", v.ID)
		g.names[v.ID] = name
		decls = append(decls, "class "+name)
		args = append(args, name)
	}
	if len(decls) == 0 {
		return "", args
	}
	return "template<" + strings.Join(decls, ",") + ">", args
}
func typeApply(name string, args []string) string {
	if len(args) == 0 {
		return name
	}
	return name + "<" + strings.Join(args, ",") + ">"
}
func (g *generator) typeArgs(ts []types.Type) []string {
	var out []string
	for _, t := range ts {
		out = append(out, g.typ(t))
	}
	return out
}
func (g *generator) adtArgs(a *types.ADTInfo, ts []types.Type) []string {
	var out []string
	for i, p := range a.Params {
		if p.Kind != types.RowVar {
			out = append(out, g.typ(ts[i]))
		}
	}
	return out
}
func (g *generator) typ(t types.Type) string {
	switch t := t.(type) {
	case *types.TVar:
		if s := g.names[t.ID]; s != "" {
			return s
		}
		panic(fmt.Sprintf("unbound type %d", t.ID))
	case types.Row:
		return "fg_unit"
	case *types.TFun:
		return "fg_fn<" + g.signature(t) + ">"
	case *types.TCon:
		switch t.Unique {
		case g.b.Int.Unique:
			return "int64_t"
		case g.b.Float.Unique:
			return "double"
		case g.b.String.Unique:
			return "fg_string"
		case g.b.Char.Unique:
			return "uint32_t"
		case g.b.Bool.Unique:
			return "bool"
		case g.b.Unit.Unique:
			return "fg_unit"
		}
		if t.Name == types.FailureTypeName {
			return "fg_exit *"
		}
		a := g.adts[t.Unique]
		if a == nil {
			panic("unknown nominal type " + t.Name)
		}
		switch a.Repr {
		case types.ReprList:
			return typeApply("fg_list", g.adtArgs(a, t.Args))
		case types.ReprBytes:
			return "fg_bytes"
		case types.ReprNativeAny:
			return "void *"
		}
		return typeApply(symbol(t.Name)+"_t", g.adtArgs(a, t.Args))
	}
	panic(fmt.Sprintf("unknown type %T", t))
}
func (g *generator) signature(t *types.TFun) string {
	var args []string
	for _, label := range types.SortedRow(t.Eff).Labels {
		if types.RuntimeEvidenceEffect(label) {
			args = append(args, g.evType(core.EffectInstance{Unique: label.Unique, Name: label.Name, Args: label.Args})+" *")
		}
	}
	if types.FunctionOpenRow(t) {
		args = append(args, "fg_row")
	}
	args = append(args, g.typ(t.Arg))
	return g.typ(t.Ret) + "(" + strings.Join(args, ",") + ")"
}
func (g *generator) evType(ev core.EffectInstance) string {
	return typeApply(symbol(ev.Name)+"_ev", g.typeArgs(ev.Args))
}
func (g *generator) descriptor(t types.Type) string {
	if v, ok := t.(*types.TVar); ok && g.descriptors[v.ID] != "" {
		return g.descriptors[v.ID]
	}
	if con, ok := t.(*types.TCon); ok && len(con.Args) > 0 {
		var dynamic func(types.Type) bool
		dynamic = func(t types.Type) bool {
			switch t := t.(type) {
			case *types.TVar:
				return g.descriptors[t.ID] != ""
			case *types.TCon:
				for _, arg := range t.Args {
					if dynamic(arg) {
						return true
					}
				}
			}
			return false
		}
		if dynamic(t) {
			var args []types.Type
			a := g.adts[con.Unique]
			for i, arg := range con.Args {
				if a == nil || i >= len(a.Params) || a.Params[i].Kind != types.RowVar {
					args = append(args, arg)
				}
			}
			return fmt.Sprintf("fg_applied(%s,%d,%s,%t)", stringValue(con.Name), len(args), g.descriptorsArray(args), types.InspectionShapeSafe(t, g.adts))
		}
	}
	return "fg_type<" + g.typ(t) + ">()"
}
func (g *generator) descriptorsArray(ts []types.Type) string {
	if len(ts) == 0 {
		return "nullptr"
	}
	name := g.fresh()
	var values []string
	for _, t := range ts {
		values = append(values, g.descriptor(t))
	}
	g.line("auto *%s=static_cast<const fg_descriptor **>(fango_alloc(sizeof(fg_descriptor *)*%d));", name, len(values))
	for i, value := range values {
		g.line("%s[%d]=%s;", name, i, value)
	}
	return name
}
func (g *generator) ev(ev core.EffectInstance) string {
	if types.SurfaceName(ev.Name) == "IO" {
		return "nullptr"
	}
	if value := g.evidence[ev.Key()]; value != "" {
		return value
	}
	panic("missing evidence " + ev.Name + " " + string(ev.Key()))
}
func (g *generator) invocation(explicit, deferred []core.EffectInstance, row types.CaptureVar) {
	names := make([]string, len(explicit))
	for i := range names {
		names[i] = fmt.Sprintf("fg_e%d", i)
	}
	g.invocationNamed(explicit, deferred, row, names, "fg_row_arg")
}
func (g *generator) invocationNamed(explicit, deferred []core.EffectInstance, row types.CaptureVar, names []string, rowName string) {
	for i, ev := range explicit {
		g.evidence[ev.Key()] = names[i]
	}
	if row != 0 {
		g.rows[row] = rowName
		for _, ev := range deferred {
			a := g.descriptorsArray(ev.Args)
			g.evidence[ev.Key()] = fmt.Sprintf("static_cast<%s *>(fg_lookup(%s,%s,%d,%s))", g.evType(ev), rowName, stringValue(ev.Name), len(ev.Args), a)
		}
	}
}
func (g *generator) row(arg *core.RowArgument) string {
	value := "fg_row{}"
	if arg == nil {
		return value
	}
	if arg.From != 0 {
		value = g.rows[arg.From]
		if value == "" {
			panic("unbound residual row")
		}
	}
	for _, ev := range arg.Effects {
		value = "fg_extend(" + value + "," + g.ev(ev) + ")"
	}
	return value
}
func (g *generator) worker(d *core.Def, prototype bool) {
	g.reset()
	templ, _ := g.params(d.TyParams)
	args, result := core.PeelFun(d.Type, len(d.Params))
	var params []string
	for i, e := range d.EffectParams {
		params = append(params, fmt.Sprintf("%s *fg_e%d", g.evType(e), i))
	}
	if d.RowParam != 0 {
		params = append(params, "fg_row fg_row_arg")
	}
	for i, t := range args {
		name := fmt.Sprintf("fg_a%d", i)
		params = append(params, g.typ(t)+" "+name)
		g.bind(d.Params[i], t, name)
	}
	modes := []types.Transport{types.Direct, types.Exit}
	if d.Control.Transport == types.Exit {
		modes = []types.Transport{types.Exit}
	}
	if !d.IsWorker() {
		modes = []types.Transport{types.Direct}
	}
	for _, mode := range modes {
		g.mode = mode
		name := symbol(d.Name)
		ret := g.typ(result)
		if mode == types.Exit {
			name += "_x"
			ret = "fg_out<" + ret + ">"
		}
		g.line("%s", templ)
		if prototype {
			g.line("%s %s(%s);", ret, name, strings.Join(params, ","))
			continue
		}
		g.line("%s %s(%s){", ret, name, strings.Join(params, ","))
		g.line("fg_exit *fg_pending=nullptr;\n{")
		g.invocation(d.EffectParams, d.RowEffects, d.RowParam)
		if !d.IsWorker() {
			g.line("static bool fg_ready=false;static %s fg_value{};if(fg_ready)return fg_value;", ret)
		}
		_, loop := core.DetectTailLoop(d)
		if loop {
			g.line("for(;;){")
			g.tail(d, d.Body)
			g.line("}")
		} else {
			v := g.expr(d.Body)
			if !d.IsWorker() {
				g.line("fg_value=%s;fg_ready=true;return fg_value;", v)
			} else {
				g.returnValue(v)
			}
		}
		g.line("}\nfg_exit_label:")
		if mode == types.Exit {
			g.line("return {{},fg_pending};")
		} else {
			g.line("fango_panic(\"exit from Direct worker\");return {}; ")
		}
		g.line("}")
	}
}
func (g *generator) returnValue(v string) {
	if g.mode == types.Exit {
		g.line("return {%s,nullptr};", v)
	} else {
		g.line("return %s;", v)
	}
}
func (g *generator) tail(d *core.Def, e core.Expr) {
	switch e := e.(type) {
	case *core.Let:
		v := g.expr(e.Rhs)
		g.bind(e.Name, e.Rhs.Type(), v)
		g.tail(d, e.Body)
	case *core.Seq:
		g.expr(e.First)
		g.tail(d, e.Then)
	case *core.If:
		c := g.expr(e.Cond)
		g.line("if(%s){", c)
		g.tail(d, e.Then)
		g.line("}else{")
		g.tail(d, e.Else)
		g.line("}")
	case *core.Case:
		v := g.expr(e.Scrut)
		g.bind(e.Bind, e.Scrut.Type(), v)
		g.tree(e.Tree, func(e core.Expr) { g.tail(d, e) })
	case *core.App:
		if core.IsTailLoopCall(d, e) {
			var vs []string
			for _, a := range e.Args {
				vs = append(vs, g.temp(a.Type(), g.expr(a)))
			}
			for i, v := range vs {
				g.line("fg_a%d=%s;", i, v)
			}
			g.line("continue;")
		} else {
			g.returnValue(g.expr(e))
		}
	default:
		g.returnValue(g.expr(e))
	}
}
