package codegen

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	goast "go/ast"
	"go/format"
	goparser "go/parser"
	gotoken "go/token"
	"maps"
	"math"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/lower"
	"github.com/waj/fango/internal/types"
)

// Unit describes one Fango source module for package emission. Units must be
// dependency-first and contain exactly one entry.
//
// Program is the entry file's stem, and names the package the entry emits
// into. It is meaningless on a dependency, and it is carried on the unit rather
// than passed alongside it so that whole-program emission and the module
// backend agree on where the entry lands: they are compared path by path.
type Unit struct {
	Name    string
	Program string
	Imports []string
	Entry   bool
}

// File is one path relative to the generated Go module root.
type File struct {
	Path string
	Data []byte
}

// EmitProject lowers a whole Core program at once into one Go package per
// Fango module, with every definition body available throughout. It is the
// differential reference the module backend is compared against, and no
// command uses it: internal/backend emits each owner from its own Core and the
// declarations it links against, and must stay byte-identical to this.
func EmitProject(p *core.Prog, b *types.Builtins, units []Unit, printMain bool) ([]File, error) {
	if errs := core.Lint(p, b); len(errs) != 0 {
		return nil, fmt.Errorf("codegen: malformed Core: %v", errs[0])
	}
	return emitProject(p, b, units, printMain)
}

