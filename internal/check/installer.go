package check

import (
	"fmt"
	"sort"
	"strings"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/elaborate"
	"github.com/waj/fango/internal/infer"
	"github.com/waj/fango/internal/modules"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/staging"
)

// Installer checks and installs modules into one checker, dependency first,
// reusing persistent checked objects. A batch entry graph installs its whole
// graph at once; a REPL session installs one prompt import increment at a
// time into the checker it keeps between inputs.
type Installer struct {
	ck      *infer.Checker
	stage   *staging.Session
	cache   ObjectCache
	observe Observer

	summaries map[string]moduleSummary
	sources   map[string]*source.File
	installed []core.Def
	states    []*infer.ModuleState
	objects   []*ModuleObject
	infos     []infer.DeclInfo
}

// NewInstaller adopts an existing checker and staging session. cache may be
// nil, which compiles normally without reusing or publishing artifacts.
func NewInstaller(ck *infer.Checker, stage *staging.Session, cache ObjectCache, observe Observer) *Installer {
	return &Installer{ck: ck, stage: stage, cache: cache, observe: observe,
		summaries: map[string]moduleSummary{}, sources: map[string]*source.File{}}
}

// Adopt records definitions already installed in the checker by another path,
// so elaboration and Core lint see the context their calls resolve against.
func (i *Installer) Adopt(defs []core.Def) {
	i.installed = append(i.installed, defs...)
}

func (i *Installer) event(stage, owner string) {
	if i.observe != nil {
		i.observe(stage, owner)
	}
}

// Installed is every definition this installer has taken in, in order.
func (i *Installer) Installed() []core.Def { return i.installed }

// Objects and States describe the modules installed so far.
func (i *Installer) Objects() []*ModuleObject     { return i.objects }
func (i *Installer) States() []*infer.ModuleState { return i.states }
func (i *Installer) Infos() []infer.DeclInfo      { return i.infos }

// Install takes in one dependency-ordered group of modules under the given
// effective fixity table. It returns the definitions the group added; the
// caller publishes them to any runtime of its own only after it accepts the
// whole group. A diagnostic is source-facing; err denotes a violated compiler
// invariant.
//
// The checker is mutated as each module is taken in, because a module is
// checked against the ones before it. A client that can reject the group
// restores its own checkpoint.
func (i *Installer) Install(group []modules.ResolvedModule, fixityHash string) ([]core.Def, []diag.Error, error) {
	// Module values are immutable, so they generalize as in a build. A
	// client whose own values are monomorphic — the REPL prompt and its memo
	// cells — keeps that rule for itself alone.
	mono := i.ck.MonoValues
	i.ck.MonoValues = false
	defer func() { i.ck.MonoValues = mono }()
	for _, module := range group {
		if module.Source != nil {
			i.sources[module.Source.Name] = module.Source
		}
	}
	var added []core.Def
	for _, module := range group {
		defs, diagnostics, err := i.installOne(module, fixityHash)
		if len(diagnostics) != 0 || err != nil {
			return nil, diagnostics, err
		}
		added = append(added, defs...)
	}
	return added, nil, nil
}

