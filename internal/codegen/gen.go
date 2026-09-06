package codegen

import (
	"bytes"
	"fmt"
	goast "go/ast"
	"go/format"
	goparser "go/parser"
	gotoken "go/token"
	"math"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

// Unit describes one Fango source module for package emission. Units must be
// dependency-first and contain exactly one entry.
type Unit struct {
	Name    string
	Imports []string
	Entry   bool
}

// File is one path relative to the generated Go module root.
type File struct {
	Path string
	Data []byte
}

// EmitProject lowers a whole Core program into one Go package per Fango
// module. The entry module is package main at the project root; dependencies
// live below modules/ in their logical source layout.
func EmitProject(p *core.Prog, b *types.Builtins, units []Unit, printMain bool) ([]File, error) {
	var files []File
	entryCount := 0
	owners := make(map[string]bool, len(units))
	for _, unit := range units {
		if owners[unit.Name] {
			return nil, fmt.Errorf("codegen: duplicate module unit %q", unit.Name)
		}
		owners[unit.Name] = true
		if unit.Entry {
			entryCount++
		}
		data, err := emitUnit(p, b, unit, printMain)
		if err != nil {
			return nil, err
		}
		path := "main.go"
		if !unit.Entry {
			path = filepath.ToSlash(filepath.Join("modules", strings.ReplaceAll(unit.Name, ".", "/"), "module.go"))
		}
		files = append(files, File{Path: path, Data: data})
	}
	if entryCount != 1 {
		return nil, fmt.Errorf("codegen: module graph has %d entry units, want 1", entryCount)
	}
	for _, def := range p.Defs {
		if !owners[def.Owner] {
			return nil, fmt.Errorf("codegen: definition %q has no module unit for owner %q", def.Name, def.Owner)
		}
	}
	for _, adt := range p.ADTs {
		owner := symbolOwner(adt.Con.Name)
		if !owners[owner] {
			return nil, fmt.Errorf("codegen: type %q has no module unit for owner %q", adt.Con.Name, owner)
		}
	}
	for _, eff := range p.Effects {
		owner := symbolOwner(eff.Name)
		if types.SurfaceName(eff.Name) != "IO" && !owners[owner] {
			return nil, fmt.Errorf("codegen: effect %q has no module unit for owner %q", eff.Name, owner)
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

func emitUnit(p *core.Prog, b *types.Builtins, unit Unit, printMain bool) ([]byte, error) {
	g := &gen{
		b:             b,
		adts:          map[int]*types.ADTInfo{},
		neededEq:      map[int]bool{},
		neededShow:    map[int]bool{},
		scalarEq:      map[int]bool{},
		scalarShow:    map[int]bool{},
		caseVarTys:    map[string]types.Type{},
		evidence:      map[int][]goast.Expr{},
		defs:          map[string]*core.Def{},
		unit:          unit.Name,
		imports:       map[string]bool{},
		nativeImports: map[string]bool{},
		direct:        map[string]bool{},
		natives:       p.Natives,
	}
	for _, name := range unit.Imports {
		g.direct[name] = true
	}
	for i := range p.Defs {
		g.defs[p.Defs[i].Name] = &p.Defs[i]
	}
	for _, adt := range p.ADTs {
		g.adts[adt.Con.Unique] = adt
	}
	if unit.Entry {
		for _, native := range p.Natives {
			if native.Template == nil {
				g.nativeImports[native.Module] = false // force sidecar compilation even when unused
			}
		}
	}

	var mainDef *core.Def
	entry := p.Entry
	if entry == "" {
		entry = "main"
	}
	for i := range p.Defs {
		if p.Defs[i].Name == entry && p.Defs[i].Owner == unit.Name {
			mainDef = &p.Defs[i]
		}
	}
	if !unit.Entry {
		mainDef = nil
	}
	if unit.Entry && mainDef == nil {
		return nil, fmt.Errorf("codegen: entry definition %q is not owned by entry module %q", entry, unit.Name)
	}
	mainIsUnit := mainDef != nil && g.unique(mainDef.Type) == b.Unit.Unique
	mainIsFn := mainDef != nil && len(mainDef.Params) == 1

	adts, effects := g.ownedADTs(p.ADTs), g.ownedEffects(p.Effects)
	decls := append(g.effectDecls(effects), g.adtDecls(adts)...)
	for i := range p.Defs {
		d := &p.Defs[i]
		if d.Owner != unit.Name {
			continue
		}
		if d == mainDef && mainIsUnit {
			continue // no package var: the effect runs inside func main()
		}
		if d.IsWorker() {
			// Includes nullary generic workers — polymorphic values emit as
			// zero-parameter generic functions (doc/design.md, "Go backend and runtime").
			decls = append(decls, g.workerDef(d))
			continue
		}
		g.tyParamNames = nil
		decls = append(decls, varDecl(g.topValueName(d.Name), g.goType(d.Type), g.expr(d.Body, 0)))
	}

	switch {
	case mainIsUnit:
		decls = append(decls, funcDecl("main", g.stmts(mainDef.Body)...))
	case mainIsFn:
		decls = append(decls, funcDecl("main", g.workerCallStmts(entry, nil)...))
	case printMain && unit.Entry:
		g.usesFangort = true
		decls = append(decls, funcDecl("main",
			exprStmt(callExpr(selector("fangort", "PrintString"), g.expr(p.EntryDisplay, 0)))))
	default:
		if mainDef != nil {
			decls = append(decls, funcDecl("main", assignBlank(g.topValueRef(entry))))
		}
	}

	// Derived eq/show, discovered during emission (on demand, doc/design.md, "Go backend and runtime"), plus
	// the scalar element-op helpers their synthesis demanded.
	decls = append(decls, g.derivedDecls(adts)...)
	decls = append(decls, g.scalarHelperDecls()...)
	// Imports come from emission (fangort for prints, math for float
	// specials), so they are prepended last — in a fixed order, for
	// deterministic output.
	if imports := g.importsDecl(); imports != nil {
		decls = append([]goast.Decl{imports}, decls...)
	}

	packageName := "main"
	if !unit.Entry {
		packageName = "fangomod"
	}
	file := &goast.File{Name: ident(packageName), Decls: decls}
	var buf bytes.Buffer
	if err := format.Node(&buf, gotoken.NewFileSet(), file); err != nil {
		return nil, fmt.Errorf("codegen: printing generated Go: %w", err)
	}
	return buf.Bytes(), nil
}

type gen struct {
	b           *types.Builtins
	adts        map[int]*types.ADTInfo
	neededEq    map[int]bool
	neededShow  map[int]bool
	tmp         int // type-switch binding counter (ts0, ts1, …)
	usesFangort bool
	usesMath    bool

	// tyParamNames maps the rigid vars of the definition (or derived
	// function) currently being emitted to their Go type-parameter names
	// (positional: A0, A1, …). Reset per definition.
	tyParamNames map[int]string

	// eqParamNames/showParamNames map an ADT's rigid params to the element-
	// operation parameters of the derived eq/show being emitted (doc/design.md, "Go backend and runtime").
	eqParamNames   map[int]string
	showParamNames map[int]string

	// scalarEq/scalarShow track which scalar element-op helpers (eqInt,
	// showInt, …) call-site synthesis demanded.
	scalarEq   map[int]bool
	scalarShow map[int]bool

	// caseVarTys records the (instantiated) types of case scrutinee binders
	// and field temporaries, so nested constructor switches know their
	// column's type arguments.
	caseVarTys    map[string]types.Type
	evidence      map[int][]goast.Expr
	defs          map[string]*core.Def
	unit          string
	imports       map[string]bool
	nativeImports map[string]bool
	direct        map[string]bool
	natives       map[string]*types.NativeInfo
}

func symbolOwner(name string) string {
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		return name[:i]
	}
	return ""
}

func moduleAlias(name string) string {
	var b strings.Builder
	b.WriteString("m_")
	for i := 0; i < len(name); i++ {
		switch name[i] {
		case '.':
			b.WriteString("_d")
		case '_':
			b.WriteString("_u")
		default:
			b.WriteByte(name[i])
		}
	}
	return b.String()
}

func nativeAlias(name string) string { return "n_" + NativeLinkName(name) }

func nativeImportPath(name string) string { return "fangobuild/native/" + NativeLinkName(name) }

// NativeLinkName is a reversible-enough filesystem/package component for a
// logical module name: underscores are escaped before dots, avoiding the
// A.B/A_dB collision a plain replacement would create.
func NativeLinkName(name string) string {
	return strings.ReplaceAll(strings.ReplaceAll(name, "_", "_u"), ".", "_d")
}

func moduleImportPath(name string) string {
	return "fangobuild/modules/" + strings.ReplaceAll(name, ".", "/")
}

func (g *gen) qualified(owner, name string) goast.Expr {
	if owner == "" || owner == g.unit {
		return ident(name)
	}
	g.imports[owner] = true
	return selector(moduleAlias(owner), name)
}

func (g *gen) topValueName(name string) string {
	return "V_" + linkName(name)
}

func (g *gen) topValueRef(name string) goast.Expr {
	owner := symbolOwner(name)
	if d := g.defs[name]; d != nil {
		owner = d.Owner
	}
	return g.qualified(owner, g.topValueName(name))
}

func (g *gen) typeRef(adt *types.ADTInfo) goast.Expr {
	return g.qualified(symbolOwner(adt.Con.Name), mangleType(adt.Con.Name))
}

func (g *gen) ctorRef(ctor *types.CtorInfo) goast.Expr {
	return g.qualified(symbolOwner(ctor.Result.Name), mangleCtor(ctor.Name))
}

func (g *gen) eqName(adt *types.ADTInfo) string {
	return "EqT_" + linkName(adt.Con.Name)
}

func (g *gen) showName(adt *types.ADTInfo) string {
	return "ShowT_" + linkName(adt.Con.Name)
}

func (g *gen) eqRef(adt *types.ADTInfo) goast.Expr {
	return g.qualified(symbolOwner(adt.Con.Name), g.eqName(adt))
}

func (g *gen) showRef(adt *types.ADTInfo) goast.Expr {
	return g.qualified(symbolOwner(adt.Con.Name), g.showName(adt))
}

func (g *gen) evidenceName(name string) string {
	return "ev_" + linkName(name)
}

func (g *gen) ownedADTs(in []*types.ADTInfo) []*types.ADTInfo {
	var out []*types.ADTInfo
	for _, adt := range in {
		if symbolOwner(adt.Con.Name) == g.unit {
			out = append(out, adt)
		}
	}
	return out
}

func (g *gen) ownedEffects(in []*types.EffectInfo) []*types.EffectInfo {
	var out []*types.EffectInfo
	for _, eff := range in {
		if types.SurfaceName(eff.Name) != "IO" && symbolOwner(eff.Name) == g.unit {
			out = append(out, eff)
		}
	}
	return out
}

func (g *gen) derivable(t types.Type, visiting map[int]bool) bool {
	switch t := t.(type) {
	case *types.TVar:
		return true
	case *types.TFun:
		return false
	case *types.TCon:
		for _, arg := range t.Args {
			if !g.derivable(arg, visiting) {
				return false
			}
		}
		adt := g.adts[t.Unique]
		if adt == nil || visiting[t.Unique] {
			return true
		}
		visiting[t.Unique] = true
		defer delete(visiting, t.Unique)
		for _, ctor := range adt.Ctors {
			for _, field := range ctor.Fields {
				if !g.derivable(field, visiting) {
					return false
				}
			}
		}
		return true
	default:
		return false
	}
}

func (g *gen) importsDecl() goast.Decl {
	type spec struct{ alias, path string }
	var specs []spec
	if g.usesFangort {
		specs = append(specs, spec{path: "fangobuild/fangort"})
	}
	if g.usesMath {
		specs = append(specs, spec{path: "math"})
	}
	nativeNames := make([]string, 0, len(g.nativeImports))
	for name := range g.nativeImports {
		nativeNames = append(nativeNames, name)
	}
	sort.Strings(nativeNames)
	for _, name := range nativeNames {
		alias := "_"
		if g.nativeImports[name] {
			alias = nativeAlias(name)
		}
		specs = append(specs, spec{alias: alias, path: nativeImportPath(name)})
	}
	owners := make(map[string]bool, len(g.direct)+len(g.imports))
	for name := range g.direct {
		owners[name] = true
	}
	for name := range g.imports {
		owners[name] = true
	}
	names := make([]string, 0, len(owners))
	for name := range owners {
		if name != "" && name != g.unit {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		alias := "_"
		if g.imports[name] {
			alias = moduleAlias(name)
		}
		specs = append(specs, spec{alias: alias, path: moduleImportPath(name)})
	}
	if len(specs) == 0 {
		return nil
	}
	goSpecs := make([]goast.Spec, len(specs))
	for i, s := range specs {
		is := &goast.ImportSpec{Path: &goast.BasicLit{Kind: gotoken.STRING, Value: strconv.Quote(s.path)}}
		if s.alias != "" {
			is.Name = ident(s.alias)
		}
		goSpecs[i] = is
	}
	return &goast.GenDecl{Tok: gotoken.IMPORT, Specs: goSpecs}
}

func (g *gen) unique(t types.Type) int {
	if con, ok := t.(*types.TCon); ok && len(con.Args) == 0 {
		return con.Unique
	}
	return -1
}

func (g *gen) isUnit(t types.Type) bool { return g.unique(t) == g.b.Unit.Unique }

func (g *gen) unitType() goast.Expr {
	g.usesFangort = true
	return selector("fangort", "Unit")
}

func (g *gen) unitValue() goast.Expr {
	g.usesFangort = true
	return selector("fangort", "UnitValue")
}

// printCall builds the print of a value: fangort.PrintX for scalars, the
// derived show piped through fangort.PrintString for ADTs.
func (g *gen) printCall(arg goast.Expr, t types.Type) goast.Expr {
	g.usesFangort = true
	if g.adtOf(t) != nil {
		return callExpr(selector("fangort", "PrintString"),
			g.showCall(t, arg, ident("false")))
	}
	return callExpr(selector("fangort", g.printFn(t)), arg)
}

// printFn picks the fangort printer for a ground scalar type.
func (g *gen) printFn(t types.Type) string {
	switch g.unique(t) {
	case g.b.Int.Unique:
		return "PrintInt"
	case g.b.Float.Unique:
		return "PrintFloat"
	case g.b.String.Unique:
		return "PrintString"
	case g.b.Bool.Unique:
		return "PrintBool"
	default:
		panic("codegen: no printer for type " + types.Show(t))
	}
}

// workerDef emits a top-level function definition as an uncurried Go func
// (doc/design.md, "Go backend and runtime" item 1): the parameter types peel off the curried fango type, the
// body emits in return-position statement context. A generic definition's
// TyParams become Go type parameters — `any` for General vars,
// fangort.Number for Number-kinded ones (doc/design.md, "Type inference", doc/design.md, "Go backend and runtime").
func (g *gen) workerDef(d *core.Def) goast.Decl {
	g.tyParamNames = tyParamNames(d.TyParams)
	argTys, ret := core.PeelFun(d.Type, len(d.Params))
	params := make([]paramSpec, 0, len(d.EffectParams)+len(d.Params))
	for _, ev := range d.EffectParams {
		name := g.evidenceName(ev.Name)
		params = append(params, paramSpec{name: name, typ: g.effectType(ev)})
		g.evidence[ev.Unique] = append(g.evidence[ev.Unique], ident(name))
	}
	for i, name := range d.Params {
		if g.isUnit(argTys[i]) {
			continue
		}
		if name != "_" {
			name = mangleValue(name)
		}
		params = append(params, paramSpec{name: name, typ: g.goType(argTys[i])})
	}
	var result goast.Expr
	if !g.isUnit(ret) {
		result = g.goType(ret)
	}
	// Self tail calls compile to loops (doc/design.md, "Go backend and
	// runtime"): an eligible body emits as one `for` statement whose leaves
	// either return or jump. A `for` with no break is a terminating
	// statement in Go, so no trailing return is needed in either result
	// shape.
	var body []goast.Stmt
	if _, ok := core.DetectTailLoop(d); ok {
		body = []goast.Stmt{&goast.ForStmt{Body: &goast.BlockStmt{List: g.loopStmts(d, d.Body, g.isUnit(ret))}}}
	} else {
		body = g.retStmtsFor(d.Body, g.isUnit(ret))
	}
	decl := workerDecl(g.topValueName(d.Name), params, result, body).(*goast.FuncDecl)
	for _, ev := range d.EffectParams {
		g.evidence[ev.Unique] = g.evidence[ev.Unique][:len(g.evidence[ev.Unique])-1]
	}
	decl.Type.TypeParams = g.typeParamFields(d.TyParams)
	return decl
}

func (g *gen) workerABI(name string) ([]types.Type, bool) {
	d := g.defs[name]
	if d == nil {
		return nil, false
	}
	args, ret := core.PeelFun(d.Type, len(d.Params))
	return args, g.isUnit(ret)
}

func (g *gen) workerCallStmts(name string, args []goast.Expr) []goast.Stmt {
	call := callExpr(g.topValueRef(name), args...)
	if args == nil {
		call = callExpr(g.topValueRef(name))
	}
	return []goast.Stmt{exprStmt(call)}
}

func (g *gen) workerCallStmt(e *core.App) goast.Stmt {
	ref := e.Callee.(*core.VarRef)
	formal, voidResult := g.workerABI(ref.Name)
	if !voidResult {
		return assignBlank(g.workerCallExpr(e))
	}
	for i, a := range e.Args {
		if i < len(formal) && g.isUnit(formal[i]) && !unitAtom(a) {
			return exprStmt(g.workerCallExpr(e))
		}
	}
	args := make([]goast.Expr, 0, len(e.EvidenceArgs)+len(e.Args))
	for _, ev := range e.EvidenceArgs {
		stack := g.evidence[ev.Unique]
		if len(stack) == 0 {
			panic("codegen: missing lexical evidence")
		}
		args = append(args, stack[len(stack)-1])
	}
	for i, a := range e.Args {
		if i < len(formal) && g.isUnit(formal[i]) {
			continue
		}
		args = append(args, g.expr(a, 0))
	}
	return exprStmt(callExpr(indexExpr(g.topValueRef(ref.Name), g.goTypes(e.TyArgs)), args...))
}

// tyParamNames assigns positional Go names (A0, A1, …) to a definition's
// rigid type variables.
func tyParamNames(vars []*types.TVar) map[int]string {
	if len(vars) == 0 {
		return nil
	}
	m := make(map[int]string, len(vars))
	for i, v := range vars {
		m[v.ID] = fmt.Sprintf("A%d", i)
	}
	return m
}

// typeParamFields builds the [A0 any, A1 fangort.Number] type-parameter list.
func (g *gen) typeParamFields(vars []*types.TVar) *goast.FieldList {
	if len(vars) == 0 {
		return nil
	}
	fields := make([]*goast.Field, len(vars))
	for i, v := range vars {
		var constraint goast.Expr = ident("any")
		fields[i] = &goast.Field{
			Names: []*goast.Ident{ident(g.tyParamNames[v.ID])},
			Type:  constraint,
		}
	}
	return &goast.FieldList{List: fields}
}

// retStmts emits an expression in return-position statement context —
// worker and lambda bodies. Lets become locals, ifs become real Go
// if/return (fib's hot path must not pay an IIFE closure), everything else
// returns directly.
func (g *gen) retStmts(e core.Expr) []goast.Stmt {
	return g.retStmtsFor(e, false)
}

func (g *gen) retStmtsFor(e core.Expr, unitResult bool) []goast.Stmt {
	switch e := e.(type) {
	case *core.Let:
		return append(g.letBindingStmts(e), g.retStmtsFor(e.Body, unitResult)...)
	case *core.If:
		stmts := []goast.Stmt{&goast.IfStmt{
			Cond: g.expr(e.Cond, 0),
			Body: &goast.BlockStmt{List: g.retStmtsFor(e.Then, unitResult)},
		}}
		return append(stmts, g.retStmtsFor(e.Else, unitResult)...)
	case *core.Case:
		return g.caseStmts(e, func(x core.Expr) []goast.Stmt { return g.retStmtsFor(x, unitResult) })
	default:
		if unitResult {
			return append(g.stmts(e), bareReturnStmt())
		}
		return []goast.Stmt{returnStmt(g.expr(e, 0))}
	}
}

// goType maps a fango type to its unboxed Go representation (see
// doc/design.md, "Go backend and runtime"). Int is int64, not int: identical
// overflow behavior on every GOARCH.
// Rigid type variables map to the enclosing definition's Go type parameters;
// parameterized ADTs to instantiated generic types (doc/design.md, "Go backend and runtime").
func (g *gen) goType(t types.Type) goast.Expr {
	switch t := t.(type) {
	case *types.TVar:
		if t.Rigid {
			if name, ok := g.tyParamNames[t.ID]; ok {
				return ident(name)
			}
		}
		panic("codegen: type variable outside its definition's type parameters")
	case *types.TFun:
		params := make([]paramSpec, 0, len(t.Eff.Labels)+1)
		for _, l := range types.SortedRow(t.Eff).Labels {
			if types.SurfaceName(l.Name) == "IO" {
				continue
			}
			params = append(params, paramSpec{typ: g.effectType(core.EffectInstance{Unique: l.Unique, Name: l.Name, Args: l.Args})})
		}
		params = append(params, paramSpec{typ: g.goType(t.Arg)})
		return &goast.FuncType{Params: paramFields(params), Results: &goast.FieldList{List: []*goast.Field{{Type: g.goType(t.Ret)}}}}
	case *types.TCon:
		switch t.Unique {
		case g.b.Int.Unique:
			return ident("int64")
		case g.b.Float.Unique:
			return ident("float64")
		case g.b.String.Unique:
			return ident("string")
		case g.b.Bool.Unique:
			return ident("bool")
		case g.b.Unit.Unique:
			return g.unitType()
		default:
			if adt, ok := g.adts[t.Unique]; ok {
				return indexExpr(g.typeRef(adt), g.goTypes(t.Args))
			}
			panic(fmt.Sprintf("codegen: unknown type constructor %s", t.Name))
		}
	default:
		panic(fmt.Sprintf("codegen: unhandled type %s", types.Show(t)))
	}
}

func (g *gen) goTypes(ts []types.Type) []goast.Expr {
	if len(ts) == 0 {
		return nil
	}
	out := make([]goast.Expr, len(ts))
	for i, t := range ts {
		out[i] = g.goType(t)
	}
	return out
}

var goOps = map[string]gotoken.Token{
	"+": gotoken.ADD, "-": gotoken.SUB, "*": gotoken.MUL, "/": gotoken.QUO,
	"++": gotoken.ADD, // String concat is Go's + on strings (doc/design.md, "Go backend and runtime")
	"==": gotoken.EQL, "/=": gotoken.NEQ,
	"<": gotoken.LSS, ">": gotoken.GTR, "<=": gotoken.LEQ, ">=": gotoken.GEQ,
}

// goPrec mirrors Go's binary precedence for the operators fango emits
// (comparisons 3, additive 4, multiplicative 5), so we parenthesize only
// where Go's grammar needs it. Unary minus uses 6: above every binary op.
func goPrec(op string) int {
	switch op {
	case "*", "/":
		return 5
	case "+", "-", "++":
		return 4
	case "==", "/=", "<", ">", "<=", ">=":
		return 3
	default:
		return 0
	}
}

// nativeSidecarCall lowers a sidecar invocation without deciding whether its
// result is needed as a value. Unit arguments are erased from the Go ABI, but
// a non-atomic Unit argument must still run in source order; prelude contains
// the statements (and any temporaries) needed to preserve that order.
func (g *gen) nativeSidecarCall(call *core.NativeCall, n *types.NativeInfo) ([]goast.Stmt, goast.Expr) {
	g.nativeImports[n.Module] = true
	args := make([]goast.Expr, 0, len(call.Args))
	needsSequence := false
	for _, arg := range call.Args {
		needsSequence = needsSequence || g.isUnit(arg.Type()) && !unitAtom(arg)
	}
	var prelude []goast.Stmt
	for _, arg := range call.Args {
		if g.isUnit(arg.Type()) {
			if needsSequence {
				prelude = append(prelude, g.stmts(arg)...)
			}
			continue
		}
		if needsSequence {
			name := fmt.Sprintf("t_native%d", g.tmp)
			g.tmp++
			prelude = append(prelude, varDeclStmt(name, g.goType(arg.Type()), g.expr(arg, 0)))
			args = append(args, ident(name))
		} else {
			args = append(args, g.expr(arg, 0))
		}
	}
	fn := selector(nativeAlias(n.Module), exportNativeName(types.SurfaceName(n.Name)))
	return prelude, callExpr(fn, args...)
}

func (g *gen) nativeTemplateExpr(call *core.NativeCall, template string, parentPrec int) goast.Expr {
	s := strings.ReplaceAll(strings.ReplaceAll(template, "$eq", "__fango_eq"), "$show", "__fango_show")
	for i := len(call.Args); i >= 1; i-- {
		s = strings.ReplaceAll(s, fmt.Sprintf("$%d", i), fmt.Sprintf("__fango_p%d", i))
	}
	x, err := goparser.ParseExpr(s)
	if err != nil {
		panic("codegen: invalid validated native template: " + err.Error())
	}
	var splice func(goast.Expr, int) goast.Expr
	splice = func(x goast.Expr, ctx int) goast.Expr {
		switch x := x.(type) {
		case *goast.Ident:
			if strings.HasPrefix(x.Name, "__fango_p") {
				i, _ := strconv.Atoi(strings.TrimPrefix(x.Name, "__fango_p"))
				return g.expr(call.Args[i-1], ctx)
			}
			return ident(x.Name)
		case *goast.BinaryExpr:
			prec := x.Op.Precedence()
			return parenIf(prec < ctx, &goast.BinaryExpr{X: splice(x.X, prec), Op: x.Op, Y: splice(x.Y, prec+1)})
		case *goast.UnaryExpr:
			return parenIf(6 < ctx, &goast.UnaryExpr{Op: x.Op, X: splice(x.X, 6)})
		case *goast.ParenExpr:
			return &goast.ParenExpr{X: splice(x.X, 0)}
		case *goast.SelectorExpr:
			if id, ok := x.X.(*goast.Ident); ok && id.Name == "fangort" {
				g.usesFangort = true
			}
			return &goast.SelectorExpr{X: splice(x.X, 0), Sel: ident(x.Sel.Name)}
		case *goast.CallExpr:
			if id, ok := x.Fun.(*goast.Ident); ok && id.Name == "__fango_eq" {
				a, b := splice(x.Args[0], 0), splice(x.Args[1], 0)
				i := nativePlaceholderIndex(x.Args[0])
				if g.adtOf(call.Args[i].Type()) != nil {
					return g.eqCall(call.Args[i].Type(), a, b)
				}
				return binExpr(gotoken.EQL, a, b)
			}
			if id, ok := x.Fun.(*goast.Ident); ok && id.Name == "__fango_show" {
				i := nativePlaceholderIndex(x.Args[0])
				return g.nativeShow(call.Args[i].Type(), splice(x.Args[0], 0))
			}
			args := make([]goast.Expr, len(x.Args))
			for i, a := range x.Args {
				args[i] = splice(a, 0)
			}
			return &goast.CallExpr{Fun: splice(x.Fun, 0), Args: args}
		case *goast.BasicLit:
			return &goast.BasicLit{Kind: x.Kind, Value: x.Value}
		default:
			panic(fmt.Sprintf("codegen: unsupported native template node %T", x))
		}
	}
	return splice(x, parentPrec)
}

func (g *gen) nativeExpr(call *core.NativeCall, parentPrec int) goast.Expr {
	n := g.natives[call.Name]
	if n == nil {
		panic("codegen: unknown native call: " + call.Name)
	}
	if n.Template == nil {
		prelude, invoke := g.nativeSidecarCall(call, n)
		if g.isUnit(call.Ty) {
			prelude = append(prelude, exprStmt(invoke), returnStmt(g.unitValue()))
			return callExpr(funcLit(g.goType(call.Ty), prelude))
		}
		if len(prelude) != 0 {
			prelude = append(prelude, returnStmt(invoke))
			return callExpr(funcLit(g.goType(call.Ty), prelude))
		}
		return invoke
	}
	result := g.nativeTemplateExpr(call, *n.Template, parentPrec)
	if g.isUnit(call.Ty) {
		return callExpr(funcLit(g.goType(call.Ty), []goast.Stmt{exprStmt(result), returnStmt(g.unitValue())}))
	}
	return result
}

// nativeStmts emits a native call whose Unit result is not demanded. The Go
// operation stays void; UnitValue is introduced only by nativeExpr when an
// enclosing value context actually needs the singleton.
func (g *gen) nativeStmts(call *core.NativeCall) []goast.Stmt {
	n := g.natives[call.Name]
	if n == nil {
		panic("codegen: unknown native call: " + call.Name)
	}
	if n.Template != nil {
		return []goast.Stmt{exprStmt(g.nativeTemplateExpr(call, *n.Template, 0))}
	}
	prelude, invoke := g.nativeSidecarCall(call, n)
	return append(prelude, exprStmt(invoke))
}

func nativePlaceholderIndex(x goast.Expr) int {
	id := x.(*goast.Ident) // validated by modules.validateTemplate
	i, _ := strconv.Atoi(strings.TrimPrefix(id.Name, "__fango_p"))
	return i - 1
}

func (g *gen) nativeShow(t types.Type, value goast.Expr) goast.Expr {
	g.usesFangort = true
	if g.adtOf(t) != nil {
		return g.showCall(t, value, ident("false"))
	}
	name := map[int]string{
		g.b.Int.Unique: "ShowInt", g.b.Float.Unique: "ShowFloat",
		g.b.String.Unique: "ShowString", g.b.Bool.Unique: "ShowBool",
	}[g.unique(t)]
	if name == "" {
		panic("codegen: no native show implementation for " + types.Show(t))
	}
	return callExpr(selector("fangort", name), value)
}

func exportNativeName(name string) string {
	if name == "" {
		return ""
	}
	return strings.ToUpper(name[:1]) + name[1:]
}

const unaryPrec = 6

// expr emits e in expression context; parentPrec is the precedence of the
// enclosing operator (0 = none) for minimal parenthesization.
func (g *gen) expr(e core.Expr, parentPrec int) goast.Expr {
	switch e := e.(type) {
	case *core.IntLit:
		return intLit(e.Val)
	case *core.FloatLit:
		return g.floatLit(e.Val)
	case *core.StringLit:
		return stringLit(e.Val)
	case *core.UnitLit:
		return g.unitValue()
	case *core.BoolLit:
		return ident(strconv.FormatBool(e.Val))
	case *core.VarRef:
		// Unit is a singleton and Unit-typed locals are never emitted
		// (their effects ran at binding time) — materialize the value.
		if g.unique(e.Ty) == g.b.Unit.Unique {
			return g.unitValue()
		}
		if !e.Local && g.defs[e.Name] != nil {
			return g.topValueRef(e.Name)
		}
		return ident(mangleValue(e.Name))
	case *core.Let:
		return g.letIIFE(e)
	case *core.Lambda:
		// A typed func literal. Go captures variables by reference, but
		// fango bindings are immutable (the single letrec assignment
		// happens-before any call), so by-reference and by-value are
		// indistinguishable.
		fn := e.Ty.(*types.TFun)
		params := make([]paramSpec, 0, len(fn.Eff.Labels)+1)
		var pushed []int
		for _, l := range types.SortedRow(fn.Eff).Labels {
			if types.SurfaceName(l.Name) == "IO" {
				continue
			}
			name := g.evidenceName(l.Name)
			inst := core.EffectInstance{Unique: l.Unique, Name: l.Name, Args: l.Args}
			params = append(params, paramSpec{name: name, typ: g.effectType(inst)})
			g.evidence[l.Unique] = append(g.evidence[l.Unique], ident(name))
			pushed = append(pushed, l.Unique)
		}
		params = append(params, paramSpec{name: func() string {
			if e.Param == "_" {
				return "_"
			}
			return mangleValue(e.Param)
		}(), typ: g.goType(fn.Arg)})
		body := g.retStmts(e.Body)
		for _, unique := range pushed {
			g.evidence[unique] = g.evidence[unique][:len(g.evidence[unique])-1]
		}
		return funcLitParams(params, g.goType(fn.Ret), body)
	case *core.App:
		switch e.CalleeKind {
		case core.Worker:
			return g.workerCallExpr(e)
		case core.Value:
			// One typed indirect call per application; chains render
			// e(a)(b). Call is a Go primary expression — no parens needed,
			// and a func-literal callee called in place is legal Go.
			args := make([]goast.Expr, 0, len(e.EvidenceArgs)+1)
			for _, ev := range e.EvidenceArgs {
				stack := g.evidence[ev.Unique]
				if len(stack) == 0 {
					panic("codegen: missing lexical evidence")
				}
				args = append(args, stack[len(stack)-1])
			}
			args = append(args, g.expr(e.Args[0], 0))
			return callExpr(g.expr(e.Callee, 0), args...)
		case core.Ctor:
			return g.ctorLit(e)
		default:
			panic("codegen: App with unknown CalleeKind")
		}
	case *core.Neg:
		operand := g.expr(e.Operand, unaryPrec)
		// Guard `--x` (invalid Go) and precedence: parenthesize any
		// non-atomic operand.
		switch operand.(type) {
		case *goast.UnaryExpr, *goast.BinaryExpr:
			operand = &goast.ParenExpr{X: operand}
		}
		return parenIf(parentPrec > 0, &goast.UnaryExpr{Op: gotoken.SUB, X: operand})
	case *core.BinOp:
		// Equality at an ADT type calls the derived eq (doc/design.md, "Go backend and runtime"); everything
		// else — including Number-kinded type params (doc/design.md, "Type inference") — compiles to a
		// native Go operator.
		if e.Op == "==" || e.Op == "/=" {
			if g.adtOf(e.L.Type()) != nil {
				call := g.eqCall(e.L.Type(), g.expr(e.L, 0), g.expr(e.R, 0))
				if e.Op == "/=" {
					return &goast.UnaryExpr{Op: gotoken.NOT, X: call}
				}
				return call
			}
		}
		op, ok := goOps[e.Op]
		if !ok {
			panic(fmt.Sprintf("codegen: unhandled operator %q", e.Op))
		}
		prec := goPrec(e.Op)
		// Left child may share our precedence (left associativity);
		// right child needs parens at equal precedence.
		l := g.expr(e.L, prec)
		r := g.expr(e.R, prec+1)
		return parenIf(prec < parentPrec, binExpr(op, l, r))
	case *core.NativeCall:
		return g.nativeExpr(e, parentPrec)
	case *core.If:
		// Go has no expression-if: an immediately-invoked typed closure
		// preserves branch laziness and stays gofmt-clean. ANF hoisting, as
		// documented in doc/design.md, "Core and evidence invariants", bypasses this inside function bodies; it
		// remains the top-level-initializer fallback.
		body := []goast.Stmt{
			&goast.IfStmt{
				Cond: g.expr(e.Cond, 0),
				Body: &goast.BlockStmt{List: []goast.Stmt{returnStmt(g.expr(e.Then, 0))}},
			},
			returnStmt(g.expr(e.Else, 0)),
		}
		return callExpr(funcLit(g.goType(e.Ty), body))
	case *core.Case:
		// Expression-context fallback (top-level initializers): an
		// immediately-invoked typed closure, exactly like If above. Inside
		// function bodies the elaborator's ANF hoisting bypasses this.
		return callExpr(funcLit(g.goType(e.Ty), g.caseStmts(e, g.retStmts)))
	case *core.Perform:
		if e.Op.Native != nil && types.SurfaceName(e.Op.Owner.Name) == "IO" {
			return g.nativeExpr(&core.NativeCall{Name: e.Op.Native.Name, Module: e.Op.Native.Module, Args: e.Args, Ty: e.Ty}, parentPrec)
		}
		stack := g.evidence[e.Effect.Unique]
		if len(stack) == 0 {
			panic("codegen: custom Perform without evidence")
		}
		args := make([]goast.Expr, 0, len(e.Args))
		needPrelude := false
		for i, a := range e.Args {
			if i < len(e.Op.ParamTypes) && g.isUnit(e.Op.ParamTypes[i]) {
				if !unitAtom(a) {
					needPrelude = true
				}
				continue
			}
			args = append(args, g.expr(a, 0))
		}
		callOp := func(as []goast.Expr) goast.Expr {
			return callExpr(&goast.SelectorExpr{X: stack[len(stack)-1], Sel: ident("Op_" + linkName(e.Op.Name))}, as...)
		}
		call := callOp(args)
		if needPrelude {
			body := []goast.Stmt{}
			args = args[:0]
			for i, a := range e.Args {
				if i < len(e.Op.ParamTypes) && g.isUnit(e.Op.ParamTypes[i]) {
					body = append(body, g.stmts(a)...)
					continue
				}
				name := fmt.Sprintf("t_u%d", g.tmp)
				g.tmp++
				body = append(body, varDeclStmt(name, g.goType(a.Type()), g.expr(a, 0)))
				args = append(args, ident(name))
			}
			call = callOp(args)
			if g.isUnit(e.Op.ResultType) {
				return callExpr(funcLit(g.goType(e.Ty), append(body, exprStmt(call), returnStmt(g.unitValue()))))
			}
			return callExpr(funcLit(g.goType(e.Ty), append(body, returnStmt(call))))
		}
		if g.isUnit(e.Op.ResultType) {
			return callExpr(funcLit(g.goType(e.Ty), []goast.Stmt{exprStmt(call), returnStmt(g.unitValue())}))
		}
		return call
	case *core.Resume:
		return g.expr(e.Value, parentPrec)
	case *core.Seq:
		return callExpr(funcLit(g.goType(e.Ty), append(g.stmts(e.First), returnStmt(g.expr(e.Then, 0)))))
	case *core.Handle:
		return g.handleExpr(e)
	default:
		panic(fmt.Sprintf("codegen: node %T arrives in a later slice", e))
	}
}

func (g *gen) workerCallExpr(e *core.App) goast.Expr {
	ref := e.Callee.(*core.VarRef)
	formal, voidResult := g.workerABI(ref.Name)
	args := make([]goast.Expr, 0, len(e.EvidenceArgs)+len(e.Args))
	for _, ev := range e.EvidenceArgs {
		stack := g.evidence[ev.Unique]
		if len(stack) == 0 {
			panic("codegen: missing lexical evidence")
		}
		args = append(args, stack[len(stack)-1])
	}
	needPrelude := false
	for i, a := range e.Args {
		if i < len(formal) && g.isUnit(formal[i]) {
			if !unitAtom(a) {
				needPrelude = true
			}
			continue
		}
		args = append(args, g.expr(a, 0))
	}
	if needPrelude {
		var body []goast.Stmt
		args = args[:len(e.EvidenceArgs)]
		for i, a := range e.Args {
			if i < len(formal) && g.isUnit(formal[i]) {
				body = append(body, g.stmts(a)...)
				continue
			}
			name := fmt.Sprintf("t_u%d", g.tmp)
			g.tmp++
			body = append(body, varDeclStmt(name, g.goType(a.Type()), g.expr(a, 0)))
			args = append(args, ident(name))
		}
		call := callExpr(indexExpr(g.topValueRef(ref.Name), g.goTypes(e.TyArgs)), args...)
		if voidResult {
			body = append(body, exprStmt(call), returnStmt(g.unitValue()))
		} else {
			body = append(body, returnStmt(call))
		}
		return callExpr(funcLit(g.goType(e.Ty), body))
	}
	call := callExpr(indexExpr(g.topValueRef(ref.Name), g.goTypes(e.TyArgs)), args...)
	if voidResult {
		return callExpr(funcLit(g.goType(e.Ty), []goast.Stmt{exprStmt(call), returnStmt(g.unitValue())}))
	}
	return call
}

func unitAtom(e core.Expr) bool {
	switch e.(type) {
	case *core.UnitLit, *core.VarRef:
		return true
	default:
		return false
	}
}

func (g *gen) handleExpr(e *core.Handle) goast.Expr {
	fields := make([]*goast.Field, len(e.Clauses))
	elts := make([]goast.Expr, len(e.Clauses))
	for i, c := range e.Clauses {
		params := make([]paramSpec, 0, len(c.Params))
		for j, p := range c.Params {
			if p == "()" || p == "_" {
				p = "_"
			} else {
				p = mangleValue(p)
			}
			if g.isUnit(c.Op.ParamTypes[j]) {
				continue
			}
			params = append(params, paramSpec{name: p, typ: g.goType(c.ParamTypes[j])})
		}
		results := &goast.FieldList{}
		if !g.isUnit(c.Op.ResultType) {
			results = &goast.FieldList{List: []*goast.Field{{Type: g.goType(c.ResultType)}}}
		}
		ft := &goast.FuncType{Params: paramFields(params), Results: results}
		fields[i] = &goast.Field{Names: []*goast.Ident{ident("Op_" + linkName(c.Op.Name))}, Type: ft}
		fn := &goast.FuncLit{Type: ft, Body: &goast.BlockStmt{List: g.resumeStmtsFor(c.Body, g.isUnit(c.Op.ResultType))}}
		elts[i] = &goast.KeyValueExpr{Key: ident("Op_" + linkName(c.Op.Name)), Value: fn}
	}
	_ = fields
	st := g.effectType(e.Effect)
	name := fmt.Sprintf("ev%d", g.tmp)
	g.tmp++
	decl := varDeclStmt(name, st, &goast.CompositeLit{Type: st, Elts: elts})
	g.evidence[e.Effect.Unique] = append(g.evidence[e.Effect.Unique], ident(name))
	body := g.expr(e.Body, 0)
	g.evidence[e.Effect.Unique] = g.evidence[e.Effect.Unique][:len(g.evidence[e.Effect.Unique])-1]
	// A handler whose subject does not perform the handled effect still
	// constructs valid lexical evidence; keep the local legal in Go even
	// when no generated operation call refers to it.
	stmts := []goast.Stmt{decl, assignBlank(ident(name))}
	if e.Return == nil {
		stmts = append(stmts, returnStmt(body))
		return callExpr(funcLit(g.goType(e.Ty), stmts))
	}
	p := e.Return.Param
	if p == "_" || p == "()" {
		stmts = append(stmts, assignBlank(body))
	} else {
		stmts = append(stmts, varDeclStmt(mangleValue(p), g.goType(e.Body.Type()), body))
	}
	stmts = append(stmts, returnStmt(g.expr(e.Return.Body, 0)))
	return callExpr(funcLit(g.goType(e.Ty), stmts))
}

// resumeStmts lowers a proven tail-resumptive clause. A tail `resume v`
// becomes a direct return of v from the evidence operation field; the
// caller's ordinary Go continuation then proceeds with that operation
// result. No continuation object or non-local control transfer is needed.
func (g *gen) resumeStmts(e core.Expr) []goast.Stmt {
	return g.resumeStmtsFor(e, false)
}

func (g *gen) resumeStmtsFor(e core.Expr, unitResult bool) []goast.Stmt {
	switch e := e.(type) {
	case *core.Resume:
		if unitResult {
			return append(g.stmts(e.Value), bareReturnStmt())
		}
		return []goast.Stmt{returnStmt(g.expr(e.Value, 0))}
	case *core.Let:
		return append(g.letBindingStmts(e), g.resumeStmtsFor(e.Body, unitResult)...)
	case *core.Seq:
		return append(g.stmts(e.First), g.resumeStmtsFor(e.Then, unitResult)...)
	case *core.If:
		return []goast.Stmt{ifStmt(g.expr(e.Cond, 0), g.resumeStmtsFor(e.Then, unitResult), g.resumeStmtsFor(e.Else, unitResult))}
	case *core.Case:
		return g.caseStmts(e, func(x core.Expr) []goast.Stmt { return g.resumeStmtsFor(x, unitResult) })
	default:
		panic(fmt.Sprintf("codegen: non-tail-resumptive clause node %T", e))
	}
}

func (g *gen) effectType(e core.EffectInstance) goast.Expr {
	return indexExpr(g.qualified(symbolOwner(e.Name), "Eff_"+linkName(e.Name)), g.goTypes(e.Args))
}

func (g *gen) effectDecls(effects []*types.EffectInfo) []goast.Decl {
	var out []goast.Decl
	for _, eff := range effects {
		if types.SurfaceName(eff.Name) == "IO" {
			continue
		}
		old := g.tyParamNames
		g.tyParamNames = map[int]string{}
		fields := make([]*goast.Field, len(eff.Ops))
		for i, p := range eff.Params {
			g.tyParamNames[p.ID] = fmt.Sprintf("E%d", i)
		}
		// Operation-local polymorphism is rejected at every runtime use in
		// the current tail-resumptive handler runtime. Keeping its otherwise-unrepresentable field slots as
		// any lets unused declarations still have deterministic named structs.
		for _, op := range eff.Ops {
			for _, v := range op.LocalVars {
				g.tyParamNames[v.ID] = "any"
			}
		}
		for i, op := range eff.Ops {
			ps := make([]paramSpec, 0, len(op.ParamTypes))
			for _, t := range op.ParamTypes {
				if g.isUnit(t) {
					continue
				}
				ps = append(ps, paramSpec{typ: g.goType(t)})
			}
			results := &goast.FieldList{}
			if !g.isUnit(op.ResultType) {
				results = &goast.FieldList{List: []*goast.Field{{Type: g.goType(op.ResultType)}}}
			}
			fields[i] = &goast.Field{Names: []*goast.Ident{ident("Op_" + linkName(op.Name))}, Type: &goast.FuncType{Params: paramFields(ps), Results: results}}
		}
		spec := &goast.TypeSpec{Name: ident("Eff_" + linkName(eff.Name)), Type: &goast.StructType{Fields: &goast.FieldList{List: fields}}}
		if len(eff.Params) > 0 {
			fs := make([]*goast.Field, len(eff.Params))
			for i := range fs {
				fs[i] = &goast.Field{Names: []*goast.Ident{ident(fmt.Sprintf("E%d", i))}, Type: ident("any")}
			}
			spec.TypeParams = &goast.FieldList{List: fs}
		}
		out = append(out, &goast.GenDecl{Tok: gotoken.TYPE, Specs: []goast.Spec{spec}})
		g.tyParamNames = old
	}
	return out
}

// floatLit emits a Float literal. Finite non-negative-zero values round-trip
// exactly through the shortest 'g' form; specials cannot be written as Go
// constants and go through math (imported on demand).
func (g *gen) floatLit(v float64) goast.Expr {
	switch {
	case math.IsNaN(v):
		g.usesMath = true
		return callExpr(selector("math", "NaN"))
	case math.IsInf(v, 1):
		g.usesMath = true
		return callExpr(selector("math", "Inf"), intLit(1))
	case math.IsInf(v, -1):
		g.usesMath = true
		return callExpr(selector("math", "Inf"), intLit(-1))
	case v == 0 && math.Signbit(v):
		g.usesMath = true
		return callExpr(selector("math", "Copysign"), intLit(0), intLit(-1))
	case v < 0:
		return &goast.UnaryExpr{
			Op: gotoken.SUB,
			X:  &goast.BasicLit{Kind: gotoken.FLOAT, Value: strconv.FormatFloat(-v, 'g', -1, 64)},
		}
	default:
		return &goast.BasicLit{Kind: gotoken.FLOAT, Value: strconv.FormatFloat(v, 'g', -1, 64)}
	}
}

// letIIFE collapses a Let chain into one immediately-invoked closure:
// `func() T { var v_r float64 = …; …; return result }()`. The expression-
// context fallback; statement contexts (main's body, and function bodies
// emit the bindings as plain Go statements instead.
func (g *gen) letIIFE(e *core.Let) goast.Expr {
	var body []goast.Stmt
	var cur core.Expr = e
	for {
		let, ok := cur.(*core.Let)
		if !ok {
			break
		}
		body = append(body, g.letBindingStmts(let)...)
		cur = let.Body
	}
	body = append(body, returnStmt(g.expr(cur, 0)))
	return callExpr(funcLit(g.goType(e.Ty), body))
}

// letBindingStmts emits one binding: Unit-typed right-hand sides run as
// statements (their value is the singleton; prints must still execute),
// other bindings become `var` declarations, kept alive with `_ =` when the
// rest of the chain never mentions them (Go rejects unused locals; fango
// bindings still evaluate eagerly).
func (g *gen) letBindingStmts(let *core.Let) []goast.Stmt {
	if let.Rec {
		// A Go local is not in scope inside its own initializer:
		// declare, then assign — the standard recursive-closure idiom.
		name := mangleValue(let.Name)
		return []goast.Stmt{
			varDeclNoValue(name, g.goType(let.Rhs.Type())),
			assignStmt(name, g.expr(let.Rhs, 0)),
		}
	}
	if g.unique(let.Rhs.Type()) == g.b.Unit.Unique {
		return g.stmts(let.Rhs)
	}
	name := mangleValue(let.Name)
	var stmts []goast.Stmt
	switch let.Rhs.(type) {
	case *core.If, *core.Case:
		// The ANF target shape from doc/design.md, "Core and evidence invariants": declare, then assign inside real Go
		// statements — no IIFE closure on hot paths.
		stmts = append([]goast.Stmt{varDeclNoValue(name, g.goType(let.Rhs.Type()))},
			g.assignStmts(let.Rhs, name)...)
	default:
		stmts = []goast.Stmt{varDeclStmt(name, g.goType(let.Rhs.Type()), g.expr(let.Rhs, 0))}
	}
	if !core.Mentions(let.Body, let.Name) {
		stmts = append(stmts, assignBlank(ident(name)))
	}
	return stmts
}

// stmts emits a Unit-typed expression in statement context — func main()'s
// body. Prints become fangort calls; ifs become genuine Go if statements;
// Let bindings become plain locals.
func (g *gen) stmts(e core.Expr) []goast.Stmt {
	switch e := e.(type) {
	case *core.App:
		if e.CalleeKind == core.Worker {
			return []goast.Stmt{g.workerCallStmt(e)}
		}
		return []goast.Stmt{assignBlank(g.expr(e, 0))}
	case *core.Perform:
		if e.Op.Native != nil && types.SurfaceName(e.Op.Owner.Name) == "IO" {
			return g.nativeStmts(&core.NativeCall{Name: e.Op.Native.Name, Module: e.Op.Native.Module, Args: e.Args, Ty: e.Ty})
		}
		return []goast.Stmt{assignBlank(g.expr(e, 0))}
	case *core.Seq:
		return append(g.stmts(e.First), g.stmts(e.Then)...)
	case *core.If:
		return []goast.Stmt{ifStmt(g.expr(e.Cond, 0), g.stmts(e.Then), g.stmts(e.Else))}
	case *core.Case:
		return g.caseStmts(e, g.stmts)
	case *core.Let:
		return append(g.letBindingStmts(e), g.stmts(e.Body)...)
	default:
		// Unit-typed but effect-free — normally unreachable because Unit is
		// constructible via print); discard defensively.
		if g.isUnit(e.Type()) {
			if _, ok := e.(*core.UnitLit); ok {
				return nil
			}
			if _, ok := e.(*core.VarRef); ok {
				return nil
			}
		}
		return []goast.Stmt{assignBlank(g.expr(e, 0))}
	}
}

// mangleValue maps a fango value name into the generated package's `v_`
// namespace (constructors use C_, types T_). Elaboration temporaries start
// with `_` — unlexable as fango identifiers — and land in a disjoint `t`
// namespace (`_w0` → `t_w0`) so they can never collide with user names.
func mangleValue(name string) string {
	if strings.HasPrefix(name, "_") {
		return "t" + name
	}
	return "v_" + linkName(name)
}

func linkName(name string) string { return strings.ReplaceAll(name, ".", "_dot_") }
