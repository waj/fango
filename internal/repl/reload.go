package repl

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"sort"
	"strings"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/check"
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/elaborate"
	"github.com/waj/fango/internal/infer"
	"github.com/waj/fango/internal/meta"
	"github.com/waj/fango/internal/modules"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/staging"
	"github.com/waj/fango/internal/types"
)

func (s *Session) reload() {
	fail := func(errs []diag.Error) {
		s.loading.clear()
		diag.Render(s.out, errs)
		fmt.Fprintln(s.out, "reload failed; session unchanged")
	}
	if len(s.levels) != 0 {
		fail([]diag.Error{{Title: "RELOAD WITH ACTIVE HANDLERS", Body: "End all handler levels with :end before reloading."}})
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	// Reload runs on the host even when runtime evaluation uses a worker.
	s.cancelMu.Lock()
	s.cancelInput = cancel
	s.cancelMu.Unlock()
	defer func() { cancel(); s.setCancel(nil); s.loading.clear() }()
	cancelled := func() bool {
		if err := ctx.Err(); err != nil {
			fail([]diag.Error{{Title: "RELOAD CANCELLED", Body: err.Error()}})
			return true
		}
		return false
	}
	graph, inc, changed, errs := s.graph.Refresh(ctx, s.imports, s.promptFixities)
	if len(errs) != 0 {
		fail(errs)
		return
	}
	if len(changed) == 0 {
		fmt.Fprintln(s.out, "nothing to reload")
		return
	}
	if !s.sameNativeSources(inc) {
		fail([]diag.Error{{Title: "RELOAD REQUIRES RESTART", Body: "Native sidecars changed. Restart the REPL to replace its native worker."}})
		return
	}
	s.loading.show(changed[0])
	prompt := graph.NewPrompt()
	var imports []ast.Import
	var removed []string
	for _, im := range s.imports {
		im, gone, es := prompt.Reimport(im)
		if len(es) != 0 {
			fail(es)
			return
		}
		imports, removed = append(imports, im), append(removed, gone...)
	}
	if errs := prompt.PreserveLocals(s.prompt); len(errs) != 0 {
		fail(errs)
		return
	}
	if cancelled() {
		return
	}
	ck := infer.NewChecker(s.ck.Sup, s.ck.B, infer.NewEnv())
	ck.RuntimeDebug = s.options.Debug
	ck.Fixity, ck.PreludeOwners = graph.Fixities(), s.ck.PreludeOwners
	stage := staging.Install(ck)
	stage.Observe(staging.Observer(s.options.Observe))
	installer := check.NewInstaller(ck, stage, s.options.objectCache(s.options.Root), s.options.Observe)
	var defs []core.Def
	for _, unit := range inc.Units {
		if cancelled() {
			return
		}
		s.loading.show(unit.Name)
		more, es, err := installer.Install([]modules.ResolvedModule{unit}, inc.FixityHash)
		if err != nil {
			fail([]diag.Error{{Title: "INTERNAL COMPILER ERROR", Body: err.Error()}})
			return
		}
		if len(es) != 0 {
			fail(es)
			return
		}
		defs = append(defs, more...)
	}
	currentStage, errs := stage.Freeze()
	if len(errs) != 0 {
		fail(errs)
		return
	}
	if cancelled() {
		return
	}
	oldStage, errs := s.stage.Freeze()
	if len(errs) != 0 {
		fail(errs)
		return
	}
	templateBase := s.ck.Templates.Len()
	ids, effects := check.NominalReuse(ck.Declarations(), s.ck.Declarations(), ck.Templates, s.ck.Templates)
	type candidate struct {
		State     *infer.ModuleState
		Defs      []core.Def
		Stage     staging.SnapshotObject
		Objects   []*check.ModuleObject
		Templates []*meta.Template
	}
	reconciled := infer.NewChecker(s.ck.Sup, s.ck.B, infer.NewEnv())
	reconciled.RuntimeDebug = s.options.Debug
	reconciled.EntryName = ""
	reconciled.IO = s.ck.IO
	objects := make([]*check.ModuleObject, len(installer.Objects()))
	for i, object := range installer.Objects() {
		copyObject := *object
		copyObject.Pending = nil
		objects[i] = &copyObject
	}
	next := check.CopyGraph(candidate{ck.Declarations(), defs, currentStage, objects, ck.Templates.Snapshot(0)}, func(v reflect.Value) {
		check.RemapNominal(v, ids, effects)
		if v.Type() == reflect.TypeFor[meta.TypeRepr]() {
			v.Addr().Interface().(*meta.TypeRepr).Schema = reconciled
		}
		switch v.Type() {
		case reflect.TypeFor[core.Quote](), reflect.TypeFor[meta.Code]():
			f := v.FieldByName("Template")
			f.SetInt(f.Int() + int64(templateBase))
		case reflect.TypeFor[check.ModuleObject]():
			f := v.FieldByName("TemplateBase")
			f.SetInt(f.Int() + int64(templateBase))
		}
	})
	// Rebuild the persistent checker from the reconciled declaration objects;
	// source-checking AST workspaces are not part of the installed contract.
	ck = reconciled
	ck.ReplaceDeclarations(next.State)
	ck.Fixity, ck.PreludeOwners = graph.Fixities(), s.ck.PreludeOwners
	ck.Templates.Append(s.ck.Templates.Snapshot(0))
	ck.Templates.Append(next.Templates)
	ck.InstanceImports = map[string]map[string]bool{"": {}}
	for _, object := range next.Objects {
		ck.InstanceImports[object.State.Name] = maps.Clone(object.State.Visibility)
		ck.InstanceImports[""][object.State.Name] = true
	}
	if !check.NativeContractsEqual(ck, s.ck) {
		fail([]diag.Error{{Title: "RELOAD REQUIRES RESTART", Body: "A native interface changed. Restart the REPL to replace its native worker."}})
		return
	}
	locals := s.preserveDeclarations(ck)
	if errs := infer.ValidateModuleStates([]*infer.ModuleState{next.State, locals}); len(errs) != 0 {
		fail(errs)
		return
	}
	mergePrompt(next.State, locals)
	ck.ReplaceDeclarations(next.State)
	ck.Sub = maps.Clone(s.ck.Sub)
	ck.LiftGen, ck.PatternGen = s.ck.LiftGen, s.ck.PatternGen
	ck.MonoValues = true
	for _, in := range locals.Instances {
		ck.InstanceImports[""][in.Owner] = true
	}
	ck.PromptEffects = func(row types.Row, at source.Span) []diag.Error { _, errs := s.grantLevels(row, at, nil); return errs }
	stage = staging.Install(ck)
	stage.Observe(staging.Observer(s.options.Observe))
	stage.InstallCore(next.Stage.Defs, next.Stage.Groups)
	// Earlier stage definitions keep earlier helpers and template indices.
	// Candidate templates were appended after the retained table, so reflected
	// historical attributes containing Code values stay valid as well.
	stageNames := maps.Clone(s.runtimeNames)
	for _, d := range oldStage.Defs {
		if d.Owner != "" && stageNames[d.Name] == "" && !strings.Contains(d.Name, "$repl") {
			stageNames[d.Name] = generationName(d.Name, s.reloadGeneration)
		}
	}
	archived := renameDefs(oldStage.Defs, stageNames)
	stage.InstallCore(archived, nil)
	installer = check.NewInstaller(ck, stage, s.options.objectCache(s.options.Root), s.options.Observe)
	installer.Adopt(next.Objects, inc.Units)
	if cancelled() {
		return
	}
	// Only source-owned Core is linted here. Historical prompt definitions
	// were validated in their own generation and retain that evidence.
	if es := elaborate.LintProgIn(next.Defs, nil, ck); len(es) != 0 {
		fail([]diag.Error{{Title: "INTERNAL COMPILER ERROR", Body: fmt.Sprint(es)}})
		return
	}
	retained := s.retainedBindings(changed)
	s.graph, s.prompt, s.ck, s.stage, s.modules = graph, prompt, ck, stage, installer
	s.imports, s.installed = imports, next.Defs
	s.reloadGeneration++
	affected := map[string]bool{}
	for _, name := range changed {
		affected[name] = true
	}
	for _, unit := range inc.Units {
		for _, dep := range unit.Dependencies {
			if affected[dep] {
				affected[unit.Name] = true
			}
		}
	}
	var refreshed []core.Def
	for _, object := range next.Objects {
		if affected[object.State.Name] {
			refreshed = append(refreshed, object.Runtime...)
		}
	}
	s.installModules(refreshed)
	s.loading.clear()
	fmt.Fprintf(s.out, "reloaded %s\n", strings.Join(changed, ", "))
	if len(removed) != 0 {
		sort.Strings(removed)
		fmt.Fprintf(s.out, "removed imported names: %s\n", strings.Join(removed, ", "))
	}
	if len(retained) != 0 {
		fmt.Fprintf(s.out, "retained earlier module bindings: %s\n", strings.Join(retained, ", "))
	}
}

func (s *Session) sameNativeSources(inc *modules.Increment) bool {
	old := map[string]string{}
	for _, n := range s.natives {
		old[n.Module] = string(n.Content)
	}
	next := map[string]string{}
	for _, n := range inc.Natives {
		next[n.Module] = string(n.Content)
	}
	for name, content := range next {
		if old[name] != content {
			return false
		}
	}
	return true
}

func mergeMap[T any](dst map[string]T, src map[string]T) { maps.Copy(dst, src) }

func mergePrompt(dst, src *infer.ModuleState) {
	mergeMap(dst.Schemes, src.Schemes)
	mergeMap(dst.Types, src.Types)
	mergeMap(dst.Aliases, src.Aliases)
	mergeMap(dst.Ctors, src.Ctors)
	mergeMap(dst.Effects, src.Effects)
	mergeMap(dst.Operations, src.Operations)
	mergeMap(dst.Classes, src.Classes)
	mergeMap(dst.Methods, src.Methods)
	mergeMap(dst.Workers, src.Workers)
	mergeMap(dst.Natives, src.Natives)
	mergeMap(dst.Intrinsics, src.Intrinsics)
	mergeMap(dst.Derivers, src.Derivers)
	mergeMap(dst.Captures, src.Captures)
	seen := map[int]bool{}
	for _, adt := range dst.ADTs {
		seen[adt.Con.Unique] = true
	}
	for _, adt := range src.ADTs {
		if !seen[adt.Con.Unique] {
			dst.ADTs = append(dst.ADTs, adt)
			seen[adt.Con.Unique] = true
		}
	}
	dst.Instances = append(dst.Instances, src.Instances...)
}

func (s *Session) retainedBindings(changed []string) []string {
	owners := map[string]bool{}
	for _, name := range changed {
		owners[name] = true
	}
	defs := map[string]core.Def{}
	for _, d := range s.env.Program().Defs {
		defs[d.Name] = d
	}
	maps.Copy(defs, s.promptDefs)
	var reaches func(string, map[string]bool) bool
	reaches = func(name string, seen map[string]bool) bool {
		if seen[name] {
			return false
		}
		seen[name] = true
		d, ok := defs[name]
		if !ok {
			// Constructors have declaration metadata rather than Core workers.
			if at := strings.LastIndexByte(name, '.'); at >= 0 {
				return owners[name[:at]]
			}
			return false
		}
		if owners[d.Owner] {
			return true
		}
		for _, ref := range s.promptReferences[name] {
			if reaches(ref, seen) {
				return true
			}
		}
		found := false
		core.Inspect(d.Body, func(e core.Expr) {
			if ref, ok := e.(*core.VarRef); ok && !ref.Local && reaches(ref.Name, seen) {
				found = true
			}
		})
		return found
	}
	var names []string
	for name := range s.promptDefs {
		if !strings.HasPrefix(name, "_") && reaches(name, map[string]bool{}) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}