func (i *Installer) installOne(module modules.ResolvedModule, fixityHash string) ([]core.Def, []diag.Error, error) {
	templateStart := i.ck.Templates.Len()
	owner := module.Name
	if owner == "" {
		owner = "<entry>"
	}
	role := infer.DependencyModule
	if module.Role == modules.EntryRole {
		role = infer.EntryModule
	}
	baseKey, hasBase := moduleBaseKey(module, fixityHash, i.summaries)
	if hasBase && i.cache != nil {
		if object, hit := loadCachedObject(i.cache, baseKey, module, i.summaries, i.sources); hit {
			if err := InstallObject(i.ck, i.stage, object); err == nil {
				i.event("checked-cache-hit", owner)
				i.states = append(i.states, object.State)
				i.objects = append(i.objects, object)
				i.installed = append(i.installed, object.Runtime...)
				i.summaries[module.Name] = moduleSummary{Semantic: object.Semantic, ABI: object.ABI, Stage: object.StageFingerprint}
				return object.Runtime, nil, nil
			}
		}
		i.event("checked-cache-miss", owner)
	}
	i.stage.BeginModule(module.Name, module.NativeModule)
	i.event("check", owner)
	checked, checkErrs := i.ck.CheckModule(module.Module, infer.ModuleOptions{Name: module.Name, Role: role, Entry: module.Entry})
	if len(checkErrs) != 0 {
		return nil, checkErrs, nil
	}
	i.infos = append(i.infos, checked.Infos...)
	intrinsics := make([]string, 0, len(checked.State.Intrinsics))
	for name := range checked.State.Intrinsics {
		intrinsics = append(intrinsics, name)
	}
	sort.Strings(intrinsics)
	i.event("elaborate", owner)
	owned, elabErrs := elaborate.Increment(checked.Infos, checked.State.Instances, intrinsics, i.installed, i.ck)
	if len(elabErrs) != 0 {
		return nil, elabErrs, nil
	}
	state := i.ck.CompleteModuleState(checked.State)
	stageObject, stageErrs := i.stage.Snapshot()
	if len(stageErrs) != 0 {
		return nil, stageErrs, nil
	}
	if compatibilityErrs := infer.ValidateModuleStates(append(i.states, state)); len(compatibilityErrs) != 0 {
		return nil, compatibilityErrs, nil
	}
	i.event("semantic-lint", owner)
	if lintErrs := elaborate.LintProgIn(owned, i.installed, i.ck); len(lintErrs) != 0 {
		parts := make([]string, len(lintErrs))
		for n, err := range lintErrs {
			parts[n] = err.Error()
		}
		return nil, nil, fmt.Errorf("Core invariants violated in module %s:\n  %s", owner, strings.Join(parts, "\n  "))
	}
	checkStageDependencies := i.stage.StageDependencies()
	stageDependencies := mergeNames(checkStageDependencies, i.stage.StageReferences(stageObject.Defs, module.Name, module.NativeModule))
	object := &ModuleObject{State: state, Resolver: module.Interface, Nominals: nominalNames(i.ck), EffectNames: effectNames(i.ck),
		Runtime: append([]core.Def(nil), owned...), Stage: stageObject.Defs, StageGroups: stageObject.Groups,
		TemplateBase: templateStart, Templates: i.ck.Templates.Snapshot(templateStart),
		StageDependencies: stageDependencies, CheckStageDependencies: checkStageDependencies}
	ownSemantic, ownABI, ownStage, ownImplementation := ownFingerprints(object)
	semanticDeps, semanticOK := dependencyFingerprints(module.Dependencies, i.summaries, func(s moduleSummary) string { return s.Semantic })
	abiDeps, abiOK := dependencyFingerprints(module.Dependencies, i.summaries, func(s moduleSummary) string { return s.ABI })
	stageDeps, stageOK := dependencyFingerprints(object.StageDependencies, i.summaries, func(s moduleSummary) string { return s.Stage })
	// A dependency outside the summarized graph leaves this owner, and every
	// later consumer of it, unsummarized rather than uncompilable.
	if semanticOK && abiOK && stageOK {
		object.Semantic = combinedFingerprint("semantic", ownSemantic, semanticDeps)
		object.ABI = combinedFingerprint("abi", ownABI, abiDeps)
		object.StageImplementation = ownStage
		object.Implementation = ownImplementation
		object.StageFingerprint = combinedFingerprint("stage", ownStage, stageDeps)
		i.summaries[module.Name] = moduleSummary{Semantic: object.Semantic, ABI: object.ABI, Stage: object.StageFingerprint}
		if hasBase && i.cache != nil {
			publishCachedObject(i.cache, baseKey, object, i.summaries)
		}
	}
	i.states = append(i.states, state)
	i.objects = append(i.objects, object)
	i.installed = append(i.installed, owned...)
	return owned, nil, nil
}

func mergeNames(groups ...[]string) []string {
	seen := map[string]bool{}
	for _, group := range groups {
		for _, name := range group {
			seen[name] = true
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