func emitProject(p *core.Prog, b *types.Builtins, units []Unit, printMain bool) ([]File, error) {
	if err := ValidateUnits(p, units); err != nil {
		return nil, err
	}
	files := make([]File, 0, len(units))
	for _, unit := range units {
		file, err := EmitUnit(UnitProgram(p, unit), b, unit, printMain)
		if err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

// ValidateUnits proves the unit set covers the program: one entry, no repeated
// owner, and a unit for every definition, type, and effect owner. It reads no
// definition body, so the module backend runs it on installed headers.
func ValidateUnits(p *core.Prog, units []Unit) error {
	entryCount := 0
	owners := make(map[string]bool, len(units))
	for _, unit := range units {
		if owners[unit.Name] {
			return fmt.Errorf("codegen: duplicate module unit %q", unit.Name)
		}
		owners[unit.Name] = true
		if unit.Entry {
			entryCount++
		}
	}
	if entryCount != 1 {
		return fmt.Errorf("codegen: module graph has %d entry units, want 1", entryCount)
	}
	for _, def := range p.Defs {
		if !owners[def.Owner] {
			return fmt.Errorf("codegen: definition %q has no module unit for owner %q", def.Name, def.Owner)
		}
	}
	for _, adt := range p.ADTs {
		owner := symbolOwner(adt.Con.Name)
		if !owners[owner] {
			return fmt.Errorf("codegen: type %q has no module unit for owner %q", adt.Con.Name, owner)
		}
	}
	for _, eff := range p.Effects {
		owner := symbolOwner(eff.Name)
		if types.SurfaceName(eff.Name) != "IO" && !owners[owner] {
			return fmt.Errorf("codegen: effect %q has no module unit for owner %q", eff.Name, owner)
		}
	}
	return nil
}

// EmitUnit emits one owner's Go package. p carries that owner's Core together
// with the installed dependency headers its calls link against. It reads no dependency
// body, so an owner can be emitted from its checked object alone.
func EmitUnit(p *core.Prog, b *types.Builtins, unit Unit, printMain bool) (File, error) {
	owned := *p
	owned.Defs = nil
	var context []core.Def
	for _, d := range p.Defs {
		if d.Owner == unit.Name {
			owned.Defs = append(owned.Defs, d)
		} else {
			context = append(context, d)
		}
	}
	if errs := core.LintIn(&owned, context, b); len(errs) != 0 {
		return File{}, fmt.Errorf("core lint: %v", errs)
	}
	data, err := emitUnit(p, b, unit, printMain)
	if err != nil {
		return File{}, err
	}
	return File{Path: UnitPath(unit), Data: data}, nil
}

// UnitPath is the generated file one unit owns, relative to the Go module
// root. Each entry program is a package main of its own beneath entries/, so
// several programs from one source directory share a build tree instead of
// overwriting each other; dependencies live below modules/ in their logical
// source layout, where two programs that import the same module share it.
func UnitPath(unit Unit) string {
	if unit.Entry {
		return filepath.ToSlash(filepath.Join("entries", EntryLinkName(unit.Program), "main.go"))
	}
	return filepath.ToSlash(filepath.Join("modules", strings.ReplaceAll(unit.Name, ".", "/"), "module.go"))
}

// UnitProgram narrows p to one owner. Imported definitions keep their headers
// — type, control, evidence order, ABI summary and bounded execution template —
// and lose their ordinary bodies. Emission cannot rediscover a dependency's
// calling convention from its implementation.
func UnitProgram(p *core.Prog, unit Unit) *core.Prog {
	out := *p
	out.Defs = make([]core.Def, len(p.Defs))
	for i := range p.Defs {
		out.Defs[i] = p.Defs[i]
		if out.Defs[i].Owner != unit.Name {
			out.Defs[i].Body = nil
		}
	}
	return &out
}

func emitUnit(p *core.Prog, b *types.Builtins, unit Unit, printMain bool) ([]byte, error) {
	lowered, err := lower.Module(p, unit.Name)
	if err != nil {
		return nil, err
	}
	g := &gen{
		disableOptimizations: p.DisableOptimizations,
		lowered:              lowered,
		b:                    b,
		adts:                 map[int]*types.ADTInfo{},
		caseVarTys:           map[string]types.Type{},
		evidence:             map[types.EffectKey][]goast.Expr{},
		evidenceModes:        map[types.EffectKey][]types.Transport{},
		defs:                 map[string]*core.Def{},
		unit:                 unit.Name,
		imports:              map[string]bool{},
		nativeImports:        map[string]bool{},
		direct:               map[string]bool{},
		natives:              p.Natives,
		effects:              map[int]*types.EffectInfo{},
		control:              types.Direct,
		abi:                  types.Direct,
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

	// Imported families are link contracts only: they supply row and
	// evidence shapes at call sites and are declared by their own owner.

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

	decls = append(decls, g.regexDecls...)
	decls = append(decls, g.descriptorDecls...)
	decls = append(decls, g.callableDecls...)
	// Imports come from emission (fangort for prints, math for float
	// specials), so they are prepended last — in a fixed order, for
	// deterministic output.
	if imports := g.importsDecl(decls); imports != nil {
		decls = append([]goast.Decl{imports}, decls...)
	}

	packageName := "main"
	if !unit.Entry {
		packageName = "fangomod"
	}
	file := &goast.File{Name: ident(packageName), Decls: decls}
	g.splitOutcomeABI(file)
	g.inlineReturnCalls(file)
	g.inlineBoundOperations(file)
	g.elideCopies(file)
	var buf bytes.Buffer
	if err := format.Node(&buf, gotoken.NewFileSet(), file); err != nil {
		return nil, fmt.Errorf("codegen: printing generated Go: %w", err)
	}
	return buf.Bytes(), nil
}

func (g *gen) topValueDecl(d *core.Def, mode types.Transport) goast.Decl {
	oldControl, oldABI := g.control, g.abi
	g.control, g.abi = types.Direct, mode
	defer func() { g.control, g.abi = oldControl, oldABI }()
	name := g.topValueName(d.Name)
	if mode == types.Exit {
		name += "_exit"
	}

	return varDecl(name, g.goType(d.Type), g.expr(d.Body, 0))
}

type gen struct {
	b               *types.Builtins
	adts            map[int]*types.ADTInfo
	tmp             int // type-switch binding counter (ts0, ts1, …)
	usesFangort     bool
	usesMath        bool
	descriptorNames map[string]string
	descriptorDecls []goast.Decl
	callableNames   map[string]string
	callableDecls   []goast.Decl
	regexNames      map[string]string
	regexDecls      []goast.Decl
	rowPreparation  *rowPreparation
	forwarders      map[string]*core.Lambda

	// tyParamNames maps the rigid vars of the definition (or derived
	// function) currently being emitted to their Go type-parameter names
	// (positional: A0, A1, …). Reset per definition.
	tyParamNames        map[int]string
	polyDescriptorNames map[int]string // clause-local descriptors supplied by PolyRequest

	// caseVarTys records the (instantiated) types of locals visible to a
	// decision tree: worker/lambda parameters, case scrutinee binders, and
	// constructor field temporaries. Multi-column pattern matrices may test
	// any parameter directly, so constructor switches need all of them here.
	caseVarTys           map[string]types.Type
	evidence             map[types.EffectKey][]goast.Expr
	evidenceModes        map[types.EffectKey][]types.Transport
	rows                 map[types.CaptureVar][]goast.Expr
	defs                 map[string]*core.Def
	unit                 string
	imports              map[string]bool
	nativeImports        map[string]bool
	direct               map[string]bool
	natives              map[string]*types.NativeInfo
	effects              map[int]*types.EffectInfo
	control              types.Transport
	flatCallbacks        map[string]flatCallback
	lowered              *lower.Program
	outcomeCalls         map[*goast.CallExpr]goast.Expr
	unitOutcomeCalls     map[*goast.CallExpr]bool
	fixedOperations      map[activationLabel]map[string]*goast.FuncLit
	knownOperations      map[goast.Expr]map[string]*goast.FuncLit
	directActivations    map[string]map[string]directOperation
	disableOptimizations bool
	// abi selects the Direct/Exit representation family for controlled
	// function and ADT values in the declaration currently being emitted.
	// Unlike control, it does not change when emission enters a pure nested
	// lambda: that lambda still consumes and produces the enclosing family's
	// representations even though its own call returns directly.
	abi        types.Transport
	resultType types.Type
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

// EntryLinkName is the path component an entry program's package occupies. A
// module name is an identifier, but an entry's stem is a file name and can
// hold anything the filesystem allows, while the package sits at
// fangobuild/entries/<component> — and Go rejects a space, a trailing tilde
// and digits, and the Windows device names on every platform. A stem Go would
// take is used as it is; anything else is sanitized and disambiguated by a
// digest of the stem it came from, which no usable stem can collide with
// because only the escaped form contains a hyphen.
func EntryLinkName(stem string) string {
	if usablePathComponent(stem) && !strings.Contains(stem, "-") {
		return stem
	}
	var b strings.Builder
	for i := 0; i < len(stem); i++ {
		c := stem[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' {
			b.WriteByte(c)
		}
	}
	name := b.String()
	if len(name) > 24 {
		name = name[:24]
	}
	if name == "" {
		name = "entry"
	}
	sum := sha256.Sum256([]byte(stem))
	return name + "-" + hex.EncodeToString(sum[:4])
}

// deviceNames are rejected as a path component by the Go tool whatever the
// host, so a program whose file is named after one cannot use its own stem.
var deviceNames = map[string]bool{"con": true, "prn": true, "aux": true, "nul": true,
	"com1": true, "com2": true, "com3": true, "com4": true, "com5": true,
	"com6": true, "com7": true, "com8": true, "com9": true,
	"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true, "lpt5": true,
	"lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true}

func usablePathComponent(name string) bool {
	if name == "" || strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".") {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-' {
			continue
		}
		return false
	}
	// The tool compares the part before the first dot, so Point.aux is fine
	// and aux.Point is not.
	head, _, _ := strings.Cut(name, ".")
	return !deviceNames[strings.ToLower(head)]
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

		}
	}
	return g.qualified(symbolOwner(ctor.Result.Name), name)
}

func (g *gen) controlledType(t types.Type, visiting map[int]bool) bool {
	return types.ControlledRepresentation(t, g.adts)
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

func (g *gen) importsDecl(decls []goast.Decl) goast.Decl {
	// Representation-only references can mark an owner during emission even
	// when the final Go type erases that reference. Keep its initialization
	// dependency, but do not give an unused import a named alias.
	used := make(map[string]bool)
	for _, decl := range decls {
		goast.Inspect(decl, func(node goast.Node) bool {
			if selected, ok := node.(*goast.SelectorExpr); ok {
				if owner, ok := selected.X.(*goast.Ident); ok {
					used[owner.Name] = true
				}
			}
			return true
		})
	}
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
		if g.nativeImports[name] && used[nativeAlias(name)] {
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
		if g.imports[name] && used[moduleAlias(name)] {
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

// workerDef emits a top-level function definition as an uncurried Go func
// (doc/design.md, "Go backend and runtime" item 1): the parameter types peel off the curried Fango type, the
// body emits in return-position statement context. A generic definition's
// TyParams become Go type parameters — `any` for General vars,
// fangort.Number for Number-kinded ones (doc/design.md, "Type inference", doc/design.md, "Go backend and runtime").
func (g *gen) workerDef(d *core.Def, mode, abi types.Transport) goast.Decl {
	oldCallbacks := g.flatCallbacks
	g.flatCallbacks = map[string]flatCallback{}
	defer func() { g.flatCallbacks = oldCallbacks }()
	oldForwarders := g.forwarders
	g.forwarders = nil
	defer func() { g.forwarders = oldForwarders }()
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
	params := g.descriptorParams(d.TyParams)
	evidenceNames := map[string]int{}
	for _, ev := range d.EffectParams {
		name := g.evidenceName(ev.Name)
		if n := evidenceNames[name]; n > 0 {
			name = fmt.Sprintf("%s_%d", name, n)
		}
		evidenceNames[g.evidenceName(ev.Name)]++
		params = append(params, paramSpec{name: name, typ: g.effectType(ev)})
		g.evidence[ev.Key()] = append(g.evidence[ev.Key()], ident(name))
		g.evidenceModes[ev.Key()] = append(g.evidenceModes[ev.Key()], mode)
	}
	if d.RowParam != 0 {
		name := fmt.Sprintf("rowParam%d", g.tmp)
		g.tmp++
		params = append(params, paramSpec{name: name, typ: g.rowType()})
		defer g.pushRow(d.RowParam, ident(name))()
		defer g.bindDeferredEffects(d.RowEffects, ident(name), mode)()
	}
	for i, name := range d.Params {
		if g.isUnit(argTys[i]) {
			continue
		}
		if name != "_" {
			name = mangleValue(name)
		}
		typ := g.goType(argTys[i])
		if contract, selected, ok := g.callbackContract(d, i, mode); ok {
			typ = g.flatCallbackType(argTys[i], contract.Arity, selected)
			g.flatCallbacks[d.Params[i]] = flatCallback{contract.Arity, selected}
		}
		params = append(params, paramSpec{name: name, typ: typ})
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
	finishRows := g.prepareRows()
	body = g.loweredStmts(d, g.lowered.Functions[d.Name].Body, false, g.isUnit(ret))
	body = finishRows(body)
	name := g.topValueName(d.Name)
	if abi == types.Exit {
		name += "_exit"
	}

	decl := workerDecl(name, params, result, body).(*goast.FuncDecl)
	for _, ev := range d.EffectParams {
		g.evidence[ev.Key()] = g.evidence[ev.Key()][:len(g.evidence[ev.Key()])-1]
		g.evidenceModes[ev.Key()] = g.evidenceModes[ev.Key()][:len(g.evidenceModes[ev.Key()])-1]
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
	if d != nil && d.ABI.Valid {
		return d.ABI.NeedsFamily
	}
	args, ret := core.PeelFun(d.Type, len(d.Params))
	if g.workerNeedsControlledArgTypes(args) {
		return true
	}
	return g.controlledType(ret, nil)
}

// Passive factories transfer stored callbacks without executing or constructing
// them. Their representation family follows their values, while their execution
// remains Direct. Closure-producing factories also need module-owned lowering.

// workerCallsControlledArg reports whether the worker actually invokes one of
// its controlled function parameters. Merely storing such a callback (for
// example in an ADT) needs the Exit representation family, but does not make
// the worker itself return an Outcome.
func (g *gen) workerCallsControlledArg(d *core.Def) bool {
	if d != nil && d.ABI.Valid {
		return d.ABI.CallsControlledArg
	}
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
	core.InspectPruned(d.Body, func(e core.Expr) bool {
		if _, ok := e.(*core.Lambda); ok {
			return false
		}
		if app, ok := e.(*core.App); ok && app.Control != (types.Control{}) {
			for name := range controlled {
				if core.Mentions(app.Callee, name) {
					called = true
				}
			}
		}
		return true
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
	if scope, ok := g.scopeBracketCall(e); ok {
		return assignBlank(scope)
	}
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
	args := g.typeDescriptorArgs(e.TyArgs)
	for _, ev := range e.EvidenceArgs {
		stack := g.evidence[ev.Key()]
		if len(stack) == 0 {
			panic("codegen: missing lexical evidence")
		}
		args = append(args, g.evidenceArg(ev, stack[len(stack)-1], g.currentEvidenceMode(ev.Key()), mode))
	}
	if e.Row != nil {
		args = append(args, g.rowArgument(e.Row))
	}
	for i, a := range e.Args {
		if i < len(formal) && g.isUnit(formal[i]) {
			continue
		}
		args = append(args, g.workerArgument(g.defs[ref.Name], i, a, mode))
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
	if stmts, ok := g.immediateUnitApplication(e, func(body core.Expr) []goast.Stmt {
		return g.retStmtsFor(body, unitResult)
	}); ok {
		return stmts
	}
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

// goType maps a Fango type to its unboxed Go representation (see
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
		return g.callbackType(t)
	case *types.TCon:
		if t.Name == types.FailureTypeName {
			g.usesFangort = true
			return &goast.StarExpr{X: selector("fangort", "Failure")}
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
				if adt.Repr == types.ReprNativeAny {
					return ident("any")
				}
				if adt.Repr == types.ReprBytes {
					// The bundled Bytes is an immutable []byte and takes no
					// type arguments (doc/design/backend.md).
					g.usesFangort = true
					return selector("fangort", "Bytes")
				}
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
	needsSequence := n.Storage.Kind != ""
	for i, arg := range call.Args {
		needsSequence = needsSequence || g.isUnit(arg.Type()) && !unitAtom(arg) || g.paramWrapper(n, i) != nil
	}
	var prelude []goast.Stmt
	for i, arg := range call.Args {
		opaque := (n.Storage.Kind == "new" || n.Storage.Kind == "write") && i == n.Storage.Payload
		if g.isUnit(arg.Type()) && !opaque {
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
			value = g.unwrapBoundaryAt(wrapper, arg.Type(), value)
		}
		if opaque {
			g.usesFangort = true
			value = callExpr(selector("fangort", "PackNativeValue"), value)
		}
		args = append(args, value)
	}
	fn := selector(nativeAlias(n.Module), exportNativeName(types.SurfaceName(n.Name)))
	var result goast.Expr = callExpr(fn, args...)
	if n.Storage.Kind == "read" {
		g.usesFangort = true
		return prelude, callExpr(indexExpr(selector("fangort", "UnpackNativeValue"), []goast.Expr{g.goType(call.Ty)}), result)
	}
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

func (g *gen) boundaryTypeArgs(wrapper *types.CtorInfo, ty types.Type) []types.Type {
	if con, ok := ty.(*types.TCon); ok {
		return runtimeADTArgs(g.adts[wrapper.Result.Unique], con.Args)
	}
	return nil
}

func (g *gen) unwrapBoundaryAt(wrapper *types.CtorInfo, ty types.Type, value goast.Expr) goast.Expr {
	if productADT(g.adts[wrapper.Result.Unique]) {
		return &goast.SelectorExpr{X: value, Sel: ident(fieldName(0))}
	}
	ctor := indexExpr(g.ctorRef(wrapper), g.goTypes(g.boundaryTypeArgs(wrapper, ty)))
	asserted := &goast.TypeAssertExpr{X: value, Type: &goast.StarExpr{X: ctor}}
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
		result = g.ctorValue(n.ResultWrapper, g.boundaryTypeArgs(n.ResultWrapper, ty), result)
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
	fields[shape.LocationIdx] = selector("t_failure", "Path")
	fields[shape.MessageIdx] = selector("t_failure", "Message")
	failure := g.ctorValue(shape.Err, resultArgs, g.ctorValue(shape.Error, nil, fields...))
	classifier := "ClassifyIOError"
	if shape.Classifier == "net" {
		classifier = "ClassifyNetError"
	}
	body = append(body, ifStmt(binExpr(gotoken.NEQ, ident("t_err"), ident("nil")), []goast.Stmt{
		varDeclStmt("t_failure", selector("fangort", "IOFailure"), callExpr(selector("fangort", classifier), ident("t_err"))),
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
	s := template
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
	case *core.RegexLit:
		if g.regexNames == nil {
			g.regexNames = map[string]string{}
		}
		name := g.regexNames[e.Pattern]
		if name == "" {
			name = fmt.Sprintf("t_regexLiteral%d", len(g.regexNames))
			g.regexNames[e.Pattern] = name
			g.usesFangort = true
			value := g.ctorValue(e.Ctor, nil, callExpr(selector("fangort", "RegexLiteral"), stringLit(e.Pattern)))
			g.regexDecls = append(g.regexDecls, varDecl(name, g.goType(e.Ty), value))
		}
		return ident(name)
	case *core.StringLit:
		return stringLit(e.Val)
	case *core.CharLit:
		return &goast.BasicLit{Kind: gotoken.CHAR, Value: strconv.QuoteRune(e.Val)}
	case *core.UnitLit:
		return g.unitValue()
	case *core.BoolLit:
		return ident(strconv.FormatBool(e.Val))
	case *core.AttributeLookup:
		panic("codegen: attributes in emitted code")
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
			if g.controlledType(e.Ty, nil) {
				mode = g.representationMode()
			}
			return g.topValueRefMode(e.Name, mode)
		}
		return ident(mangleValue(e.Name))
	case *core.Let:
		return g.letIIFE(e)
	case *core.Lambda:
		return g.callbackValue(e)
	case *core.App:
		switch e.CalleeKind {
		case core.Worker:
			return g.workerCallExpr(e)
		case core.Value:
			if call, ok := g.flatCallbackCall(e); ok {
				return call
			}
			// One typed indirect call per application; chains render
			// e(a)(b). Call is a Go primary expression — no parens needed,
			// and a func-literal callee called in place is legal Go.
			mode := e.Control.Resolve(g.control)
			args := make([]goast.Expr, 0, len(e.EvidenceArgs)+1)
			for _, ev := range e.EvidenceArgs {
				stack := g.evidence[ev.Key()]
				if len(stack) == 0 {
					panic("codegen: missing lexical evidence")
				}
				args = append(args, g.evidenceArg(ev, stack[len(stack)-1], g.currentEvidenceMode(ev.Key()), mode))
			}
			if e.Row != nil {
				args = append(args, g.rowArgument(e.Row))
			}
			args = append(args, g.expr(e.Args[0], 0))
			if lam, ok := e.Callee.(*core.Lambda); ok {
				return callExpr(g.directLambdaMember(lam, mode), args...)
			}
			if ref, ok := e.Callee.(*core.VarRef); ok && ref.Local {
				if lam := g.forwarders[ref.Name]; lam != nil {
					return callExpr(g.directLambdaMember(lam, mode), args...)
				}
			}
			call := callExpr(callbackMember(g.expr(e.Callee, 0), mode), args...)
			if mode == types.Exit {
				g.markOutcomeCall(call.(*goast.CallExpr), g.goType(e.Ty))
			}
			return call
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

	case *core.FailureInspect:
		return g.failureInspectExpr(e)

	case *core.ParallelMap:
		return g.parallelMap(e)
	case *core.AsyncLaunch:
		return g.asyncLaunch(e)
	case *core.AsyncSupervise:
		return g.expr(e.Call, parentPrec)
	case *core.AsyncRebase:
		return g.asyncRebase(e)
	case *core.NativeCall:
		return g.nativeExpr(e, parentPrec)
	case *core.If:
		// Go has no expression-if: an immediately-invoked typed closure
		// preserves branch laziness and stays gofmt-clean. ANF hoisting, as
		// documented in doc/design.md, "Core and evidence invariants", bypasses this inside function bodies; it
		// remains the top-level-initializer fallback.
		return g.controlIIFE(e, func() []goast.Stmt {
			body := []goast.Stmt{&goast.IfStmt{
				Cond: g.expr(e.Cond, 0),
				Body: &goast.BlockStmt{List: g.retStmts(e.Then)},
			}}
			return append(body, g.retStmts(e.Else)...)
		})
	case *core.Case:
		// Expression-context fallback (top-level initializers): an
		// immediately-invoked typed closure, exactly like If above. Inside
		// function bodies the elaborator's ANF hoisting bypasses this.
		return g.controlIIFE(e, func() []goast.Stmt {
			return g.caseStmts(e, g.retStmts)
		})
	case *core.Perform:
		if e.Op.Native != nil && len(g.evidence[e.Effect.Key()]) == 0 {
			return g.nativeExpr(&core.NativeCall{Name: e.Op.Native.Name, Module: e.Op.Native.Module, Args: e.Args, Ty: e.Ty}, parentPrec)
		}
		stack := g.evidence[e.Effect.Key()]
		if len(stack) == 0 {
			panic("codegen: custom Perform without evidence")
		}
		if len(e.Op.LocalVars) > 0 && e.Op.Native == nil {
			return g.polymorphicPerformExpr(e, stack[len(stack)-1])
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
			call := callExpr(g.operationCallee(stack[len(stack)-1], "Op_"+linkName(e.Op.Name)), as...)
			if g.currentEvidenceMode(e.Effect.Key()) == types.Exit {
				g.markOutcomeCall(call.(*goast.CallExpr), g.goType(e.Ty))
			}
			return call
		}
		// A perform that resolves to Exit in this context but reaches a
		// Direct handler activation gets a plain result back — a void call
		// for a Unit operation — and must wrap it as a normal Outcome, the
		// same adaptation evidenceArg applies when Direct evidence is passed
		// to an Exit worker.
		exit := e.Control.Resolve(g.control) == types.Exit
		directEvidence := g.currentEvidenceMode(e.Effect.Key()) != types.Exit
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
		stack := g.evidence[e.Effect.Key()]
		if len(stack) == 0 {
			panic("codegen: ControlExit without lexical evidence")
		}
		payload := make([]goast.Expr, len(e.Payload))
		descriptors := make([]goast.Expr, len(e.Payload))
		for i, p := range e.Payload {
			// Interface payloads otherwise default untyped literals (notably Int)
			// to Go's `int`, while handler frames consistently expect the Fango
			// representation selected by goType.
			payload[i] = callExpr(g.goType(p.Type()), g.expr(p, 0))
			descriptors[i] = g.typeDescriptor(p.Type())
		}
		exit := &goast.UnaryExpr{Op: gotoken.AND, X: &goast.CompositeLit{Type: selector("fangort", "ExitRequest"), Elts: []goast.Expr{
			&goast.KeyValueExpr{Key: ident("Target"), Value: callExpr(selector("fangort", "ResolveExitTarget"), &goast.SelectorExpr{X: stack[len(stack)-1], Sel: ident("Target")})},
			&goast.KeyValueExpr{Key: ident("Effect"), Value: stringLit(e.Op.Owner.Name)},
			&goast.KeyValueExpr{Key: ident("Operation"), Value: intLit(int64(e.Op.Index))},
			&goast.KeyValueExpr{Key: ident("OperationName"), Value: stringLit(e.Op.Name)},
			&goast.KeyValueExpr{Key: ident("Payload"), Value: &goast.CompositeLit{Type: &goast.ArrayType{Elt: ident("any")}, Elts: payload}},
			&goast.KeyValueExpr{Key: ident("PayloadTypes"), Value: &goast.CompositeLit{Type: &goast.ArrayType{Elt: g.descriptorType()}, Elts: descriptors}},
		}}}
		return g.propagateOutcome(e.Ty, exit)
	case *core.ResumeTail:
		panic("codegen: ResumeTail outside verified handler-clause emission")
	case *core.Seq:
		return g.controlIIFE(e, func() []goast.Stmt {
			return append(g.stmts(e.First), g.retStmts(e.Then)...)
		})
	case *core.Handle:
		return g.handleExpr(e)
	case *core.Bracket:
		return g.bracketExpr(e)

	default:
		panic(fmt.Sprintf("codegen: node %T arrives in a later slice", e))
	}
}

func (g *gen) workerCallExpr(e *core.App) goast.Expr {
	if scope, ok := g.scopeBracketCall(e); ok {
		return scope
	}
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
	args := g.typeDescriptorArgs(e.TyArgs)
	for _, ev := range e.EvidenceArgs {
		stack := g.evidence[ev.Key()]
		if len(stack) == 0 {
			panic("codegen: missing lexical evidence")
		}
		args = append(args, g.evidenceArg(ev, stack[len(stack)-1], g.currentEvidenceMode(ev.Key()), mode))
	}
	if e.Row != nil {
		args = append(args, g.rowArgument(e.Row))
	}
	leadingArgs := len(args)
	needPrelude := false
	for i, a := range e.Args {
		if i < len(formal) && g.isUnit(formal[i]) {
			if !unitAtom(a) {
				needPrelude = true
			}
			continue
		}
		args = append(args, g.workerArgument(g.defs[ref.Name], i, a, mode))
	}
	if needPrelude {
		var body []goast.Stmt
		args = args[:leadingArgs]
		for i, a := range e.Args {
			if i < len(formal) && g.isUnit(formal[i]) {
				body = append(body, g.stmts(a)...)
				continue
			}
			name := fmt.Sprintf("t_u%d", g.tmp)
			g.tmp++
			body = append(body, varDeclStmt(name, g.workerArgumentType(g.defs[ref.Name], i, a.Type(), mode), g.workerArgument(g.defs[ref.Name], i, a, mode)))
			args = append(args, ident(name))
		}
		call := callExpr(indexExpr(g.topValueRefMode(ref.Name, abi), g.goTypes(e.TyArgs)), args...)
		if mode == types.Exit {
			g.markWorkerOutcomeCall(call.(*goast.CallExpr), g.goType(e.Ty), g.defs[ref.Name])
		}
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
	if mode == types.Exit {
		g.markWorkerOutcomeCall(call.(*goast.CallExpr), g.goType(e.Ty), g.defs[ref.Name])
	}
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

func unitAtom(e core.Expr) bool {
	switch e.(type) {
	case *core.UnitLit, *core.VarRef:
		return true
	default:
		return false
	}
}

// activationLabel names one application of one handler activation: the
// unit a backend evidence record, its origin, and its exit target serve.
type activationLabel struct {
	handle *core.Handle
	label  int
}

func (g *gen) handleExpr(e *core.Handle) goast.Expr {
	if e.HandlesAbort() {
		return g.abortHandleExpr(e)
	}
	stateCell := ""
	var state *handlerState
	if e.State != nil {
		stateCell = fmt.Sprintf("t_state%d", g.tmp)
		g.tmp++
		state = g.cellState(stateCell, e.State.Name)
	}
	// One record per application, all over the same state cell. A record's
	// clauses are the ones serving its application.
	decls := make([]goast.Stmt, 0, 2*len(e.Effects))
	for label := range e.Effects {
		decls = append(decls, g.resumptiveRecord(e, label, state)...)
	}
	body := g.expr(e.Body, 0)
	for _, inst := range e.Effects {
		g.evidence[inst.Key()] = g.evidence[inst.Key()][:len(g.evidence[inst.Key()])-1]
		g.evidenceModes[inst.Key()] = g.evidenceModes[inst.Key()][:len(g.evidenceModes[inst.Key()])-1]
	}
	// A handler whose subject does not perform the handled effect still
	// constructs valid lexical evidence; keep the local legal in Go even
	// when no generated operation call refers to it.
	var stmts []goast.Stmt
	if e.State != nil {
		initial := g.expr(e.State.Initial, 0)
		stateType := g.goType(e.State.Ty)
		initial = callExpr(indexExpr(selector("fangort", "NewHandlerState"), []goast.Expr{stateType}), initial)
		stateType = &goast.StarExpr{X: indexExpr(selector("fangort", "HandlerState"), []goast.Expr{stateType})}
		stmts = append(stmts, varDeclStmt(stateCell, stateType, initial))
	}
	stmts = append(stmts, decls...)
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
		snapshot := state.read()
		stmts = append(stmts, varDeclStmt(mangleValue(e.State.Name), g.goType(e.State.Ty), snapshot))
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

// abortHandleExpr installs only a unique target token. Performing an abort
// constructs an ExitRequest; the clause is invoked here, after the handled
// body has unwound and the handler's evidence has been removed.
// cellState describes a handler's state cell held in a fangort.HandlerState,
// which clauses read by snapshot and resumes commit to.
func (g *gen) cellState(cell, name string) *handlerState {
	g.usesFangort = true
	return &handlerState{
		cell:  cell,
		name:  name,
		read:  func() goast.Expr { return callExpr(selector(cell, "Snapshot")) },
		write: func(next goast.Expr) goast.Stmt { return exprStmt(callExpr(selector(cell, "Store"), next)) },
	}
}

// resumptiveRecord declares the evidence record of one resumptive application
// of a handler, in the transport its own clauses need, and installs it as the
// lexical evidence for the handler's body. The caller pops it.
func (g *gen) resumptiveRecord(e *core.Handle, label int, state *handlerState) []goast.Stmt {
	inst := e.Effects[label]
	mode := inst.Control.Resolve(g.control)
	st, record := g.forkableHandlerEvidence(e, label, mode, state)
	name := fmt.Sprintf("ev%d", g.tmp)
	g.tmp++
	evidenceValue := ident(name)
	if operations := g.fixedOperations[activationLabel{e, label}]; len(operations) != 0 {
		if g.knownOperations == nil {
			g.knownOperations = map[goast.Expr]map[string]*goast.FuncLit{}
		}
		g.knownOperations[evidenceValue] = operations
	}
	if mode == types.Direct {
		g.recordDirectActivation(name, e, label)
	}
	g.evidence[inst.Key()] = append(g.evidence[inst.Key()], evidenceValue)
	g.evidenceModes[inst.Key()] = append(g.evidenceModes[inst.Key()], mode)
	return []goast.Stmt{varDeclStmt(name, st, record), assignBlank(evidenceValue)}
}

// labelAborts reports whether the clauses serving one application of a
// handler are abort clauses.
func labelAborts(e *core.Handle, label int) bool {
	for _, c := range e.Clauses {
		if c.Effect == label {
			return c.Op != nil && c.Op.Abort
		}
	}
	return false
}

// abortHandleExpr emits an activation with at least one abort application.
// Its body runs as an Outcome closure so exits reach the dispatch below. A
// resumptive application beside the aborts keeps an ordinary record in its
// own transport; all applications share the state cell, which abort clauses
// read by snapshot.
func (g *gen) abortHandleExpr(e *core.Handle) goast.Expr {
	g.usesFangort = true
	overall := e.Control.Resolve(g.control)
	// One exit target and one record per abort application: an exit names
	// the application it aborts through, and the activation owns them all.
	targetNames := make([]string, len(e.Effects))
	mixed := false
	for i := range e.Effects {
		if labelAborts(e, i) {
			targetNames[i] = fmt.Sprintf("t_target%d", g.tmp)
			g.tmp++
		} else {
			mixed = true
		}
	}
	outcomeName := fmt.Sprintf("t_handle%d", g.tmp)
	g.tmp++
	stateCell := ""
	var state *handlerState
	if e.State != nil {
		stateCell = fmt.Sprintf("t_state%d", g.tmp)
		g.tmp++
		if mixed {
			state = g.cellState(stateCell, e.State.Name)
		}
	}
	readState := func() goast.Expr {
		if state != nil {
			return state.read()
		}
		return ident(stateCell)
	}

	var stmts []goast.Stmt
	if e.State != nil {
		initial := g.expr(e.State.Initial, 0)
		stateType := g.goType(e.State.Ty)
		if mixed {
			initial = callExpr(indexExpr(selector("fangort", "NewHandlerState"), []goast.Expr{stateType}), initial)
			stateType = &goast.StarExpr{X: indexExpr(selector("fangort", "HandlerState"), []goast.Expr{stateType})}
		}
		stmts = append(stmts, varDeclStmt(stateCell, stateType, initial))
	}
	for i, inst := range e.Effects {
		if targetNames[i] == "" {
			stmts = append(stmts, g.resumptiveRecord(e, i, state)...)
			continue
		}
		target := &goast.UnaryExpr{Op: gotoken.AND, X: &goast.CompositeLit{Type: selector("fangort", "ExitTarget"), Elts: []goast.Expr{
			&goast.KeyValueExpr{Key: ident("Marker"), Value: intLit(1)},
		}}}
		stmts = append(stmts, varDeclStmt(targetNames[i], &goast.StarExpr{X: selector("fangort", "ExitTarget")}, target))
		evidenceName := fmt.Sprintf("ev%d", g.tmp)
		g.tmp++
		st := g.effectType(inst)
		evidenceValue := &goast.UnaryExpr{Op: gotoken.AND, X: &goast.CompositeLit{Type: st.(*goast.StarExpr).X, Elts: []goast.Expr{
			&goast.KeyValueExpr{Key: ident("Origin"), Value: g.evidenceOrigin(inst)},
			&goast.KeyValueExpr{Key: ident("Target"), Value: ident(targetNames[i])},
		}}}
		stmts = append(stmts, varDeclStmt(evidenceName, st, g.completeEvidence(inst, evidenceValue, types.Exit)), assignBlank(ident(evidenceName)))
		g.evidence[inst.Key()] = append(g.evidence[inst.Key()], ident(evidenceName))
		g.evidenceModes[inst.Key()] = append(g.evidenceModes[inst.Key()], types.Exit)
	}
	oldControl, oldResult := g.control, g.resultType
	g.control, g.resultType = types.Exit, e.Body.Type()
	body := callExpr(funcLit(g.outcomeType(e.Body.Type()), g.retStmtsFor(e.Body, g.isUnit(e.Body.Type()))))
	g.control, g.resultType = oldControl, oldResult
	for _, inst := range e.Effects {
		g.evidence[inst.Key()] = g.evidence[inst.Key()][:len(g.evidence[inst.Key()])-1]
		g.evidenceModes[inst.Key()] = g.evidenceModes[inst.Key()][:len(g.evidenceModes[inst.Key()])-1]
	}
	stmts = append(stmts, varDeclStmt(outcomeName, g.outcomeType(e.Body.Type()), body))

	exit := &goast.SelectorExpr{X: ident(outcomeName), Sel: ident("Exit")}
	foreignBody := []goast.Stmt{returnStmt(g.propagateOutcome(e.Ty, exit))}
	if overall == types.Direct {
		foreignBody = g.zeroReturn(e.Ty)
	}
	exitTarget := &goast.SelectorExpr{X: exit, Sel: ident("Target")}
	var targetMismatch goast.Expr
	abortLabels := 0
	for _, name := range targetNames {
		if name == "" {
			continue
		}
		abortLabels++
		mismatch := &goast.BinaryExpr{X: exitTarget, Op: gotoken.NEQ, Y: ident(name)}
		if targetMismatch == nil {
			targetMismatch = mismatch
		} else {
			targetMismatch = &goast.BinaryExpr{X: targetMismatch, Op: gotoken.LAND, Y: mismatch}
		}
	}
	hasExitBody := []goast.Stmt{&goast.IfStmt{Cond: targetMismatch, Body: &goast.BlockStmt{List: foreignBody}}}
	for _, clause := range e.Clauses {
		if !clause.Op.Abort {
			continue
		}
		var cond goast.Expr = &goast.BinaryExpr{X: &goast.SelectorExpr{X: exit, Sel: ident("Operation")}, Op: gotoken.EQL, Y: intLit(int64(clause.Op.Index))}
		if abortLabels > 1 {
			cond = &goast.BinaryExpr{X: &goast.BinaryExpr{X: exitTarget, Op: gotoken.EQL, Y: ident(targetNames[clause.Effect])}, Op: gotoken.LAND, Y: cond}
		}
		var clauseStmts []goast.Stmt
		if clause.SuppressedParam != "" {
			clauseStmts = append(clauseStmts, varDeclStmt(mangleValue(clause.SuppressedParam), g.goType(clause.SuppressedType), callExpr(selector("fangort", "SnapshotSuppressed"), exit)), assignBlank(ident(mangleValue(clause.SuppressedParam))))
		}
		if e.State != nil {
			clauseStmts = append(clauseStmts, varDeclStmt(mangleValue(e.State.Name), g.goType(e.State.Ty), readState()), assignBlank(ident(mangleValue(e.State.Name))))
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
			stmts = append(stmts, varDeclStmt(mangleValue(e.State.Name), g.goType(e.State.Ty), readState()))
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

// handlerState supplies the snapshot and commit operations for an activation.
// Each access publishes a complete value; no lock spans clause evaluation.
type handlerState struct {
	cell  string
	name  string
	read  func() goast.Expr
	write func(goast.Expr) goast.Stmt
}

// unchanged reports a next state that is the clause's own snapshot. Core
// never shadows, so the binder still holds what the cell held at clause
// entry; the commit would store that value back and is omitted.
func (s *handlerState) unchanged(next core.Expr) bool {
	ref, ok := next.(*core.VarRef)
	return ok && ref.Name == s.name
}

// handlerEvidence builds an installed activation's record of operation
// closures: one ordinary Go function per clause over the handler's state. The
// record's protocol is the activation's own — its clauses' — so this is also
// how a handler installs an ordinary record inside a machine worker, when its
// clauses neither exit nor suspend.
func (g *gen) handlerEvidence(e *core.Handle, label int, mode types.Transport, state *handlerState) (goast.Expr, goast.Expr) {
	var elts []goast.Expr
	for _, c := range e.Clauses {
		if c.Effect != label {
			continue
		}
		if len(c.LocalVars) > 0 && !c.Op.Abort && c.Op.Native == nil {
			elts = append(elts, &goast.KeyValueExpr{Key: ident("Op_" + linkName(c.Op.Name)), Value: g.polymorphicHandlerClause(e, c, mode, state)})
			continue
		}
		params := make([]paramSpec, 0, len(c.Params))
		for j, p := range c.Params {
			if p == "()" || p == "_" {
				p = "_"
			} else {
				p = mangleValue(p)
			}
			if g.isUnit(c.ParamTypes[j]) {
				continue
			}
			params = append(params, paramSpec{name: p, typ: g.goType(c.ParamTypes[j])})
		}
		results := &goast.FieldList{}
		if mode == types.Exit {
			results = &goast.FieldList{List: []*goast.Field{{Type: g.outcomeType(c.ResultType)}}}
		} else if !g.isUnit(c.Op.ResultType) {
			results = &goast.FieldList{List: []*goast.Field{{Type: g.goType(c.ResultType)}}}
		}
		ft := &goast.FuncType{Params: paramFields(params), Results: results}
		var clausePrefix []goast.Stmt
		var stateType types.Type
		if e.State != nil {
			stateType = e.State.Ty
			if core.Mentions(c.Body, e.State.Name) {
				clausePrefix = append(clausePrefix,
					varDeclStmt(mangleValue(e.State.Name), g.goType(e.State.Ty), state.read()),
					assignBlank(ident(mangleValue(e.State.Name))))
			}
		}
		oldControl, oldResult := g.control, g.resultType
		g.control, g.resultType = mode, c.ResultType
		clauseBody := g.resumeStmtsFor(c.Body, c.ResumeID, g.isUnit(c.Op.ResultType), state, stateType)
		g.control, g.resultType = oldControl, oldResult
		fn := &goast.FuncLit{Type: ft, Body: &goast.BlockStmt{List: append(clausePrefix, clauseBody...)}}
		elts = append(elts, &goast.KeyValueExpr{Key: ident("Op_" + linkName(c.Op.Name)), Value: fn})
	}
	st := g.effectTypeMode(e.Effects[label], mode)
	return st, &goast.UnaryExpr{Op: gotoken.AND, X: &goast.CompositeLit{Type: st.(*goast.StarExpr).X, Elts: elts}}
}

func (g *gen) polymorphicHandlerClause(e *core.Handle, c core.HandlerClause, mode types.Transport, state *handlerState) goast.Expr {
	oldNames, oldDescriptors := g.tyParamNames, g.polyDescriptorNames
	g.tyParamNames = maps.Clone(oldNames)
	g.polyDescriptorNames = maps.Clone(oldDescriptors)
	if g.tyParamNames == nil {
		g.tyParamNames = make(map[int]string)
	}
	if g.polyDescriptorNames == nil {
		g.polyDescriptorNames = make(map[int]string)
	}
	defer func() { g.tyParamNames, g.polyDescriptorNames = oldNames, oldDescriptors }()
	var prefix []goast.Stmt
	for i, v := range c.LocalVars {
		g.tyParamNames[v.ID] = "any"
		name := fmt.Sprintf("t_poly_type%d", g.tmp)
		g.tmp++
		g.polyDescriptorNames[v.ID] = name
		prefix = append(prefix, varDeclStmt(name, g.descriptorType(), &goast.IndexExpr{X: selector("t_request", "Types"), Index: intLit(int64(i))}))
	}
	for i, p := range c.Params {
		if p == "_" || p == "()" {
			continue
		}
		name := mangleValue(p)
		value := &goast.IndexExpr{X: selector("t_request", "Args"), Index: intLit(int64(i))}
		var decoded goast.Expr = value
		if _, isAny := c.ParamTypes[i].(*types.TVar); !isAny {
			decoded = &goast.TypeAssertExpr{X: value, Type: g.goType(c.ParamTypes[i])}
		}
		prefix = append(prefix, varDeclStmt(name, g.goType(c.ParamTypes[i]), decoded))
		if !core.Mentions(c.Body, p) {
			prefix = append(prefix, assignBlank(ident(name)))
		}
	}
	if e.State != nil && core.Mentions(c.Body, e.State.Name) {
		prefix = append(prefix, varDeclStmt(mangleValue(e.State.Name), g.goType(e.State.Ty), state.read()), assignBlank(ident(mangleValue(e.State.Name))))
	}
	oldControl, oldResult := g.control, g.resultType
	g.control, g.resultType = mode, c.ResultType
	body := g.resumeStmtsFor(c.Body, c.ResumeID, g.isUnit(c.Op.ResultType), state, func() types.Type {
		if e.State != nil {
			return e.State.Ty
		}
		return nil
	}())
	g.control, g.resultType = oldControl, oldResult
	resultType := g.goType(c.ResultType)
	descriptor := g.typeDescriptor(c.ResultType)
	if mode == types.Exit {
		outcome := callExpr(funcLit(g.outcomeType(c.ResultType), body))
		prefix = append(prefix, returnStmt(callExpr(selector("fangort", "PolyOutcome"), outcome, descriptor)))
	} else if g.isUnit(c.Op.ResultType) {
		prefix = append(prefix, exprStmt(callExpr(funcLit(nil, body))))
		prefix = append(prefix, returnStmt(&goast.CompositeLit{Type: selector("fangort", "PolyReply"), Elts: []goast.Expr{
			&goast.KeyValueExpr{Key: ident("Type"), Value: descriptor},
			&goast.KeyValueExpr{Key: ident("Value"), Value: g.unitValue()},
		}}))
	} else {
		value := callExpr(funcLit(resultType, body))
		prefix = append(prefix, returnStmt(&goast.CompositeLit{Type: selector("fangort", "PolyReply"), Elts: []goast.Expr{
			&goast.KeyValueExpr{Key: ident("Type"), Value: descriptor},
			&goast.KeyValueExpr{Key: ident("Value"), Value: value},
		}}))
	}
	result := goast.Expr(selector("fangort", "PolyReply"))
	if mode == types.Exit {
		result = indexExpr(selector("fangort", "Outcome"), []goast.Expr{result})
	}
	return funcLitParams([]paramSpec{{name: "t_request", typ: selector("fangort", "PolyRequest")}}, result, prefix)
}

func (g *gen) resumeStmtsFor(e core.Expr, owner types.ResumeID, unitResult bool, state *handlerState, stateType types.Type) []goast.Stmt {
	switch e := e.(type) {
	case *core.ControlExit:
		if g.control != types.Exit {
			panic("codegen: abort terminal in a Direct resumptive clause")
		}
		// The source terminal has the handler subject's result type, but this
		// callback returns the operation's result. Abort has no normal value.
		terminal := *e
		terminal.Ty = g.resultType
		return []goast.Stmt{returnStmt(g.expr(&terminal, 0))}
	case *core.ResumeTail:
		if e.Owner != owner {
			panic("codegen: ResumeTail owner does not match handler clause")
		}
		if e.NextState != nil && !state.unchanged(e.NextState) {
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
				state.write(ident(nextName)))
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
		return append(g.letBindingStmts(e), g.resumeStmtsFor(e.Body, owner, unitResult, state, stateType)...)
	case *core.Seq:
		return append(g.stmts(e.First), g.resumeStmtsFor(e.Then, owner, unitResult, state, stateType)...)
	case *core.If:
		return []goast.Stmt{ifStmt(g.expr(e.Cond, 0), g.resumeStmtsFor(e.Then, owner, unitResult, state, stateType), g.resumeStmtsFor(e.Else, owner, unitResult, state, stateType))}
	case *core.Case:
		return g.caseStmts(e, func(x core.Expr) []goast.Stmt { return g.resumeStmtsFor(x, owner, unitResult, state, stateType) })
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

	}
	return &goast.StarExpr{X: indexExpr(g.qualified(symbolOwner(e.Name), name), g.goTypes(e.Args))}
}

// evidenceArg widens a Direct evidence record to its Exit ABI family when an
// enclosing aborting call needs Outcome-returning operation callbacks. The
// reverse conversion is intentionally absent.
func (g *gen) currentEvidenceMode(unique types.EffectKey) types.Transport {
	stack := g.evidenceModes[unique]
	if len(stack) == 0 {
		return types.Direct
	}
	return stack[len(stack)-1]
}

func (g *gen) evidenceArg(ev core.EffectInstance, value goast.Expr, actual, want types.Transport) goast.Expr {
	if actual == want {
		return value
	}
	effect := g.effects[ev.Unique]
	abort := effect != nil && len(effect.Ops) > 0 && effect.Ops[0].Abort
	if actual == types.Direct && want == types.Exit {
		return &goast.SelectorExpr{X: value, Sel: ident("Exit")}
	}
	if abort && want == types.Direct {
		return &goast.SelectorExpr{X: value, Sel: ident("Direct")}
	}
	return value
}

// rawEvidenceAdapter constructs the other transport once, before publishing
// the immutable activation family. Ordinary calls only select its pointer.
func (g *gen) rawEvidenceAdapter(ev core.EffectInstance, value goast.Expr, actual, want types.Transport) goast.Expr {
	if eff := g.effects[ev.Unique]; actual != want && eff != nil && len(eff.Ops) > 0 && eff.Ops[0].Abort {
		desired := ev
		desired.Control = types.Control{Transport: want}
		return &goast.UnaryExpr{Op: gotoken.AND, X: &goast.CompositeLit{Type: g.effectTypeMode(desired, want).(*goast.StarExpr).X, Elts: []goast.Expr{&goast.KeyValueExpr{Key: ident("Origin"), Value: &goast.SelectorExpr{X: value, Sel: ident("Origin")}}, &goast.KeyValueExpr{Key: ident("Target"), Value: &goast.SelectorExpr{X: value, Sel: ident("Target")}}}}}
	}
	if actual == want || want == types.Direct {
		return value
	}
	eff := g.effects[ev.Unique]
	if eff == nil {
		return value
	}

	if want != types.Exit || actual == types.Exit || (len(eff.Ops) > 0 && eff.Ops[0].Abort) {
		return value
	}
	desired := ev
	desired.Control = types.Control{Transport: types.Exit}
	elts := []goast.Expr{&goast.KeyValueExpr{Key: ident("Origin"), Value: &goast.SelectorExpr{X: value, Sel: ident("Origin")}}}
	sub := make(map[int]types.Type, len(eff.Params))
	for i, p := range eff.Params {
		if i < len(ev.Args) {
			sub[p.ID] = ev.Args[i]
		}
	}
	for _, op := range eff.Ops {
		if len(op.LocalVars) > 0 && op.Native == nil {
			request := ident("t_request")
			call := callExpr(&goast.SelectorExpr{X: value, Sel: ident("Op_" + linkName(op.Name))}, request)
			wrapped := callExpr(indexExpr(selector("fangort", "Normal"), []goast.Expr{selector("fangort", "PolyReply")}), call)
			fn := funcLitParams([]paramSpec{{name: "t_request", typ: selector("fangort", "PolyRequest")}},
				indexExpr(selector("fangort", "Outcome"), []goast.Expr{selector("fangort", "PolyReply")}),
				[]goast.Stmt{returnStmt(wrapped)})
			elts = append(elts, &goast.KeyValueExpr{Key: ident("Op_" + linkName(op.Name)), Value: fn})
			continue
		}
		var params []paramSpec
		var args []goast.Expr
		for i, raw := range op.RuntimeParamTypes() {
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
	return &goast.UnaryExpr{Op: gotoken.AND, X: &goast.CompositeLit{Type: g.effectType(desired).(*goast.StarExpr).X, Elts: elts}}
}

func (g *gen) effectDecls(effects []*types.EffectInfo) []goast.Decl {
	var out []goast.Decl
	for _, eff := range effects {
		if types.SurfaceName(eff.Name) == "IO" {
			continue
		}
		modes := []types.Transport{types.Direct, types.Exit}
		for _, mode := range modes {
			oldNames, oldControl, oldABI := g.tyParamNames, g.control, g.abi
			g.tyParamNames, g.control, g.abi = map[int]string{}, mode, mode
			var fields []*goast.Field
			g.usesFangort = true
			fields = append(fields, &goast.Field{Names: []*goast.Ident{ident("Origin")}, Type: &goast.StarExpr{X: selector("fangort", "EvidenceOrigin")}},
				&goast.Field{Names: []*goast.Ident{ident("Binding")}, Type: &goast.StarExpr{X: selector("fangort", "EvidenceBinding")}})
			otherName := "Eff_" + linkName(eff.Name)
			otherMember := "Direct"
			if mode == types.Direct {
				otherName += "_exit"
				otherMember = "Exit"
			}
			var otherArgs []goast.Expr
			for i := range eff.Params {
				otherArgs = append(otherArgs, ident(fmt.Sprintf("E%d", i)))
			}
			fields = append(fields, &goast.Field{Names: []*goast.Ident{ident(otherMember)}, Type: &goast.StarExpr{X: indexExpr(ident(otherName), otherArgs)}})
			for i, p := range eff.Params {
				g.tyParamNames[p.ID] = fmt.Sprintf("E%d", i)
			}
			// Clause-local variables have a uniform Go representation. The
			// request/reply ABI carries their source type descriptors separately.
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
				if len(op.LocalVars) > 0 && op.Native == nil {
					result := goast.Expr(selector("fangort", "PolyReply"))
					if mode == types.Exit {
						result = indexExpr(selector("fangort", "Outcome"), []goast.Expr{result})
					}
					fields = append(fields, &goast.Field{Names: []*goast.Ident{ident("Op_" + linkName(op.Name))}, Type: &goast.FuncType{
						Params:  &goast.FieldList{List: []*goast.Field{{Type: selector("fangort", "PolyRequest")}}},
						Results: &goast.FieldList{List: []*goast.Field{{Type: result}}},
					}})
					continue
				}
				ps := make([]paramSpec, 0, len(op.RuntimeParamTypes()))
				for _, t := range op.RuntimeParamTypes() {
					if g.isUnit(t) {
						continue
					}
					ps = append(ps, paramSpec{typ: g.goType(t)})
				}
				results := &goast.FieldList{}

				if mode == types.Exit {
					results = &goast.FieldList{List: []*goast.Field{{Type: g.outcomeType(op.ResultType)}}}
				} else if !g.isUnit(op.ResultType) {
					results = &goast.FieldList{List: []*goast.Field{{Type: g.goType(op.ResultType)}}}
				}
				fields = append(fields, &goast.Field{Names: []*goast.Ident{ident("Op_" + linkName(op.Name))}, Type: &goast.FuncType{Params: paramFields(ps), Results: results}})
			}
			name := "Eff_" + linkName(eff.Name)
			if mode == types.Exit {
				name += "_exit"
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

// controlIIFE emits a control-flow expression in a typed closure. Exit-mode
// leaves return Outcome at the expression's own result type, not the enclosing
// worker's type; this matters for an effectful If, Case, or Seq used as a let
// right-hand side.
func (g *gen) controlIIFE(e core.Expr, body func() []goast.Stmt) goast.Expr {
	oldControl, oldResult := g.control, g.resultType
	exits := g.control == types.Exit && core.ExprControl(e).Resolve(g.control) == types.Exit
	if g.control == types.Exit && !exits {
		g.control = types.Direct
	}
	g.resultType = e.Type()
	stmts := body()
	g.control, g.resultType = oldControl, oldResult
	result := g.goType(e.Type())
	if exits {
		result = g.outcomeType(e.Type())
	}
	return callExpr(funcLit(result, stmts))
}

// letIIFE collapses a Let chain into one immediately-invoked closure:
// `func() T { var v_r float64 = …; …; return result }()`. The expression-
// context fallback; statement contexts (main's body, and function bodies
// emit the bindings as plain Go statements instead.
func (g *gen) letIIFE(e *core.Let) goast.Expr {
	if core.ExprControl(e).Resolve(g.control) == types.Exit {
		oldResult := g.resultType
		g.resultType = e.Ty
		defer func() { g.resultType = oldResult }()
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
// rest of the chain never mentions them (Go rejects unused locals; Fango
// bindings still evaluate eagerly).
func (g *gen) letBindingStmts(let *core.Let) []goast.Stmt {
	if g.rememberForwarder(let) {
		return nil
	}
	if let.Rec {
		// A Go local is not in scope inside its own initializer:
		// declare, then assign — the standard recursive-closure idiom.
		name := mangleValue(let.Name)
		return []goast.Stmt{
			varDeclNoValue(name, g.goType(let.Rhs.Type())),
			assignStmt(name, g.expr(let.Rhs, 0)),
		}
	}
	if stmts, ok := g.immediateUnitApplication(let.Rhs, g.stmts); ok {
		return stmts
	}
	if g.control == types.Exit && core.ExprControl(let.Rhs).Resolve(g.control) == types.Exit {
		outcome := fmt.Sprintf("t_outcome%d", g.tmp)
		g.tmp++
		propagate := returnStmt(g.propagateOutcome(g.resultType, selector(outcome, "Exit")))
		var stmts []goast.Stmt
		switch let.Rhs.(type) {
		case *core.If, *core.Case, *core.Let, *core.Seq:
			stmts = append([]goast.Stmt{varDeclNoValue(outcome, g.outcomeType(let.Rhs.Type()))}, g.assignOutcomeStmts(let.Rhs, outcome)...)
		default:
			stmts = []goast.Stmt{varDeclStmt(outcome, g.outcomeType(let.Rhs.Type()), g.expr(let.Rhs, 0))}
		}
		stmts = append(stmts, &goast.IfStmt{Cond: &goast.BinaryExpr{X: selector(outcome, "Exit"), Op: gotoken.NEQ, Y: ident("nil")}, Body: &goast.BlockStmt{List: []goast.Stmt{propagate}}})
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
	if stmts, ok := g.immediateUnitApplication(e, g.stmts); ok {
		return stmts
	}
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
		if e.Op.Native != nil && len(g.evidence[e.Effect.Key()]) == 0 {
			return g.nativeStmts(&core.NativeCall{Name: e.Op.Native.Name, Module: e.Op.Native.Module, Args: e.Args, Ty: e.Ty})
		}
		if g.isUnit(e.Op.ResultType) {
			stack := g.evidence[e.Effect.Key()]
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
				call := callExpr(g.operationCallee(stack[len(stack)-1], "Op_"+linkName(e.Op.Name)), args...)
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

// mangleValue maps a Fango value name into the generated package's `v_`
// namespace (constructors use C_, types T_). Elaboration temporaries start
// with `_` — unlexable as Fango identifiers — and land in a disjoint `t`
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

// linkName maps a canonical Fango symbol to a Go identifier. Module
// separators and operator characters are the only characters a Fango symbol
// can hold that Go cannot.
//
// The substitution is injective against ordinary names: identifier
// characters and operator characters are disjoint sets, a Fango name may
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

func (g *gen) directLambdaMember(e *core.Lambda, mode types.Transport) goast.Expr {
	oldCallbacks := g.flatCallbacks
	g.flatCallbacks = maps.Clone(oldCallbacks)
	delete(g.flatCallbacks, e.Param)
	defer func() { g.flatCallbacks = oldCallbacks }()
	oldForwarders := g.forwarders
	g.forwarders = maps.Clone(oldForwarders)
	delete(g.forwarders, e.Param)
	defer func() { g.forwarders = oldForwarders }()
	fn := e.Ty.(*types.TFun)
	oldControl, oldResult := g.control, g.resultType
	g.control, g.resultType = mode, fn.Ret
	params := make([]paramSpec, 0, len(fn.Eff.Labels)+1)
	var pushed []types.EffectKey
	evidenceNames := map[string]int{}
	for _, l := range types.SortedRow(fn.Eff).Labels {
		if !types.RuntimeEvidenceEffect(l) {
			continue
		}
		name := g.evidenceName(l.Name)
		if n := evidenceNames[name]; n > 0 {
			name = fmt.Sprintf("%s_%d", name, n)
		}
		evidenceNames[g.evidenceName(l.Name)]++
		inst := core.EffectInstance{Unique: l.Unique, Name: l.Name, Args: l.Args, Control: types.Control{Transport: mode}}
		params = append(params, paramSpec{name: name, typ: g.effectType(inst)})
		g.evidence[types.EffectLabelKey(l)] = append(g.evidence[types.EffectLabelKey(l)], ident(name))
		g.evidenceModes[types.EffectLabelKey(l)] = append(g.evidenceModes[types.EffectLabelKey(l)], mode)
		pushed = append(pushed, types.EffectLabelKey(l))
	}
	if e.RowParam != 0 {
		name := fmt.Sprintf("rowParam%d", g.tmp)
		g.tmp++
		params = append(params, paramSpec{name: name, typ: g.rowType()})
		defer g.pushRow(e.RowParam, ident(name))()
		defer g.bindDeferredEffects(e.RowEffects, ident(name), mode)()
	}
	params = append(params, paramSpec{name: func() string {
		if e.Param == "_" {
			return "_"
		}
		return mangleValue(e.Param)
	}(), typ: g.goType(fn.Arg)})
	oldParamTy, hadParamTy := g.caseVarTys[e.Param]
	g.caseVarTys[e.Param] = fn.Arg
	finishRows := g.prepareRows()
	body := finishRows(g.retStmts(e.Body))
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
}
