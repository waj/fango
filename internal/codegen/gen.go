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
	machineir "github.com/waj/fango/internal/machine"
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
	return emitProject(p, nil, b, units, printMain)
}

// EmitMachineProject emits selective Machine definitions as iterative
// fangort frames while every other definition retains the ordinary
// Direct/Exit path. The source pipeline selects it only for the resolved
// Generator.withIterator owner boundary.
func EmitMachineProject(p *core.Prog, mp *machineir.Prog, b *types.Builtins, units []Unit, printMain bool) ([]File, error) {
	if errs := machineir.Lint(mp); len(errs) != 0 {
		return nil, fmt.Errorf("codegen: malformed machine IR: %v", errs[0])
	}
	return emitProject(p, mp, b, units, printMain)
}

func emitProject(p *core.Prog, mp *machineir.Prog, b *types.Builtins, units []Unit, printMain bool) ([]File, error) {
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
		data, err := emitUnitWithMachine(p, mp, b, unit, printMain)
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
	return emitUnitWithMachine(p, nil, b, unit, printMain)
}

func emitUnitWithMachine(p *core.Prog, mp *machineir.Prog, b *types.Builtins, unit Unit, printMain bool) ([]byte, error) {
	g := &gen{
		b:               b,
		adts:            map[int]*types.ADTInfo{},
		neededEq:        map[int]bool{},
		neededShow:      map[int]bool{},
		scalarEq:        map[int]bool{},
		scalarShow:      map[int]bool{},
		caseVarTys:      map[string]types.Type{},
		evidence:        map[int][]goast.Expr{},
		evidenceModes:   map[int][]types.Transport{},
		defs:            map[string]*core.Def{},
		unit:            unit.Name,
		imports:         map[string]bool{},
		nativeImports:   map[string]bool{},
		direct:          map[string]bool{},
		natives:         p.Natives,
		effects:         map[int]*types.EffectInfo{},
		control:         types.Direct,
		abi:             types.Direct,
		machine:         mp != nil,
		machineClosures: map[*core.Lambda]*machineir.Closure{},
		machineWorkers:  map[string]*machineir.Worker{},
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
	for _, effect := range p.Effects {
		g.effects[effect.Unique] = effect
	}
	if unit.Entry {
		for _, native := range p.Natives {
			if native.Template == nil {
				g.nativeImports[native.Module] = false // force sidecar compilation even when unused
			}
		}
	}

	var mainDef *core.Def
	machineWorkers := map[string]*machineir.Worker{}
	if mp != nil {
		for i := range mp.Workers {
			machineWorkers[mp.Workers[i].Name] = &mp.Workers[i]
			g.machineWorkers[mp.Workers[i].Name] = &mp.Workers[i]
		}
		for i := range mp.Closures {
			g.machineClosures[mp.Closures[i].Expr] = &mp.Closures[i]
		}
	}
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
		if machineWorkers[d.Name] != nil {
			continue
		}
		if d.IsWorker() {
			// Includes nullary generic workers — polymorphic values emit as
			// zero-parameter generic functions (doc/design.md, "Go backend and runtime").
			baseMode := d.Control.Transport
			decls = append(decls, g.workerDef(d, baseMode, baseMode))
			if baseMode != types.Exit && (d.Control.Polymorphic || g.workerNeedsABIFamily(d)) {
				execution := baseMode
				// A worker that consumes a controlled callback must run in
				// Exit control as well as use the Exit representation family:
				// invoking that callback can return an Outcome which has to
				// propagate through this worker. Workers whose controlled
				// values are only in their result may keep Direct execution;
				// their Exit member is only a representation-family variant.
				if d.Control.Polymorphic || g.workerCallsControlledArg(d) {
					execution = types.Exit
				}
				decls = append(decls, g.workerDef(d, execution, types.Exit))
			}
			continue
		}
		g.tyParamNames = nil
		decls = append(decls, g.topValueDecl(d, types.Direct))
		if g.controlledType(d.Type, nil) {
			decls = append(decls, g.topValueDecl(d, types.Exit))
		}
	}
	if mp != nil {
		machineDecls, err := g.machineDecls(mp)
		if err != nil {
			return nil, err
		}
		decls = append(decls, machineDecls...)
	}

	switch {
	case mainDef != nil && machineWorkers[mainDef.Name] != nil:
		// The private fixture driver owns Run/Resume. Keep a valid entry
		// package without choosing a source-level suspension policy.
		decls = append(decls, funcDecl("main", assignBlank(callExpr(g.machineConstructorRef(mainDef.Name)))))
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

func (g *gen) topValueDecl(d *core.Def, mode types.Transport) goast.Decl {
	oldControl, oldABI := g.control, g.abi
	g.control, g.abi = mode, mode
	defer func() { g.control, g.abi = oldControl, oldABI }()
	name := g.topValueName(d.Name)
	if mode == types.Exit {
		name += "_exit"
	}
	return varDecl(name, g.goType(d.Type), g.expr(d.Body, 0))
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

	// caseVarTys records the (instantiated) types of locals visible to a
	// decision tree: worker/lambda parameters, case scrutinee binders, and
	// constructor field temporaries. Multi-column pattern matrices may test
	// any parameter directly, so constructor switches need all of them here.
	caseVarTys    map[string]types.Type
	evidence      map[int][]goast.Expr
	evidenceModes map[int][]types.Transport
	defs          map[string]*core.Def
	unit          string
	imports       map[string]bool
	nativeImports map[string]bool
	direct        map[string]bool
	natives       map[string]*types.NativeInfo
	effects       map[int]*types.EffectInfo
	control       types.Transport
	// abi selects the Direct/Exit representation family for controlled
	// function and ADT values in the declaration currently being emitted.
	// Unlike control, it does not change when emission enters a pure nested
	// lambda: that lambda still consumes and produces the enclosing family's
	// representations even though its own call returns directly.
	abi             types.Transport
	resultType      types.Type
	machine         bool
	machineClosures map[*core.Lambda]*machineir.Closure
	machineWorkers  map[string]*machineir.Worker
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
	return g.topValueRefMode(name, types.Direct)
}

func (g *gen) topValueRefMode(name string, mode types.Transport) goast.Expr {
	owner := symbolOwner(name)
	if d := g.defs[name]; d != nil {
		owner = d.Owner
	}
	link := g.topValueName(name)
	if mode == types.Exit {
		link += "_exit"
	} else if mode == types.Machine {
		link += "_machine"
	}
	return g.qualified(owner, link)
}

func (g *gen) representationMode() types.Transport {
	if g.control > g.abi {
		return g.control
	}
	return g.abi
}

func (g *gen) typeRef(adt *types.ADTInfo) goast.Expr {
	name := mangleType(adt.Con.Name)
	if g.controlledType(adt.Con, nil) {
		switch g.representationMode() {
		case types.Exit:
			name += "_exit"
		case types.Machine:
			name += "_machine"
		}
	}
	return g.qualified(symbolOwner(adt.Con.Name), name)
}

func (g *gen) ctorRef(ctor *types.CtorInfo) goast.Expr {
	name := mangleCtor(ctor.Name)
	if g.controlledType(ctor.Result, nil) {
		switch g.representationMode() {
		case types.Exit:
			name += "_exit"
		case types.Machine:
			name += "_machine"
		}
	}
	return g.qualified(symbolOwner(ctor.Result.Name), name)
}

func (g *gen) controlledType(t types.Type, visiting map[int]bool) bool {
	switch t := t.(type) {
	case *types.TFun:
		return types.FunctionControl(t) != (types.Control{}) || g.controlledType(t.Arg, visiting) || g.controlledType(t.Ret, visiting)
	case *types.TCon:
		for _, a := range t.Args {
			if g.controlledType(a, visiting) {
				return true
			}
		}
		adt := g.adts[t.Unique]
		if adt == nil {
			return false
		}
		if visiting == nil {
			visiting = map[int]bool{}
		}
		if visiting[t.Unique] {
			return false
		}
		visiting[t.Unique] = true
		defer delete(visiting, t.Unique)
		for _, c := range adt.Ctors {
			for _, f := range c.Fields {
				if g.controlledType(f, visiting) {
					return true
				}
			}
		}
	}
	return false
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
	sort.Slice(specs, func(i, j int) bool { return specs[i].path < specs[j].path })
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
	case g.b.Char.Unique:
		return "PrintChar"
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
func (g *gen) workerDef(d *core.Def, mode, abi types.Transport) goast.Decl {
	oldControl, oldABI, oldResult := g.control, g.abi, g.resultType
	g.control, g.abi, g.resultType = mode, abi, func() types.Type {
		_, ret := core.PeelFun(d.Type, len(d.Params))
		return ret
	}()
	defer func() { g.control, g.abi, g.resultType = oldControl, oldABI, oldResult }()
	g.tyParamNames = tyParamNames(d.TyParams)
	argTys, ret := core.PeelFun(d.Type, len(d.Params))
	for i, name := range d.Params {
		g.caseVarTys[name] = argTys[i]
	}
	defer func() {
		for _, name := range d.Params {
			delete(g.caseVarTys, name)
		}
	}()
	params := make([]paramSpec, 0, len(d.EffectParams)+len(d.Params))
	for _, ev := range d.EffectParams {
		name := g.evidenceName(ev.Name)
		params = append(params, paramSpec{name: name, typ: g.effectType(ev)})
		g.evidence[ev.Unique] = append(g.evidence[ev.Unique], ident(name))
		g.evidenceModes[ev.Unique] = append(g.evidenceModes[ev.Unique], mode)
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
	if mode == types.Exit {
		result = g.outcomeType(ret)
	} else if !g.isUnit(ret) {
		result = g.goType(ret)
	}
	// Self tail calls compile to loops (doc/design.md, "Go backend and
	// runtime"): an eligible body emits as one `for` statement whose leaves
	// either return or jump. A `for` with no break is a terminating
	// statement in Go, so no trailing return is needed in either result
	// shape.
	var body []goast.Stmt
	if _, ok := core.DetectTailLoop(d); ok && mode == types.Direct {
		body = []goast.Stmt{&goast.ForStmt{Body: &goast.BlockStmt{List: g.loopStmts(d, d.Body, g.isUnit(ret))}}}
	} else {
		body = g.retStmtsFor(d.Body, g.isUnit(ret))
	}
	name := g.topValueName(d.Name)
	if abi == types.Exit {
		name += "_exit"
	}
	decl := workerDecl(name, params, result, body).(*goast.FuncDecl)
	for _, ev := range d.EffectParams {
		g.evidence[ev.Unique] = g.evidence[ev.Unique][:len(g.evidence[ev.Unique])-1]
		g.evidenceModes[ev.Unique] = g.evidenceModes[ev.Unique][:len(g.evidenceModes[ev.Unique])-1]
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
	d := g.defs[name]
	mode := types.Direct
	abi := types.Direct
	if d != nil {
		mode = d.Control.Resolve(g.control)
		abi = g.workerCallABI(d, mode)
	}
	call := callExpr(g.topValueRefMode(name, abi), args...)
	if args == nil {
		call = callExpr(g.topValueRefMode(name, abi))
	}
	return []goast.Stmt{exprStmt(call)}
}

func (g *gen) workerCallABI(d *core.Def, execution types.Transport) types.Transport {
	if execution == types.Exit {
		return types.Exit
	}
	// A transport-polymorphic worker's existing Direct/Exit pair is joined:
	// its execution choice also selects the family. Only a control-fixed
	// worker that produces a controlled value needs an independent
	// Exit-family member.
	if d != nil && !d.Control.Polymorphic && g.representationMode() == types.Exit && g.workerNeedsABIFamily(d) {
		return types.Exit
	}
	return types.Direct
}

func (g *gen) workerNeedsABIFamily(d *core.Def) bool {
	args, ret := core.PeelFun(d.Type, len(d.Params))
	if g.workerNeedsControlledArgTypes(args) {
		return true
	}
	return g.controlledType(ret, nil)
}

// workerCallsControlledArg reports whether the worker actually invokes one of
// its controlled function parameters. Merely storing such a callback (for
// example in an ADT) needs the Exit representation family, but does not make
// the worker itself return an Outcome.
func (g *gen) workerCallsControlledArg(d *core.Def) bool {
	args, _ := core.PeelFun(d.Type, len(d.Params))
	controlled := map[string]bool{}
	for i, arg := range args {
		if i < len(d.Params) && g.controlledType(arg, nil) {
			controlled[d.Params[i]] = true
		}
	}
	if len(controlled) == 0 {
		return false
	}
	called := false
	core.Rewrite(d.Body, func(t types.Type) types.Type { return t }, func(e core.Expr) core.Expr {
		if app, ok := e.(*core.App); ok {
			for name := range controlled {
				if core.Mentions(app.Callee, name) {
					called = true
				}
			}
		}
		return e
	})
	return called
}

func (g *gen) workerNeedsControlledArgTypes(args []types.Type) bool {
	for _, arg := range args {
		if g.controlledType(arg, nil) {
			return true
		}
	}
	return false
}

func (g *gen) workerCallStmt(e *core.App) goast.Stmt {
	ref := e.Callee.(*core.VarRef)
	formal, voidResult := g.workerABI(ref.Name)
	if !voidResult {
		return assignBlank(g.workerCallExpr(e))
	}
	if g.workerNeedsNormalProjection(e) {
		return assignBlank(g.workerCallExpr(e))
	}
	for i, a := range e.Args {
		if i < len(formal) && g.isUnit(formal[i]) && !unitAtom(a) {
			return exprStmt(g.workerCallExpr(e))
		}
	}
	mode := e.Control.Resolve(g.control)
	abi := g.workerCallABI(g.defs[ref.Name], mode)
	args := make([]goast.Expr, 0, len(e.EvidenceArgs)+len(e.Args))
	for _, ev := range e.EvidenceArgs {
		stack := g.evidence[ev.Unique]
		if len(stack) == 0 {
			panic("codegen: missing lexical evidence")
		}
		args = append(args, g.evidenceArg(ev, stack[len(stack)-1], g.currentEvidenceMode(ev.Unique), mode))
	}
	for i, a := range e.Args {
		if i < len(formal) && g.isUnit(formal[i]) {
			continue
		}
		args = append(args, g.expr(a, 0))
	}
	return exprStmt(callExpr(indexExpr(g.topValueRefMode(ref.Name, abi), g.goTypes(e.TyArgs)), args...))
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

func runtimeADTParams(adt *types.ADTInfo) []*types.TVar {
	var out []*types.TVar
	for _, v := range adt.Params {
		if v.Kind != types.RowVar {
			out = append(out, v)
		}
	}
	return out
}

func runtimeADTArgs(adt *types.ADTInfo, args []types.Type) []types.Type {
	var out []types.Type
	for i, a := range args {
		if i < len(adt.Params) && adt.Params[i].Kind == types.RowVar {
			continue
		}
		out = append(out, a)
	}
	return out
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
	case *core.Seq:
		return append(g.stmts(e.First), g.retStmtsFor(e.Then, unitResult)...)
	default:
		if g.control == types.Exit {
			if core.ExprControl(e).Resolve(g.control) == types.Exit {
				return []goast.Stmt{returnStmt(g.expr(e, 0))}
			}
			if unitResult {
				return append(g.stmts(e), returnStmt(g.normalOutcome(g.resultType, g.unitValue())))
			}
			return []goast.Stmt{returnStmt(g.normalOutcome(g.resultType, g.expr(e, 0)))}
		}
		if unitResult {
			return append(g.stmts(e), bareReturnStmt())
		}
		return []goast.Stmt{returnStmt(g.expr(e, 0))}
	}
}

// keepUnused blanks a binding the body never reads. Go rejects an unused
// local, and a handler's return transformation is free to ignore the handled
// value or the final state.
func (g *gen) keepUnused(body core.Expr, name string, ty types.Type) []goast.Stmt {
	if name == "_" || name == "()" {
		return nil
	}
	if !g.isUnit(ty) && core.Mentions(body, name) {
		return nil
	}
	return []goast.Stmt{assignBlank(ident(mangleValue(name)))}
}

// unitValueRetStmts emits a Unit expression for a Go closure that represents
// Unit as fangort.Unit. Ordinary Direct workers erase Unit results and use a
// bare return, but expression closures must return the singleton value.
func (g *gen) unitValueRetStmts(e core.Expr) []goast.Stmt {
	switch e := e.(type) {
	case *core.Let:
		return append(g.letBindingStmts(e), g.unitValueRetStmts(e.Body)...)
	case *core.If:
		stmts := []goast.Stmt{&goast.IfStmt{
			Cond: g.expr(e.Cond, 0),
			Body: &goast.BlockStmt{List: g.unitValueRetStmts(e.Then)},
		}}
		return append(stmts, g.unitValueRetStmts(e.Else)...)
	case *core.Case:
		return g.caseStmts(e, g.unitValueRetStmts)
	case *core.Seq:
		return append(g.stmts(e.First), g.unitValueRetStmts(e.Then)...)
	default:
		return append(g.stmts(e), returnStmt(g.unitValue()))
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
		arrowControl := types.FunctionControl(t)
		mode := arrowControl.Resolve(g.representationMode())
		for _, l := range types.SortedRow(t.Eff).Labels {
			if !types.RuntimeEvidenceEffect(l) {
				continue
			}
			params = append(params, paramSpec{typ: g.effectTypeMode(core.EffectInstance{Unique: l.Unique, Name: l.Name, Args: l.Args, Control: arrowControl}, mode)})
		}
		params = append(params, paramSpec{typ: g.goType(t.Arg)})
		result := g.goType(t.Ret)
		if mode == types.Machine {
			g.usesFangort = true
			result = selector("fangort", "MachineFrame")
		} else if mode == types.Exit {
			result = g.outcomeType(t.Ret)
		}
		return &goast.FuncType{Params: paramFields(params), Results: &goast.FieldList{List: []*goast.Field{{Type: result}}}}
	case *types.TCon:
		if t.Name == types.IteratorTypeName {
			g.usesFangort = true
			return &goast.StarExpr{X: selector("fangort", "MachineIterator")}
		}
		switch t.Unique {
		case g.b.Int.Unique:
			return ident("int64")
		case g.b.Float.Unique:
			return ident("float64")
		case g.b.String.Unique:
			return ident("string")
		case g.b.Char.Unique:
			return ident("rune")
		case g.b.Bool.Unique:
			return ident("bool")
		case g.b.Unit.Unique:
			return g.unitType()
		default:
			if adt, ok := g.adts[t.Unique]; ok {
				if adt.Repr == types.ReprList {
					// The bundled List is one runtime type for both ABI
					// families: its own fields cannot be controlled, so the
					// Direct/Exit distinction rides entirely on the element
					// argument, which the generic parameter absorbs.
					g.usesFangort = true
					return indexExpr(selector("fangort", "List"), g.goTypes(runtimeADTArgs(adt, t.Args)))
				}
				return indexExpr(g.typeRef(adt), g.goTypes(runtimeADTArgs(adt, t.Args)))
			}
			panic(fmt.Sprintf("codegen: unknown type constructor %s", t.Name))
		}
	default:
		panic(fmt.Sprintf("codegen: unhandled type %s", types.Show(t)))
	}
}

func (g *gen) outcomeType(t types.Type) goast.Expr {
	g.usesFangort = true
	return indexExpr(selector("fangort", "Outcome"), []goast.Expr{g.goType(t)})
}

func (g *gen) normalOutcome(t types.Type, value goast.Expr) goast.Expr {
	g.usesFangort = true
	return callExpr(indexExpr(selector("fangort", "Normal"), []goast.Expr{g.goType(t)}), value)
}

func (g *gen) propagateOutcome(t types.Type, exit goast.Expr) goast.Expr {
	g.usesFangort = true
	return callExpr(indexExpr(selector("fangort", "Propagate"), []goast.Expr{g.goType(t)}), exit)
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

// nativeSidecarCall lowers a sidecar invocation without deciding whether its
// result is needed as a value. Unit arguments are erased from the Go ABI, but
// a non-atomic Unit argument must still run in source order; prelude contains
// the statements (and any temporaries) needed to preserve that order.
//
// Boundary wrappers (doc/design.md, "Go backend and runtime") are erased
// here: a wrapped argument is projected to its scalar field and a wrapped
// result is rebuilt with the wrapper's constructor. A wrapped argument always
// goes through a typed temporary, because the projection is a type assertion
// on the ADT's interface type and a constructor literal would not have it.
func (g *gen) nativeSidecarCall(call *core.NativeCall, n *types.NativeInfo) ([]goast.Stmt, goast.Expr) {
	g.nativeImports[n.Module] = true
	args := make([]goast.Expr, 0, len(call.Args))
	needsSequence := false
	for i, arg := range call.Args {
		needsSequence = needsSequence || g.isUnit(arg.Type()) && !unitAtom(arg) || g.paramWrapper(n, i) != nil
	}
	var prelude []goast.Stmt
	for i, arg := range call.Args {
		if g.isUnit(arg.Type()) {
			if needsSequence {
				prelude = append(prelude, g.stmts(arg)...)
			}
			continue
		}
		var value goast.Expr
		if needsSequence {
			name := fmt.Sprintf("t_native%d", g.tmp)
			g.tmp++
			prelude = append(prelude, varDeclStmt(name, g.goType(arg.Type()), g.expr(arg, 0)))
			value = ident(name)
		} else {
			value = g.expr(arg, 0)
		}
		if wrapper := g.paramWrapper(n, i); wrapper != nil {
			value = g.unwrapBoundary(wrapper, value)
		}
		args = append(args, value)
	}
	fn := selector(nativeAlias(n.Module), exportNativeName(types.SurfaceName(n.Name)))
	var result goast.Expr = callExpr(fn, args...)
	if n.Fallible != nil {
		return prelude, g.fallibleNativeResult(call, n, result)
	}
	return prelude, g.wrapBoundaryResult(n, call.Ty, result)
}

func (g *gen) paramWrapper(n *types.NativeInfo, i int) *types.CtorInfo {
	if i < len(n.ParamWrappers) {
		return n.ParamWrappers[i]
	}
	return nil
}

// unwrapBoundary projects a wrapper value to its scalar: `v.(*C_Wrap).F0`.
// A wrapper has exactly one constructor, so the assertion is a projection
// that cannot fail, not a type check.
func (g *gen) unwrapBoundary(wrapper *types.CtorInfo, value goast.Expr) goast.Expr {
	asserted := &goast.TypeAssertExpr{X: value, Type: &goast.StarExpr{X: g.ctorRef(wrapper)}}
	return &goast.SelectorExpr{X: asserted, Sel: ident(fieldName(0))}
}

// wrapBoundaryResult validates a scalar native result and rebuilds a wrapper
// around it when the declaration names one.
func (g *gen) wrapBoundaryResult(n *types.NativeInfo, ty types.Type, result goast.Expr) goast.Expr {
	scalar := ty
	if n.ResultWrapper != nil {
		scalar = n.ResultWrapper.Fields[0]
	}
	result = g.validatedScalar(n.Name, scalar, result)
	if n.ResultWrapper != nil {
		result = g.ctorValue(n.ResultWrapper, nil, result)
	}
	return result
}

func (g *gen) validatedScalar(name string, ty types.Type, result goast.Expr) goast.Expr {
	switch g.unique(ty) {
	case g.b.String.Unique:
		g.usesFangort = true
		result = callExpr(selector("fangort", "RequireValidString"), stringLit(name), result)
	case g.b.Char.Unique:
		g.usesFangort = true
		result = callExpr(selector("fangort", "RequireValidChar"), stringLit(name), result)
	}
	return result
}

// fallibleNativeResult turns a Go `(T, error)` call into `Result IO.Error T`
// as straight-line Go: classify a non-nil error through fangort and build
// `Err (Error {...})`, otherwise build `Ok payload`. No panic, no defer.
func (g *gen) fallibleNativeResult(call *core.NativeCall, n *types.NativeInfo, invoke goast.Expr) goast.Expr {
	g.usesFangort = true
	shape := n.Fallible
	resultArgs := call.Ty.(*types.TCon).Args
	unitPayload := g.isUnit(shape.Payload)
	var body []goast.Stmt
	lhs := []goast.Expr{ident("t_err")}
	if !unitPayload {
		lhs = []goast.Expr{ident("t_payload"), ident("t_err")}
	}
	body = append(body, &goast.AssignStmt{Lhs: lhs, Tok: gotoken.DEFINE, Rhs: []goast.Expr{invoke}})
	kindType := g.goType(shape.Error.Fields[shape.KindIdx])
	kinds := make([]goast.Expr, len(shape.Kinds))
	for i, k := range shape.Kinds {
		kinds[i] = g.ctorValue(k, nil)
	}
	kind := &goast.IndexExpr{
		X:     &goast.CompositeLit{Type: &goast.ArrayType{Elt: kindType}, Elts: kinds},
		Index: selector("t_failure", "Kind"),
	}
	fields := make([]goast.Expr, 3)
	fields[shape.KindIdx] = kind
	fields[shape.PathIdx] = selector("t_failure", "Path")
	fields[shape.MessageIdx] = selector("t_failure", "Message")
	failure := g.ctorValue(shape.Err, resultArgs, g.ctorValue(shape.Error, nil, fields...))
	body = append(body, ifStmt(binExpr(gotoken.NEQ, ident("t_err"), ident("nil")), []goast.Stmt{
		varDeclStmt("t_failure", selector("fangort", "IOFailure"), callExpr(selector("fangort", "ClassifyIOError"), ident("t_err"))),
		returnStmt(failure),
	}, nil))
	var payload goast.Expr
	if unitPayload {
		payload = g.unitValue()
	} else {
		payload = g.wrapBoundaryResult(n, shape.Payload, ident("t_payload"))
	}
	body = append(body, returnStmt(g.ctorValue(shape.Ok, resultArgs, payload)))
	return callExpr(funcLit(g.goType(call.Ty), body))
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
		g.b.Char.Unique: "ShowChar",
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
	case *core.CharLit:
		return &goast.BasicLit{Kind: gotoken.CHAR, Value: strconv.QuoteRune(e.Val)}
	case *core.UnitLit:
		return g.unitValue()
	case *core.BoolLit:
		return ident(strconv.FormatBool(e.Val))
	case *core.Quote:
		// Compile-time-only definitions are never emitted and the Core linter
		// runs before this, so reaching here means both rules were bypassed.
		panic("codegen: quote in emitted code — a compile-time-only value escaped")
	case *core.VarRef:
		// Unit is a singleton and Unit-typed locals are never emitted
		// (their effects ran at binding time) — materialize the value.
		if g.unique(e.Ty) == g.b.Unit.Unique {
			return g.unitValue()
		}
		if !e.Local && g.defs[e.Name] != nil {
			mode := types.Direct
			if g.representationMode() == types.Exit && g.controlledType(e.Ty, nil) {
				mode = types.Exit
			}
			return g.topValueRefMode(e.Name, mode)
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
		if types.FunctionControl(fn).Resolve(g.representationMode()) == types.Machine {
			if g.machineClosures[e] == nil {
				panic("codegen: Machine lambda has no lowered closure")
			}
			return g.machineExpr(e)
		}
		mode := types.FunctionControl(fn).Resolve(g.representationMode())
		oldControl, oldResult := g.control, g.resultType
		g.control, g.resultType = mode, fn.Ret
		params := make([]paramSpec, 0, len(fn.Eff.Labels)+1)
		var pushed []int
		for _, l := range types.SortedRow(fn.Eff).Labels {
			if !types.RuntimeEvidenceEffect(l) {
				continue
			}
			name := g.evidenceName(l.Name)
			inst := core.EffectInstance{Unique: l.Unique, Name: l.Name, Args: l.Args, Control: types.FunctionControl(fn)}
			params = append(params, paramSpec{name: name, typ: g.effectType(inst)})
			g.evidence[l.Unique] = append(g.evidence[l.Unique], ident(name))
			g.evidenceModes[l.Unique] = append(g.evidenceModes[l.Unique], mode)
			pushed = append(pushed, l.Unique)
		}
		params = append(params, paramSpec{name: func() string {
			if e.Param == "_" {
				return "_"
			}
			return mangleValue(e.Param)
		}(), typ: g.goType(fn.Arg)})
		oldParamTy, hadParamTy := g.caseVarTys[e.Param]
		g.caseVarTys[e.Param] = fn.Arg
		body := g.retStmts(e.Body)
		if hadParamTy {
			g.caseVarTys[e.Param] = oldParamTy
		} else {
			delete(g.caseVarTys, e.Param)
		}
		for _, unique := range pushed {
			g.evidence[unique] = g.evidence[unique][:len(g.evidence[unique])-1]
			g.evidenceModes[unique] = g.evidenceModes[unique][:len(g.evidenceModes[unique])-1]
		}
		result := g.goType(fn.Ret)
		if mode == types.Exit {
			result = g.outcomeType(fn.Ret)
		}
		g.control, g.resultType = oldControl, oldResult
		return funcLitParams(params, result, body)
	case *core.App:
		switch e.CalleeKind {
		case core.Worker:
			return g.workerCallExpr(e)
		case core.Value:
			// One typed indirect call per application; chains render
			// e(a)(b). Call is a Go primary expression — no parens needed,
			// and a func-literal callee called in place is legal Go.
			mode := e.Control.Resolve(g.control)
			args := make([]goast.Expr, 0, len(e.EvidenceArgs)+1)
			for _, ev := range e.EvidenceArgs {
				stack := g.evidence[ev.Unique]
				if len(stack) == 0 {
					panic("codegen: missing lexical evidence")
				}
				args = append(args, g.evidenceArg(ev, stack[len(stack)-1], g.currentEvidenceMode(ev.Unique), mode))
			}
			args = append(args, g.callArgExpr(e.Args[0], e.Callee.Type().(*types.TFun).Arg, mode))
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
		if e.Op.Native != nil && len(g.evidence[e.Effect.Unique]) == 0 {
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
		// A perform that resolves to Exit in this context but reaches a
		// Direct handler activation gets a plain result back — a void call
		// for a Unit operation — and must wrap it as a normal Outcome, the
		// same adaptation evidenceArg applies when Direct evidence is passed
		// to an Exit worker.
		exit := e.Control.Resolve(g.control) == types.Exit
		directEvidence := g.currentEvidenceMode(e.Effect.Unique) != types.Exit
		unitResult := g.isUnit(e.Op.ResultType)
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
			switch {
			case exit && directEvidence && unitResult:
				return callExpr(funcLit(g.outcomeType(e.Ty), append(body, exprStmt(call), returnStmt(g.normalOutcome(e.Ty, g.unitValue())))))
			case exit && directEvidence:
				return callExpr(funcLit(g.outcomeType(e.Ty), append(body, returnStmt(g.normalOutcome(e.Ty, call)))))
			case exit:
				return callExpr(funcLit(g.outcomeType(e.Ty), append(body, returnStmt(call))))
			case unitResult:
				return callExpr(funcLit(g.goType(e.Ty), append(body, exprStmt(call), returnStmt(g.unitValue()))))
			}
			return callExpr(funcLit(g.goType(e.Ty), append(body, returnStmt(call))))
		}
		switch {
		case exit && directEvidence && unitResult:
			return callExpr(funcLit(g.outcomeType(e.Ty), []goast.Stmt{exprStmt(call), returnStmt(g.normalOutcome(e.Ty, g.unitValue()))}))
		case exit && directEvidence:
			return g.normalOutcome(e.Ty, call)
		case exit:
			return call
		case unitResult:
			return callExpr(funcLit(g.goType(e.Ty), []goast.Stmt{exprStmt(call), returnStmt(g.unitValue())}))
		}
		return call
	case *core.ControlExit:
		g.usesFangort = true
		stack := g.evidence[e.Effect.Unique]
		if len(stack) == 0 {
			panic("codegen: ControlExit without lexical evidence")
		}
		payload := make([]goast.Expr, len(e.Payload))
		for i, p := range e.Payload {
			// Interface payloads otherwise default untyped literals (notably Int)
			// to Go's `int`, while handler frames consistently expect the Fango
			// representation selected by goType.
			payload[i] = callExpr(g.goType(p.Type()), g.expr(p, 0))
		}
		exit := &goast.UnaryExpr{Op: gotoken.AND, X: &goast.CompositeLit{Type: selector("fangort", "ExitRequest"), Elts: []goast.Expr{
			&goast.KeyValueExpr{Key: ident("Target"), Value: &goast.SelectorExpr{X: stack[len(stack)-1], Sel: ident("Target")}},
			&goast.KeyValueExpr{Key: ident("Effect"), Value: stringLit(e.Op.Owner.Name)},
			&goast.KeyValueExpr{Key: ident("Operation"), Value: intLit(int64(e.Op.Index))},
			&goast.KeyValueExpr{Key: ident("Payload"), Value: &goast.CompositeLit{Type: &goast.ArrayType{Elt: ident("any")}, Elts: payload}},
		}}}
		return g.propagateOutcome(e.Ty, exit)
	case *core.ResumeTail:
		panic("codegen: ResumeTail outside verified handler-clause emission")
	case *core.Seq:
		return callExpr(funcLit(g.goType(e.Ty), append(g.stmts(e.First), returnStmt(g.expr(e.Then, 0)))))
	case *core.Handle:
		return g.handleExpr(e)
	case *core.Bracket:
		return g.bracketExpr(e)
	case *core.IteratorScope:
		return g.iteratorScopeExpr(e)
	case *core.IteratorForEach:
		return g.iteratorForEachExpr(e)
	case *core.IteratorFold:
		return g.iteratorFoldExpr(e)
	default:
		panic(fmt.Sprintf("codegen: node %T arrives in a later slice", e))
	}
}

func (g *gen) workerCallExpr(e *core.App) goast.Expr {
	ref := e.Callee.(*core.VarRef)
	formal, voidResult := g.workerABI(ref.Name)
	mode := e.Control.Resolve(g.control)
	// A source-Direct call can still construct an Exit-family function value.
	// A polymorphic worker has a joined execution/representation ABI, so use
	// its Exit member and project its statically normal result. Supplied Direct
	// evidence is widened below; no Exit is discarded by this projection.
	normalProjection := g.workerNeedsNormalProjection(e)
	if normalProjection {
		mode = types.Exit
		g.usesFangort = true
	}
	abi := g.workerCallABI(g.defs[ref.Name], mode)
	args := make([]goast.Expr, 0, len(e.EvidenceArgs)+len(e.Args))
	for _, ev := range e.EvidenceArgs {
		stack := g.evidence[ev.Unique]
		if len(stack) == 0 {
			panic("codegen: missing lexical evidence")
		}
		args = append(args, g.evidenceArg(ev, stack[len(stack)-1], g.currentEvidenceMode(ev.Unique), mode))
	}
	needPrelude := false
	for i, a := range e.Args {
		if i < len(formal) && g.isUnit(formal[i]) {
			if !unitAtom(a) {
				needPrelude = true
			}
			continue
		}
		args = append(args, g.callArgExpr(a, formal[i], abi))
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
		call := callExpr(indexExpr(g.topValueRefMode(ref.Name, abi), g.goTypes(e.TyArgs)), args...)
		if mode == types.Exit {
			body = append(body, returnStmt(call))
		} else if voidResult {
			body = append(body, exprStmt(call), returnStmt(g.unitValue()))
		} else {
			body = append(body, returnStmt(call))
		}
		result := g.goType(e.Ty)
		if mode == types.Exit {
			result = g.outcomeType(e.Ty)
		}
		wrapped := callExpr(funcLit(result, body))
		if normalProjection {
			return callExpr(selector("fangort", "RequireNormal"), wrapped)
		}
		return wrapped
	}
	call := callExpr(indexExpr(g.topValueRefMode(ref.Name, abi), g.goTypes(e.TyArgs)), args...)
	if normalProjection {
		return callExpr(selector("fangort", "RequireNormal"), call)
	}
	if mode == types.Exit {
		return call
	}
	if voidResult {
		return callExpr(funcLit(g.goType(e.Ty), []goast.Stmt{exprStmt(call), returnStmt(g.unitValue())}))
	}
	return call
}

func (g *gen) workerNeedsNormalProjection(e *core.App) bool {
	d := g.defs[e.Callee.(*core.VarRef).Name]
	return d != nil && d.Control.Polymorphic &&
		e.Control.Resolve(g.control) == types.Direct && g.representationMode() == types.Exit
}

// callArgExpr widens a direct callback to the Exit representation family when
// an enclosing call is being emitted in Exit control.  The source type of a
// callback can be direct (for example, an IO-only cleanup), while the
// higher-order function receiving it must use an Outcome-returning callback so
// that exits from callbacks can propagate through its body.
func (g *gen) callArgExpr(arg core.Expr, formal types.Type, mode types.Transport) goast.Expr {
	fn, ok := formal.(*types.TFun)
	if !ok || mode != types.Exit || types.FunctionControl(fn).Resolve(mode) != types.Exit {
		return g.expr(arg, 0)
	}
	actual, ok := arg.Type().(*types.TFun)
	if !ok || types.FunctionControl(actual).Resolve(mode) == types.Exit {
		return g.expr(arg, 0)
	}

	direct := g.expr(arg, 0)
	params := make([]paramSpec, 0, len(actual.Eff.Labels)+1)
	var callArgs []goast.Expr
	for _, label := range types.SortedRow(actual.Eff).Labels {
		if !types.RuntimeEvidenceEffect(label) {
			continue
		}
		name := g.evidenceName(label.Name)
		params = append(params, paramSpec{name: name, typ: g.effectTypeMode(core.EffectInstance{Unique: label.Unique, Name: label.Name, Args: label.Args, Control: types.FunctionControl(actual)}, types.Direct)})
		callArgs = append(callArgs, ident(name))
	}
	paramName := "v_arg"
	params = append(params, paramSpec{name: paramName, typ: g.goType(actual.Arg)})
	callArgs = append(callArgs, ident(paramName))
	body := []goast.Stmt{returnStmt(g.normalOutcome(actual.Ret, callExpr(direct, callArgs...)))}
	return funcLitParams(params, g.outcomeType(actual.Ret), body)
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
	if len(e.Clauses) > 0 && e.Clauses[0].Op.Abort {
		return g.abortHandleExpr(e)
	}
	evidenceMode := e.Effect.Control.Resolve(g.control)
	stateCell := ""
	if e.State != nil {
		stateCell = fmt.Sprintf("t_state%d", g.tmp)
		g.tmp++
	}
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
		if evidenceMode == types.Exit {
			results = &goast.FieldList{List: []*goast.Field{{Type: g.outcomeType(c.ResultType)}}}
		} else if !g.isUnit(c.Op.ResultType) {
			results = &goast.FieldList{List: []*goast.Field{{Type: g.goType(c.ResultType)}}}
		}
		ft := &goast.FuncType{Params: paramFields(params), Results: results}
		fields[i] = &goast.Field{Names: []*goast.Ident{ident("Op_" + linkName(c.Op.Name))}, Type: ft}
		var clausePrefix []goast.Stmt
		if e.State != nil {
			clausePrefix = append(clausePrefix,
				varDeclStmt(mangleValue(e.State.Name), g.goType(e.State.Ty), ident(stateCell)),
				assignBlank(ident(mangleValue(e.State.Name))))
		}
		oldControl, oldResult := g.control, g.resultType
		g.control, g.resultType = evidenceMode, c.ResultType
		clauseBody := g.resumeStmtsFor(c.Body, c.ResumeID, g.isUnit(c.Op.ResultType), stateCell, func() types.Type {
			if e.State != nil {
				return e.State.Ty
			}
			return nil
		}())
		g.control, g.resultType = oldControl, oldResult
		fn := &goast.FuncLit{Type: ft, Body: &goast.BlockStmt{List: append(clausePrefix, clauseBody...)}}
		elts[i] = &goast.KeyValueExpr{Key: ident("Op_" + linkName(c.Op.Name)), Value: fn}
	}
	_ = fields
	st := g.effectType(e.Effect)
	name := fmt.Sprintf("ev%d", g.tmp)
	g.tmp++
	decl := varDeclStmt(name, st, &goast.CompositeLit{Type: st, Elts: elts})
	g.evidence[e.Effect.Unique] = append(g.evidence[e.Effect.Unique], ident(name))
	g.evidenceModes[e.Effect.Unique] = append(g.evidenceModes[e.Effect.Unique], evidenceMode)
	body := g.expr(e.Body, 0)
	g.evidence[e.Effect.Unique] = g.evidence[e.Effect.Unique][:len(g.evidence[e.Effect.Unique])-1]
	g.evidenceModes[e.Effect.Unique] = g.evidenceModes[e.Effect.Unique][:len(g.evidenceModes[e.Effect.Unique])-1]
	// A handler whose subject does not perform the handled effect still
	// constructs valid lexical evidence; keep the local legal in Go even
	// when no generated operation call refers to it.
	var stmts []goast.Stmt
	if e.State != nil {
		stmts = append(stmts, varDeclStmt(stateCell, g.goType(e.State.Ty), g.expr(e.State.Initial, 0)))
	}
	stmts = append(stmts, decl, assignBlank(ident(name)))
	if e.Return == nil {
		stmts = append(stmts, returnStmt(body))
		result := g.goType(e.Ty)
		if core.ExprControl(e.Body).Resolve(g.control) == types.Exit {
			result = g.outcomeType(e.Ty)
		}
		return callExpr(funcLit(result, stmts))
	}
	p := e.Return.Param
	if g.control == types.Exit && core.ExprControl(e.Body).Resolve(g.control) == types.Exit {
		outcome := fmt.Sprintf("t_handle%d", g.tmp)
		g.tmp++
		stmts = append(stmts,
			varDeclStmt(outcome, g.outcomeType(e.Body.Type()), body),
			&goast.IfStmt{Cond: &goast.BinaryExpr{X: selector(outcome, "Exit"), Op: gotoken.NEQ, Y: ident("nil")}, Body: &goast.BlockStmt{List: []goast.Stmt{returnStmt(g.propagateOutcome(e.Ty, selector(outcome, "Exit")))}}})
		if p != "_" && p != "()" {
			stmts = append(stmts, varDeclStmt(mangleValue(p), g.goType(e.Body.Type()), selector(outcome, "Value")))
			stmts = append(stmts, g.keepUnused(e.Return.Body, p, e.Body.Type())...)
		}
	} else if p == "_" || p == "()" {
		stmts = append(stmts, assignBlank(body))
	} else {
		stmts = append(stmts, varDeclStmt(mangleValue(p), g.goType(e.Body.Type()), body))
		stmts = append(stmts, g.keepUnused(e.Return.Body, p, e.Body.Type())...)
	}
	if e.State != nil {
		stmts = append(stmts, varDeclStmt(mangleValue(e.State.Name), g.goType(e.State.Ty), ident(stateCell)))
		stmts = append(stmts, g.keepUnused(e.Return.Body, e.State.Name, e.State.Ty)...)
	}
	// The closure declares fangort.Unit, so a Unit body returns the
	// singleton; only a worker whose Go signature erases the result may
	// return bare.
	if g.control != types.Exit && g.isUnit(e.Ty) {
		stmts = append(stmts, g.unitValueRetStmts(e.Return.Body)...)
	} else {
		stmts = append(stmts, g.retStmtsFor(e.Return.Body, g.isUnit(e.Ty))...)
	}
	result := g.goType(e.Ty)
	if g.control == types.Exit {
		result = g.outcomeType(e.Ty)
	}
	return callExpr(funcLit(result, stmts))
}

// bracketExpr lowers a cleanup scope to straight-line Go. Acquire runs once;
// release runs after the body on every path that acquired, including one
// where the body is carrying an exit aimed at an outer handler. Nothing here
// captures a continuation or tests a consumed-state flag: a scope is a
// sequence of ordinary calls plus the Outcome tests the Exit ABI already
// uses. Go `defer` is deliberately not used — the release must be ordered
// against the body result, not against this function literal returning.
func (g *gen) bracketExpr(e *core.Bracket) goast.Expr {
	overall := e.Control.Resolve(g.control)
	oldControl, oldResult := g.control, g.resultType
	// Each slot is emitted as a value of its own type, so a slot that is
	// itself a statement shape propagates at its own result type.
	child := func(x core.Expr) (goast.Expr, bool) {
		g.control, g.resultType = overall, x.Type()
		out := g.expr(x, 0)
		g.control, g.resultType = oldControl, oldResult
		return out, overall == types.Exit && core.ExprControl(x).Resolve(overall) == types.Exit
	}
	name := func(kind string) string {
		n := fmt.Sprintf("t_scope%s%d", kind, g.tmp)
		g.tmp++
		return n
	}

	resource := mangleValue(e.Resource)
	var stmts []goast.Stmt

	acquire, acquireExits := child(e.Acquire)
	if acquireExits {
		acquired := name("Acquired")
		stmts = append(stmts,
			varDeclStmt(acquired, g.outcomeType(e.ResourceTy), acquire),
			&goast.IfStmt{
				Cond: &goast.BinaryExpr{X: selector(acquired, "Exit"), Op: gotoken.NEQ, Y: ident("nil")},
				// Acquisition failed, so there is no resource to release.
				Body: &goast.BlockStmt{List: []goast.Stmt{returnStmt(g.propagateOutcome(e.Ty, selector(acquired, "Exit")))}},
			})
		acquire = selector(acquired, "Value")
	}
	stmts = append(stmts, varDeclStmt(resource, g.goType(e.ResourceTy), acquire))
	if !core.Mentions(e.Body, e.Resource) && !core.Mentions(e.Release, e.Resource) {
		stmts = append(stmts, assignBlank(ident(resource)))
	}

	bodyName := name("Body")
	body, bodyExits := child(e.Body)
	bodyType := g.goType(e.Ty)
	if bodyExits {
		bodyType = g.outcomeType(e.Ty)
	}
	stmts = append(stmts, varDeclStmt(bodyName, bodyType, body))

	release, releaseExits := child(e.Release)
	releaseName := ""
	if releaseExits {
		releaseName = name("Release")
		stmts = append(stmts, varDeclStmt(releaseName, g.outcomeType(e.Release.Type()), release))
	} else {
		stmts = append(stmts, assignBlank(release))
	}

	result := g.goType(e.Ty)
	if overall == types.Exit {
		result = g.outcomeType(e.Ty)
		bodyExit := selector(bodyName, "Exit")
		if bodyExits {
			primary := []goast.Stmt{returnStmt(g.propagateOutcome(e.Ty, bodyExit))}
			if releaseExits {
				// The body failure stays primary; the release failure is recorded
				// rather than dropped.
				g.usesFangort = true
				joined := callExpr(selector("fangort", "Suppress"), bodyExit, selector(releaseName, "Exit"))
				primary = append([]goast.Stmt{&goast.IfStmt{
					Cond: &goast.BinaryExpr{X: selector(releaseName, "Exit"), Op: gotoken.NEQ, Y: ident("nil")},
					Body: &goast.BlockStmt{List: []goast.Stmt{returnStmt(g.propagateOutcome(e.Ty, joined))}},
				}}, primary...)
			}
			stmts = append(stmts, &goast.IfStmt{
				Cond: &goast.BinaryExpr{X: bodyExit, Op: gotoken.NEQ, Y: ident("nil")},
				Body: &goast.BlockStmt{List: primary},
			})
		}
		if releaseExits {
			// The body completed, so a failed release is the only failure.
			stmts = append(stmts, &goast.IfStmt{
				Cond: &goast.BinaryExpr{X: selector(releaseName, "Exit"), Op: gotoken.NEQ, Y: ident("nil")},
				Body: &goast.BlockStmt{List: []goast.Stmt{returnStmt(g.propagateOutcome(e.Ty, selector(releaseName, "Exit")))}},
			})
		}
		if bodyExits {
			stmts = append(stmts, returnStmt(ident(bodyName)))
		} else {
			stmts = append(stmts, returnStmt(g.normalOutcome(e.Ty, ident(bodyName))))
		}
	} else {
		stmts = append(stmts, returnStmt(ident(bodyName)))
	}
	return callExpr(funcLit(result, stmts))
}

// iteratorScopeExpr owns one nested producer machine for the dynamic extent of
// its consumer callback. The consumer-facing cursor is the runtime owner
// itself; source code can only pass it to checked terminal combinators.
func (g *gen) iteratorScopeExpr(e *core.IteratorScope) goast.Expr {
	overall := e.Control.Resolve(g.control)
	oldControl, oldResult := g.control, g.resultType
	g.control, g.resultType = overall, e.Ty

	name := func(kind string) string {
		n := fmt.Sprintf("t_iterator%s%d", kind, g.tmp)
		g.tmp++
		return n
	}
	iterator := name("Owner")
	consumerResult := name("Result")
	closeExit := name("CloseExit")

	producerFrame := callExpr(g.machineExpr(e.Producer), g.unitValue())
	start := callExpr(selector("fangort", "StartMachineIterator"), producerFrame)
	consume := callExpr(g.expr(e.Consumer, 0), ident(iterator))
	resultType := g.goType(e.Ty)
	consumerExits := overall == types.Exit
	if consumerExits {
		resultType = g.outcomeType(e.Ty)
	}
	stmts := []goast.Stmt{
		varDeclStmt(iterator, &goast.StarExpr{X: selector("fangort", "MachineIterator")}, start),
		varDeclStmt(consumerResult, resultType, consume),
		varDeclStmt(closeExit, &goast.StarExpr{X: selector("fangort", "ExitRequest")},
			callExpr(selector("fangort", "CloseMachineIterator"), ident(iterator))),
	}

	var iifeResult goast.Expr = g.goType(e.Ty)
	if consumerExits {
		g.usesFangort = true
		consumerExit := selector(consumerResult, "Exit")
		joined := callExpr(selector("fangort", "Suppress"), consumerExit, ident(closeExit))
		stmts = append(stmts,
			&goast.IfStmt{Cond: &goast.BinaryExpr{X: consumerExit, Op: gotoken.NEQ, Y: ident("nil")},
				Body: &goast.BlockStmt{List: []goast.Stmt{returnStmt(g.propagateOutcome(e.Ty, joined))}}},
			&goast.IfStmt{Cond: &goast.BinaryExpr{X: ident(closeExit), Op: gotoken.NEQ, Y: ident("nil")},
				Body: &goast.BlockStmt{List: []goast.Stmt{returnStmt(g.propagateOutcome(e.Ty, ident(closeExit)))}}},
			returnStmt(g.normalOutcome(e.Ty, selector(consumerResult, "Value"))))
		iifeResult = g.outcomeType(e.Ty)
	} else {
		stmts = append(stmts,
			exprStmt(callExpr(selector("fangort", "AssertNoMachineExit"), ident(closeExit))),
			returnStmt(ident(consumerResult)))
	}

	g.control, g.resultType = oldControl, oldResult
	return callExpr(funcLit(iifeResult, stmts))
}

func (g *gen) iteratorForEachExpr(e *core.IteratorForEach) goast.Expr {
	overall := e.Control.Resolve(g.control)
	oldControl, oldResult := g.control, g.resultType
	g.control, g.resultType = overall, e.Ty
	g.usesFangort = true

	name := func(kind string) string {
		n := fmt.Sprintf("t_iterator%s%d", kind, g.tmp)
		g.tmp++
		return n
	}
	action := name("Action")
	cursor := name("Cursor")
	value := name("Value")
	yielded := name("Yielded")
	exit := name("Exit")
	item := name("Item")

	stmts := []goast.Stmt{
		varDeclStmt(action, g.goType(e.Action.Type()), g.expr(e.Action, 0)),
		varDeclStmt(cursor, g.goType(e.Cursor.Type()), g.expr(e.Cursor, 0)),
	}
	next := &goast.AssignStmt{
		Lhs: []goast.Expr{ident(value), ident(yielded), ident(exit)}, Tok: gotoken.DEFINE,
		Rhs: []goast.Expr{callExpr(selector("fangort", "PullMachineIterator"), ident(cursor))},
	}
	loop := []goast.Stmt{next}
	if overall == types.Exit {
		loop = append(loop, &goast.IfStmt{Cond: &goast.BinaryExpr{X: ident(exit), Op: gotoken.NEQ, Y: ident("nil")},
			Body: &goast.BlockStmt{List: []goast.Stmt{returnStmt(g.propagateOutcome(e.Ty, ident(exit)))}}})
	} else {
		loop = append(loop, exprStmt(callExpr(selector("fangort", "AssertNoMachineExit"), ident(exit))))
	}
	doneValue := g.unitValue()
	if overall == types.Exit {
		doneValue = g.normalOutcome(e.Ty, doneValue)
	}
	loop = append(loop,
		&goast.IfStmt{Cond: &goast.UnaryExpr{Op: gotoken.NOT, X: ident(yielded)},
			Body: &goast.BlockStmt{List: []goast.Stmt{returnStmt(doneValue)}}},
		varDeclStmt(item, g.goType(e.Element), &goast.TypeAssertExpr{X: ident(value), Type: g.goType(e.Element)}),
	)
	call := callExpr(ident(action), ident(item))
	if overall == types.Exit {
		outcome := name("ActionResult")
		loop = append(loop,
			varDeclStmt(outcome, g.outcomeType(e.Ty), call),
			&goast.IfStmt{Cond: &goast.BinaryExpr{X: selector(outcome, "Exit"), Op: gotoken.NEQ, Y: ident("nil")},
				Body: &goast.BlockStmt{List: []goast.Stmt{returnStmt(g.propagateOutcome(e.Ty, selector(outcome, "Exit")))}}})
	} else {
		loop = append(loop, exprStmt(call))
	}
	stmts = append(stmts, &goast.ForStmt{Body: &goast.BlockStmt{List: loop}})

	result := g.goType(e.Ty)
	if overall == types.Exit {
		result = g.outcomeType(e.Ty)
	}
	g.control, g.resultType = oldControl, oldResult
	return callExpr(funcLit(result, stmts))
}

func (g *gen) iteratorFoldExpr(e *core.IteratorFold) goast.Expr {
	overall := e.Control.Resolve(g.control)
	oldControl, oldResult := g.control, g.resultType
	g.control, g.resultType = overall, e.Ty
	g.usesFangort = true

	name := func(kind string) string {
		n := fmt.Sprintf("t_iterator%s%d", kind, g.tmp)
		g.tmp++
		return n
	}
	combine := name("Combine")
	accumulator := name("Accumulator")
	cursor := name("Cursor")
	value := name("Value")
	yielded := name("Yielded")
	exit := name("Exit")
	item := name("Item")

	stmts := []goast.Stmt{
		varDeclStmt(combine, g.goType(e.Combine.Type()), g.expr(e.Combine, 0)),
		varDeclStmt(accumulator, g.goType(e.Accumulator), g.expr(e.Initial, 0)),
		varDeclStmt(cursor, g.goType(e.Cursor.Type()), g.expr(e.Cursor, 0)),
	}
	loop := []goast.Stmt{
		&goast.AssignStmt{
			Lhs: []goast.Expr{ident(value), ident(yielded), ident(exit)}, Tok: gotoken.DEFINE,
			Rhs: []goast.Expr{callExpr(selector("fangort", "PullMachineIterator"), ident(cursor))},
		},
	}
	if overall == types.Exit {
		loop = append(loop, &goast.IfStmt{Cond: &goast.BinaryExpr{X: ident(exit), Op: gotoken.NEQ, Y: ident("nil")},
			Body: &goast.BlockStmt{List: []goast.Stmt{returnStmt(g.propagateOutcome(e.Ty, ident(exit)))}}})
	} else {
		loop = append(loop, exprStmt(callExpr(selector("fangort", "AssertNoMachineExit"), ident(exit))))
	}
	doneValue := goast.Expr(ident(accumulator))
	if overall == types.Exit {
		doneValue = g.normalOutcome(e.Ty, doneValue)
	}
	loop = append(loop,
		&goast.IfStmt{Cond: &goast.UnaryExpr{Op: gotoken.NOT, X: ident(yielded)},
			Body: &goast.BlockStmt{List: []goast.Stmt{returnStmt(doneValue)}}},
		varDeclStmt(item, g.goType(e.Element), &goast.TypeAssertExpr{X: ident(value), Type: g.goType(e.Element)}),
	)
	call := callExpr(callExpr(ident(combine), ident(item)), ident(accumulator))
	if overall == types.Exit {
		outcome := name("CombineResult")
		loop = append(loop,
			varDeclStmt(outcome, g.outcomeType(e.Accumulator), call),
			&goast.IfStmt{Cond: &goast.BinaryExpr{X: selector(outcome, "Exit"), Op: gotoken.NEQ, Y: ident("nil")},
				Body: &goast.BlockStmt{List: []goast.Stmt{returnStmt(g.propagateOutcome(e.Ty, selector(outcome, "Exit")))}}},
			assignStmt(accumulator, selector(outcome, "Value")))
	} else {
		loop = append(loop, assignStmt(accumulator, call))
	}
	stmts = append(stmts, &goast.ForStmt{Body: &goast.BlockStmt{List: loop}})

	result := g.goType(e.Ty)
	if overall == types.Exit {
		result = g.outcomeType(e.Ty)
	}
	g.control, g.resultType = oldControl, oldResult
	return callExpr(funcLit(result, stmts))
}

// abortHandleExpr installs only a unique target token. Performing an abort
// constructs an ExitRequest; the clause is invoked here, after the handled
// body has unwound and the handler's evidence has been removed.
func (g *gen) abortHandleExpr(e *core.Handle) goast.Expr {
	g.usesFangort = true
	overall := e.Control.Resolve(g.control)
	targetName := fmt.Sprintf("t_target%d", g.tmp)
	g.tmp++
	evidenceName := fmt.Sprintf("ev%d", g.tmp)
	g.tmp++
	outcomeName := fmt.Sprintf("t_handle%d", g.tmp)
	g.tmp++
	stateCell := ""
	if e.State != nil {
		stateCell = fmt.Sprintf("t_state%d", g.tmp)
		g.tmp++
	}

	var stmts []goast.Stmt
	if e.State != nil {
		stmts = append(stmts, varDeclStmt(stateCell, g.goType(e.State.Ty), g.expr(e.State.Initial, 0)))
	}
	target := &goast.UnaryExpr{Op: gotoken.AND, X: &goast.CompositeLit{Type: selector("fangort", "ExitTarget"), Elts: []goast.Expr{
		&goast.KeyValueExpr{Key: ident("Marker"), Value: intLit(1)},
	}}}
	stmts = append(stmts, varDeclStmt(targetName, &goast.StarExpr{X: selector("fangort", "ExitTarget")}, target))
	st := g.effectType(e.Effect)
	evidenceValue := &goast.CompositeLit{Type: st, Elts: []goast.Expr{
		&goast.KeyValueExpr{Key: ident("Target"), Value: ident(targetName)},
	}}
	stmts = append(stmts, varDeclStmt(evidenceName, st, evidenceValue), assignBlank(ident(evidenceName)))

	g.evidence[e.Effect.Unique] = append(g.evidence[e.Effect.Unique], ident(evidenceName))
	g.evidenceModes[e.Effect.Unique] = append(g.evidenceModes[e.Effect.Unique], types.Exit)
	oldControl, oldResult := g.control, g.resultType
	g.control, g.resultType = types.Exit, e.Body.Type()
	body := callExpr(funcLit(g.outcomeType(e.Body.Type()), g.retStmtsFor(e.Body, g.isUnit(e.Body.Type()))))
	g.control, g.resultType = oldControl, oldResult
	g.evidence[e.Effect.Unique] = g.evidence[e.Effect.Unique][:len(g.evidence[e.Effect.Unique])-1]
	g.evidenceModes[e.Effect.Unique] = g.evidenceModes[e.Effect.Unique][:len(g.evidenceModes[e.Effect.Unique])-1]
	stmts = append(stmts, varDeclStmt(outcomeName, g.outcomeType(e.Body.Type()), body))

	exit := &goast.SelectorExpr{X: ident(outcomeName), Sel: ident("Exit")}
	foreignBody := []goast.Stmt{returnStmt(g.propagateOutcome(e.Ty, exit))}
	if overall == types.Direct {
		foreignBody = g.zeroReturn(e.Ty)
	}
	targetMismatch := &goast.BinaryExpr{X: &goast.SelectorExpr{X: exit, Sel: ident("Target")}, Op: gotoken.NEQ, Y: ident(targetName)}
	hasExitBody := []goast.Stmt{&goast.IfStmt{Cond: targetMismatch, Body: &goast.BlockStmt{List: foreignBody}}}
	for _, clause := range e.Clauses {
		cond := &goast.BinaryExpr{X: &goast.SelectorExpr{X: exit, Sel: ident("Operation")}, Op: gotoken.EQL, Y: intLit(int64(clause.Op.Index))}
		var clauseStmts []goast.Stmt
		if e.State != nil {
			clauseStmts = append(clauseStmts, varDeclStmt(mangleValue(e.State.Name), g.goType(e.State.Ty), ident(stateCell)), assignBlank(ident(mangleValue(e.State.Name))))
		}
		for i, p := range clause.Params {
			if p == "_" || p == "()" {
				continue
			}
			payload := &goast.IndexExpr{X: &goast.SelectorExpr{X: exit, Sel: ident("Payload")}, Index: intLit(int64(i))}
			value := &goast.TypeAssertExpr{X: payload, Type: g.goType(clause.ParamTypes[i])}
			clauseStmts = append(clauseStmts, varDeclStmt(mangleValue(p), g.goType(clause.ParamTypes[i]), value))
			if g.isUnit(clause.ParamTypes[i]) || !core.Mentions(clause.Body, p) {
				clauseStmts = append(clauseStmts, assignBlank(ident(mangleValue(p))))
			}
		}
		g.control, g.resultType = overall, e.Ty
		if overall == types.Direct && g.isUnit(e.Ty) {
			clauseStmts = append(clauseStmts, g.unitValueRetStmts(clause.Body)...)
		} else {
			clauseStmts = append(clauseStmts, g.retStmtsFor(clause.Body, g.isUnit(e.Ty))...)
		}
		g.control, g.resultType = oldControl, oldResult
		hasExitBody = append(hasExitBody, &goast.IfStmt{Cond: cond, Body: &goast.BlockStmt{List: clauseStmts}})
	}
	if overall == types.Exit {
		hasExitBody = append(hasExitBody, returnStmt(g.propagateOutcome(e.Ty, exit)))
	} else {
		hasExitBody = append(hasExitBody, g.zeroReturn(e.Ty)...)
	}
	stmts = append(stmts, &goast.IfStmt{Cond: &goast.BinaryExpr{X: exit, Op: gotoken.NEQ, Y: ident("nil")}, Body: &goast.BlockStmt{List: hasExitBody}})

	if e.Return == nil {
		value := &goast.SelectorExpr{X: ident(outcomeName), Sel: ident("Value")}
		if overall == types.Exit {
			stmts = append(stmts, returnStmt(g.normalOutcome(e.Ty, value)))
		} else {
			stmts = append(stmts, returnStmt(value))
		}
	} else {
		if p := e.Return.Param; p != "_" && p != "()" {
			stmts = append(stmts, varDeclStmt(mangleValue(p), g.goType(e.Body.Type()), &goast.SelectorExpr{X: ident(outcomeName), Sel: ident("Value")}))
			stmts = append(stmts, g.keepUnused(e.Return.Body, p, e.Body.Type())...)
		}
		if e.State != nil {
			stmts = append(stmts, varDeclStmt(mangleValue(e.State.Name), g.goType(e.State.Ty), ident(stateCell)))
			stmts = append(stmts, g.keepUnused(e.Return.Body, e.State.Name, e.State.Ty)...)
		}
		g.control, g.resultType = overall, e.Ty
		if overall == types.Direct && g.isUnit(e.Ty) {
			stmts = append(stmts, g.unitValueRetStmts(e.Return.Body)...)
		} else {
			stmts = append(stmts, g.retStmtsFor(e.Return.Body, g.isUnit(e.Ty))...)
		}
		g.control, g.resultType = oldControl, oldResult
	}
	result := g.goType(e.Ty)
	if overall == types.Exit {
		result = g.outcomeType(e.Ty)
	}
	return callExpr(funcLit(result, stmts))
}

// zeroReturn closes generated branches that Core proves unreachable: a
// foreign target in a Direct handler or an unknown operation for a matching
// private target. Keeping this path data-only preserves the no-panic control
// runtime invariant.
func (g *gen) zeroReturn(t types.Type) []goast.Stmt {
	name := fmt.Sprintf("t_unreachable%d", g.tmp)
	g.tmp++
	return []goast.Stmt{varDeclNoValue(name, g.goType(t)), returnStmt(ident(name))}
}

// resumeStmts lowers a proven tail-resumptive clause. A tail `resume v`
// becomes a direct return of v from the evidence operation field; the
// caller's ordinary Go continuation then proceeds with that operation
// result. No continuation object or non-local control transfer is needed.
func (g *gen) resumeStmtsFor(e core.Expr, owner types.ResumeID, unitResult bool, stateCell string, stateType types.Type) []goast.Stmt {
	switch e := e.(type) {
	case *core.ControlExit:
		if g.control != types.Exit {
			panic("codegen: abort terminal in a Direct resumptive clause")
		}
		return []goast.Stmt{returnStmt(g.expr(e, 0))}
	case *core.ResumeTail:
		if e.Owner != owner {
			panic("codegen: ResumeTail owner does not match handler clause")
		}
		if e.NextState != nil {
			resultName := fmt.Sprintf("t_resume%d", g.tmp)
			g.tmp++
			nextName := fmt.Sprintf("t_next%d", g.tmp)
			g.tmp++
			var stmts []goast.Stmt
			if unitResult {
				stmts = append(stmts, g.stmts(e.Value)...)
			} else {
				stmts = append(stmts, varDeclStmt(resultName, g.goType(e.Value.Type()), g.expr(e.Value, 0)))
			}
			stmts = append(stmts,
				varDeclStmt(nextName, g.goType(stateType), g.expr(e.NextState, 0)),
				assignStmt(stateCell, ident(nextName)))
			if unitResult {
				if g.control == types.Exit {
					return append(stmts, returnStmt(g.normalOutcome(g.resultType, g.unitValue())))
				}
				return append(stmts, bareReturnStmt())
			}
			if g.control == types.Exit {
				return append(stmts, returnStmt(g.normalOutcome(g.resultType, ident(resultName))))
			}
			return append(stmts, returnStmt(ident(resultName)))
		}
		if unitResult {
			stmts := g.stmts(e.Value)
			if g.control == types.Exit {
				return append(stmts, returnStmt(g.normalOutcome(g.resultType, g.unitValue())))
			}
			return append(stmts, bareReturnStmt())
		}
		if g.control == types.Exit {
			return []goast.Stmt{returnStmt(g.normalOutcome(g.resultType, g.expr(e.Value, 0)))}
		}
		return []goast.Stmt{returnStmt(g.expr(e.Value, 0))}
	case *core.Let:
		if _, exits := e.Rhs.(*core.ControlExit); exits {
			outcome := fmt.Sprintf("t_terminal%d", g.tmp)
			g.tmp++
			return []goast.Stmt{
				varDeclStmt(outcome, g.outcomeType(e.Rhs.Type()), g.expr(e.Rhs, 0)),
				returnStmt(g.propagateOutcome(g.resultType, selector(outcome, "Exit"))),
			}
		}
		return append(g.letBindingStmts(e), g.resumeStmtsFor(e.Body, owner, unitResult, stateCell, stateType)...)
	case *core.Seq:
		return append(g.stmts(e.First), g.resumeStmtsFor(e.Then, owner, unitResult, stateCell, stateType)...)
	case *core.If:
		return []goast.Stmt{ifStmt(g.expr(e.Cond, 0), g.resumeStmtsFor(e.Then, owner, unitResult, stateCell, stateType), g.resumeStmtsFor(e.Else, owner, unitResult, stateCell, stateType))}
	case *core.Case:
		return g.caseStmts(e, func(x core.Expr) []goast.Stmt { return g.resumeStmtsFor(x, owner, unitResult, stateCell, stateType) })
	default:
		panic(fmt.Sprintf("codegen: non-tail-resumptive clause node %T", e))
	}
}

func (g *gen) effectType(e core.EffectInstance) goast.Expr {
	return g.effectTypeMode(e, g.control)
}

func (g *gen) effectTypeMode(e core.EffectInstance, mode types.Transport) goast.Expr {
	name := "Eff_" + linkName(e.Name)
	switch e.Control.Resolve(mode) {
	case types.Exit:
		name += "_exit"
	case types.Machine:
		name += "_machine"
	}
	return indexExpr(g.qualified(symbolOwner(e.Name), name), g.goTypes(e.Args))
}

// evidenceArg widens a Direct evidence record to its Exit ABI family when an
// enclosing aborting call needs Outcome-returning operation callbacks. The
// reverse conversion is intentionally absent.
func (g *gen) currentEvidenceMode(unique int) types.Transport {
	stack := g.evidenceModes[unique]
	if len(stack) == 0 {
		return types.Direct
	}
	return stack[len(stack)-1]
}

func (g *gen) evidenceArg(ev core.EffectInstance, value goast.Expr, actual, want types.Transport) goast.Expr {
	if actual == want || want == types.Direct {
		return value
	}
	eff := g.effects[ev.Unique]
	if eff == nil {
		return value
	}
	if want == types.Machine {
		desired := ev
		desired.Control = types.Control{Transport: types.Machine}
		if len(eff.Ops) > 0 && eff.Ops[0].Abort {
			return &goast.CompositeLit{Type: g.effectTypeMode(desired, types.Machine), Elts: []goast.Expr{
				&goast.KeyValueExpr{Key: ident("Target"), Value: &goast.SelectorExpr{X: value, Sel: ident("Target")}},
			}}
		}
		g.usesFangort = true
		elts := make([]goast.Expr, 0, len(eff.Ops))
		sub := make(map[int]types.Type, len(eff.Params))
		for i, p := range eff.Params {
			if i < len(ev.Args) {
				sub[p.ID] = ev.Args[i]
			}
		}
		for _, op := range eff.Ops {
			var params []paramSpec
			var args []goast.Expr
			for i, raw := range op.ParamTypes {
				ty := types.SubstRigid(raw, sub)
				if g.isUnit(ty) {
					continue
				}
				name := fmt.Sprintf("t_evarg%d", i)
				params = append(params, paramSpec{name: name, typ: g.goType(ty)})
				args = append(args, ident(name))
			}
			resultTy := types.SubstRigid(op.ResultType, sub)
			call := callExpr(&goast.SelectorExpr{X: value, Sel: ident("Op_" + linkName(op.Name))}, args...)
			var runBody []goast.Stmt
			if actual == types.Exit {
				outcome := fmt.Sprintf("t_evout%d", g.tmp)
				g.tmp++
				runBody = append(runBody, varDeclStmt(outcome, g.outcomeType(resultTy), call))
				runBody = append(runBody, &goast.ReturnStmt{Results: []goast.Expr{selector(outcome, "Value"), selector(outcome, "Exit")}})
			} else if g.isUnit(resultTy) {
				runBody = append(runBody, exprStmt(call), &goast.ReturnStmt{Results: []goast.Expr{g.unitValue(), ident("nil")}})
			} else {
				runBody = append(runBody, &goast.ReturnStmt{Results: []goast.Expr{call, ident("nil")}})
			}
			run := &goast.FuncLit{Type: &goast.FuncType{
				Params: &goast.FieldList{}, Results: &goast.FieldList{List: []*goast.Field{
					{Type: ident("any")}, {Type: &goast.StarExpr{X: selector("fangort", "ExitRequest")}},
				}},
			}, Body: &goast.BlockStmt{List: runBody}}
			body := []goast.Stmt{returnStmt(callExpr(selector("fangort", "ImmediateMachine"), run))}
			fn := funcLitParams(params, selector("fangort", "MachineFrame"), body)
			elts = append(elts, &goast.KeyValueExpr{Key: ident("Op_" + linkName(op.Name)), Value: fn})
		}
		return &goast.CompositeLit{Type: g.effectTypeMode(desired, types.Machine), Elts: elts}
	}
	if want != types.Exit || actual == types.Exit || (len(eff.Ops) > 0 && eff.Ops[0].Abort) {
		return value
	}
	desired := ev
	desired.Control = types.Control{Transport: types.Exit}
	elts := make([]goast.Expr, 0, len(eff.Ops))
	sub := make(map[int]types.Type, len(eff.Params))
	for i, p := range eff.Params {
		if i < len(ev.Args) {
			sub[p.ID] = ev.Args[i]
		}
	}
	for _, op := range eff.Ops {
		var params []paramSpec
		var args []goast.Expr
		for i, raw := range op.ParamTypes {
			ty := types.SubstRigid(raw, sub)
			if g.isUnit(ty) {
				continue
			}
			name := fmt.Sprintf("t_evarg%d", i)
			params = append(params, paramSpec{name: name, typ: g.goType(ty)})
			args = append(args, ident(name))
		}
		resultTy := types.SubstRigid(op.ResultType, sub)
		call := callExpr(&goast.SelectorExpr{X: value, Sel: ident("Op_" + linkName(op.Name))}, args...)
		var body []goast.Stmt
		if g.isUnit(resultTy) {
			body = []goast.Stmt{exprStmt(call), returnStmt(g.normalOutcome(resultTy, g.unitValue()))}
		} else {
			body = []goast.Stmt{returnStmt(g.normalOutcome(resultTy, call))}
		}
		fn := funcLitParams(params, g.outcomeType(resultTy), body)
		elts = append(elts, &goast.KeyValueExpr{Key: ident("Op_" + linkName(op.Name)), Value: fn})
	}
	return &goast.CompositeLit{Type: g.effectType(desired), Elts: elts}
}

func (g *gen) effectDecls(effects []*types.EffectInfo) []goast.Decl {
	var out []goast.Decl
	for _, eff := range effects {
		if types.SurfaceName(eff.Name) == "IO" {
			continue
		}
		modes := []types.Transport{types.Direct, types.Exit}
		if g.machine {
			modes = append(modes, types.Machine)
		}
		for _, mode := range modes {
			oldNames, oldControl, oldABI := g.tyParamNames, g.control, g.abi
			g.tyParamNames, g.control, g.abi = map[int]string{}, mode, mode
			var fields []*goast.Field
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
			if len(eff.Ops) > 0 && eff.Ops[0].Abort {
				g.usesFangort = true
				fields = append(fields, &goast.Field{Names: []*goast.Ident{ident("Target")}, Type: &goast.StarExpr{X: selector("fangort", "ExitTarget")}})
			}
			for _, op := range eff.Ops {
				if op.Abort {
					continue
				}
				ps := make([]paramSpec, 0, len(op.ParamTypes))
				for _, t := range op.ParamTypes {
					if g.isUnit(t) {
						continue
					}
					ps = append(ps, paramSpec{typ: g.goType(t)})
				}
				results := &goast.FieldList{}
				if mode == types.Machine {
					g.usesFangort = true
					results = &goast.FieldList{List: []*goast.Field{{Type: selector("fangort", "MachineFrame")}}}
				} else if mode == types.Exit {
					results = &goast.FieldList{List: []*goast.Field{{Type: g.outcomeType(op.ResultType)}}}
				} else if !g.isUnit(op.ResultType) {
					results = &goast.FieldList{List: []*goast.Field{{Type: g.goType(op.ResultType)}}}
				}
				fields = append(fields, &goast.Field{Names: []*goast.Ident{ident("Op_" + linkName(op.Name))}, Type: &goast.FuncType{Params: paramFields(ps), Results: results}})
			}
			name := "Eff_" + linkName(eff.Name)
			if mode == types.Exit {
				name += "_exit"
			} else if mode == types.Machine {
				name += "_machine"
			}
			spec := &goast.TypeSpec{Name: ident(name), Type: &goast.StructType{Fields: &goast.FieldList{List: fields}}}
			if len(eff.Params) > 0 {
				fs := make([]*goast.Field, len(eff.Params))
				for i := range fs {
					fs[i] = &goast.Field{Names: []*goast.Ident{ident(fmt.Sprintf("E%d", i))}, Type: ident("any")}
				}
				spec.TypeParams = &goast.FieldList{List: fs}
			}
			out = append(out, &goast.GenDecl{Tok: gotoken.TYPE, Specs: []goast.Spec{spec}})
			g.tyParamNames, g.control, g.abi = oldNames, oldControl, oldABI
		}
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
	if core.ExprControl(e).Resolve(g.control) == types.Exit {
		return callExpr(funcLit(g.outcomeType(e.Ty), g.retStmtsFor(e, false)))
	}
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
	if g.control == types.Exit && core.ExprControl(let.Rhs).Resolve(g.control) == types.Exit {
		outcome := fmt.Sprintf("t_outcome%d", g.tmp)
		g.tmp++
		propagate := returnStmt(g.propagateOutcome(g.resultType, selector(outcome, "Exit")))
		stmts := []goast.Stmt{
			varDeclStmt(outcome, g.outcomeType(let.Rhs.Type()), g.expr(let.Rhs, 0)),
			&goast.IfStmt{Cond: &goast.BinaryExpr{X: selector(outcome, "Exit"), Op: gotoken.NEQ, Y: ident("nil")}, Body: &goast.BlockStmt{List: []goast.Stmt{propagate}}},
		}
		if g.isUnit(let.Rhs.Type()) {
			return stmts
		}
		name := mangleValue(let.Name)
		stmts = append(stmts, varDeclStmt(name, g.goType(let.Rhs.Type()), selector(outcome, "Value")))
		if !core.Mentions(let.Body, let.Name) {
			stmts = append(stmts, assignBlank(ident(name)))
		}
		return stmts
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
		if g.control == types.Exit && core.ExprControl(e).Resolve(g.control) == types.Exit {
			return g.exitPrefix(e)
		}
		if e.CalleeKind == core.Worker {
			return []goast.Stmt{g.workerCallStmt(e)}
		}
		return []goast.Stmt{assignBlank(g.expr(e, 0))}
	case *core.Perform:
		if g.control == types.Exit && core.ExprControl(e).Resolve(g.control) == types.Exit {
			return g.exitPrefix(e)
		}
		if e.Op.Native != nil && len(g.evidence[e.Effect.Unique]) == 0 {
			return g.nativeStmts(&core.NativeCall{Name: e.Op.Native.Name, Module: e.Op.Native.Module, Args: e.Args, Ty: e.Ty})
		}
		if g.isUnit(e.Op.ResultType) {
			stack := g.evidence[e.Effect.Unique]
			if len(stack) == 0 {
				panic("codegen: custom Perform without evidence")
			}
			var args []goast.Expr
			needPrelude := false
			for i, a := range e.Args {
				if i < len(e.Op.ParamTypes) && g.isUnit(e.Op.ParamTypes[i]) {
					needPrelude = needPrelude || !unitAtom(a)
					continue
				}
				args = append(args, g.expr(a, 0))
			}
			if !needPrelude {
				call := callExpr(&goast.SelectorExpr{X: stack[len(stack)-1], Sel: ident("Op_" + linkName(e.Op.Name))}, args...)
				return []goast.Stmt{exprStmt(call)}
			}
		}
		return []goast.Stmt{assignBlank(g.expr(e, 0))}
	case *core.ControlExit:
		return g.exitPrefix(e)
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

func (g *gen) exitPrefix(e core.Expr) []goast.Stmt {
	outcome := fmt.Sprintf("t_outcome%d", g.tmp)
	g.tmp++
	return []goast.Stmt{
		varDeclStmt(outcome, g.outcomeType(e.Type()), g.expr(e, 0)),
		&goast.IfStmt{
			Cond: &goast.BinaryExpr{X: selector(outcome, "Exit"), Op: gotoken.NEQ, Y: ident("nil")},
			Body: &goast.BlockStmt{List: []goast.Stmt{returnStmt(g.propagateOutcome(g.resultType, selector(outcome, "Exit")))}},
		},
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

// linkOps spells the operator characters Go cannot hold in an identifier.
// Words rather than a hash because generated Go is meant to be readable:
// `Basics.++` becomes `v_Basics_dot__plus__plus_`, which can be traced back
// to its source by eye.
var linkOps = map[byte]string{
	'!': "_bang_", '#': "_hash_", '%': "_pct_", '&': "_amp_", '*': "_star_",
	'+': "_plus_", '-': "_dash_", '/': "_slash_", ':': "_colon_", '<': "_lt_",
	'=': "_eq_", '>': "_gt_", '?': "_qmark_", '@': "_at_", '^': "_hat_",
	'|': "_bar_", '~': "_tilde_",
}

// linkName maps a canonical fango symbol to a Go identifier. Module
// separators and operator characters are the only characters a fango symbol
// can hold that Go cannot.
//
// The substitution is injective against ordinary names: identifier
// characters and operator characters are disjoint sets, a fango name may
// not begin with `_`, and the character after a `_dot_` is always a letter —
// so no identifier can spell one of these words in the position where an
// operator's would appear. It is a pure function of the name, which is what
// the byte-identical-Go requirement needs.
func linkName(name string) string {
	if !strings.ContainsFunc(name, func(r rune) bool {
		_, isOp := linkOps[byte(r)]
		return r == '.' || (r < 0x80 && isOp)
	}) {
		return name
	}
	var b strings.Builder
	for i := 0; i < len(name); i++ {
		switch c := name[i]; {
		case c == '.':
			b.WriteString("_dot_")
		default:
			if word, isOp := linkOps[c]; isOp {
				b.WriteString(word)
				continue
			}
			b.WriteByte(c)
		}
	}
	return b.String()
}
