package check

import (
	"fmt"
	"maps"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/waj/fango/internal/compileevent"
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/elaborate"
	"github.com/waj/fango/internal/infer"
	"github.com/waj/fango/internal/modules"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/staging"
	"github.com/waj/fango/internal/types"
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

	summaries    map[string]moduleSummary
	sources      map[string]*source.File
	installed    []core.Def
	states       []*infer.ModuleState
	objects      []*ModuleObject
	infos        []infer.DeclInfo
	FreshSources map[string]bool

	// publishing holds artifacts being encoded and stored while later modules
	// are checked. An object is complete once its module is installed, and
	// nothing mutates it after, so writing it can overlap the rest of the
	// group; the group waits for every write before it returns.
	publishing []*publication
	published  sync.WaitGroup

	// reading holds the group's slots being read and decoded ahead of the
	// modules that consult them.
	reading map[string]*reading

	// summarized names the modules that have, or will have once their
	// summary job runs, an entry in summaries.
	summarized map[string]bool
	// summarizing is the queue of modules checked from source whose
	// fingerprints are computed in the background; settle folds the
	// finished ones into summaries.
	summarizing chan *summaryJob
	pending     []*summaryJob
	outstanding sync.WaitGroup
	publishMu   sync.Mutex
}

type reading struct {
	done   chan struct{}
	cached cachedObject
}

type publication struct {
	owner   string
	bytes   int
	elapsed time.Duration
}

// NewInstaller adopts an existing checker and staging session. cache may be
// nil, which compiles normally without reusing or publishing artifacts.
func NewInstaller(ck *infer.Checker, stage *staging.Session, cache ObjectCache, observe Observer) *Installer {
	return &Installer{ck: ck, stage: stage, cache: cache, observe: observe,
		summaries: map[string]moduleSummary{}, summarized: map[string]bool{}, sources: map[string]*source.File{}}
}

func (i *Installer) timed(stage, owner string, start time.Time) { i.observe.Timed(stage, owner, start) }

func (i *Installer) begin(stage, owner string) time.Time { return i.observe.Begin(stage, owner) }

// artifact reports a cache decision together with the bytes it moved.
func (i *Installer) artifact(stage, owner string, start time.Time, bytes int) {
	i.observe.Report(compileevent.Event{Stage: stage, Owner: owner, Duration: time.Since(start), Bytes: bytes})
}

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
	defer i.flush()
	i.readAhead(group)
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

// InstallCollect continues past source diagnostics in independent modules.
// Failed owners and their dependents are skipped, so missing declarations do
// not produce cascaded errors. The batch compiler uses Install instead.
func (i *Installer) InstallCollect(group []modules.ResolvedModule, fixityHash string) ([]core.Def, []diag.Error, error) {
	mono := i.ck.MonoValues
	i.ck.MonoValues = false
	defer func() { i.ck.MonoValues = mono }()
	for _, module := range group {
		if module.Source != nil {
			i.sources[module.Source.Name] = module.Source
		}
	}
	defer i.flush()
	i.readAhead(group)
	failed := map[string]bool{}
	var added []core.Def
	var diagnostics []diag.Error
	for _, module := range group {
		skip := false
		for _, dependency := range module.Dependencies {
			if failed[dependency] {
				skip = true
				break
			}
		}
		if skip {
			failed[module.Name] = true
			continue
		}
		restore := i.ck.Checkpoint()
		beforeInfos := len(i.infos)
		defs, errs, err := i.installOne(module, fixityHash)
		if len(errs) > 0 || err != nil {
			restore()
			i.infos = i.infos[:beforeInfos]
			if err != nil {
				return nil, diagnostics, err
			}
			failed[module.Name] = true
			diagnostics = append(diagnostics, errs...)
			continue
		}
		added = append(added, defs...)
	}
	return added, diagnostics, nil
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
	slot, hasSlot := objectSlot(module)
	// Whether this module can be summarized is known now; the summaries
	// themselves may still be computing for the modules just checked.
	summarizable := i.summarizable(module.Dependencies)
	cacheable := summarizable && hasSlot && i.cache != nil && (module.Source == nil || !i.FreshSources[module.Source.Name])
	if cacheable {
		lookupStart := time.Now()
		cached := i.readCached(slot)
		read := cached.read
		var object *ModuleObject
		hit := false
		// Only an artifact to compare needs the dependencies' summaries, so a
		// cold build checks on while they are computed.
		if cached.object != nil {
			i.settle()
			baseKey, _ := moduleBaseKey(module, fixityHash, i.summaries)
			object, hit = acceptCachedObject(cached, baseKey, module, i.summaries)
		}
		if hit {
			if err := InstallObject(i.ck, i.stage, object); err == nil {
				i.artifact("checked-cache-hit", owner, lookupStart, read)
				i.states = append(i.states, object.State)
				i.objects = append(i.objects, object)
				i.installed = append(i.installed, object.Runtime...)
				i.summaries[module.Name] = moduleSummary{Semantic: object.Semantic, ABI: object.ABI, Stage: object.StageFingerprint, Unfolding: object.Unfolding}
				i.summarized[module.Name] = true
				return object.Runtime, nil, nil
			}
		}
		i.artifact("checked-cache-miss", owner, lookupStart, read)
	}
	// Checking a module from source elaborates it against the installed stage
	// definitions of its dependencies, so every deferred section is needed
	// from here on. Nothing before this point can reach one.
	if err := i.stage.Force(); err != nil {
		return nil, nil, err
	}
	i.stage.BeginModule(module.Name, module.NativeModule)
	checkStart := i.begin("check", owner)
	checked, checkErrs := i.ck.CheckModule(module.Module, infer.ModuleOptions{Name: module.Name, Role: role, Entry: module.Entry})
	i.timed("check", owner, checkStart)
	if len(checkErrs) != 0 {
		return nil, checkErrs, nil
	}
	i.infos = append(i.infos, checked.Infos...)
	intrinsics := make([]string, 0, len(checked.State.Intrinsics))
	for name := range checked.State.Intrinsics {
		intrinsics = append(intrinsics, name)
	}
	sort.Strings(intrinsics)
	elaborateStart := i.begin("elaborate", owner)
	owned, elabErrs := elaborate.Increment(checked.Infos, checked.State.Instances, intrinsics, i.installed, i.ck)
	i.timed("elaborate", owner, elaborateStart)
	if len(elabErrs) != 0 {
		return nil, elabErrs, nil
	}
	state := i.ck.CompleteModuleState(checked.State)
	// Building this module's stage Core is a pass over its declarations in its
	// own right, and for a module with many derived instances it rivals
	// elaboration, so it answers for its own time rather than the caller's.
	// Increment has just discharged every definition it returned, and the
	// stage elaboration runs the same decl over the same declarations, so it
	// discharges only what Increment did not see.
	snapshotStart := i.begin("stage-snapshot", owner)
	stageObject, stageErrs := i.stage.Snapshot()
	i.timed("stage-snapshot", owner, snapshotStart)
	if len(stageErrs) != 0 {
		return nil, stageErrs, nil
	}
	if compatibilityErrs := infer.ValidateModuleStates(append(i.states, state)); len(compatibilityErrs) != 0 {
		return nil, compatibilityErrs, nil
	}
	lintStart := i.begin("semantic-lint", owner)
	// Increment discharged these definitions' obligations moments ago, on this
	// same slice, before any transform could touch it.
	lintErrs := elaborate.LintProgIn(owned, i.installed, i.ck)
	i.timed("semantic-lint", owner, lintStart)
	if len(lintErrs) != 0 {
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
	// A dependency outside the summarized graph leaves this owner, and every
	// later consumer of it, unsummarized rather than uncompilable.
	if i.summarizable(module.Dependencies) && i.summarizable(object.StageDependencies) {
		i.summarized[module.Name] = true
		i.summarize(&summaryJob{module: module, owner: owner, slot: slot, fixityHash: fixityHash, publish: cacheable,
			object: object, unfoldable: elaborate.Unfoldable(owned, i.ck),
			captures: maps.Clone(i.ck.CaptureSummaries), own: state.Captures, known: maps.Clone(i.summaries)})
	}
	i.states = append(i.states, state)
	i.objects = append(i.objects, object)
	i.installed = append(i.installed, owned...)
	return owned, nil, nil
}

// readAhead starts reading every slot the group may consult. Reading and
// decoding are independent of the graph; accepting an artifact and installing
// it stay in dependency order.
func (i *Installer) readAhead(group []modules.ResolvedModule) {
	if i.cache == nil {
		return
	}
	var slots []string
	for _, module := range group {
		slot, ok := objectSlot(module)
		if !ok || (module.Source != nil && i.FreshSources[module.Source.Name]) {
			continue
		}
		slots = append(slots, slot)
	}
	if len(slots) == 0 {
		return
	}
	i.reading = make(map[string]*reading, len(slots))
	next := make(chan *reading, len(slots))
	slotOf := make(map[*reading]string, len(slots))
	for _, slot := range slots {
		r := &reading{done: make(chan struct{})}
		i.reading[slot] = r
		slotOf[r] = slot
		next <- r
	}
	close(next)
	cache, sources := i.cache, maps.Clone(i.sources)
	for range min(runtime.GOMAXPROCS(0), len(slots)) {
		go func() {
			for r := range next {
				r.cached = readCachedObject(cache, slotOf[r], sources)
				close(r.done)
			}
		}()
	}
}

// readCached is a slot's artifact, read ahead if the group started it.
func (i *Installer) readCached(slot string) cachedObject {
	if r := i.reading[slot]; r != nil {
		delete(i.reading, slot)
		<-r.done
		return r.cached
	}
	return readCachedObject(i.cache, slot, i.sources)
}

// summaryJob fingerprints one module checked from source, then publishes it.
// It reads only the finished object and copies taken when it was queued.
type summaryJob struct {
	module        modules.ResolvedModule
	owner, slot   string
	fixityHash    string
	publish       bool
	object        *ModuleObject
	unfoldable    []core.Def
	captures, own map[string]types.CaptureSummary
	known         map[string]moduleSummary
	summary       moduleSummary
}

func (i *Installer) summarizable(names []string) bool {
	for _, name := range names {
		if !i.summarized[name] {
			return false
		}
	}
	return true
}

// summarize queues a job. One worker runs the jobs in order, so each sees the
// summaries of every module checked before it.
func (i *Installer) summarize(job *summaryJob) {
	if i.summarizing == nil {
		i.summarizing = make(chan *summaryJob, 64)
		go i.summarizeAll(i.summarizing)
	}
	i.pending = append(i.pending, job)
	i.outstanding.Add(1)
	i.summarizing <- job
}

func (i *Installer) summarizeAll(jobs <-chan *summaryJob) {
	known := map[string]moduleSummary{}
	for job := range jobs {
		for name, summary := range job.known {
			if _, ok := known[name]; !ok {
				known[name] = summary
			}
		}
		job.run(known)
		known[job.module.Name] = job.summary
		if job.publish {
			if base, ok := moduleBaseKey(job.module, job.fixityHash, known); ok {
				if record, ok := encodeRecord(base, job.object, known); ok {
					i.publish(job.owner, job.slot, job.object, record)
				}
			}
		}
		i.outstanding.Done()
	}
}

func (job *summaryJob) run(known map[string]moduleSummary) {
	object, module := job.object, job.module
	object.ScopeNames = foreignScopeNames(job.captures, job.own, object)
	// The fingerprints are independent reads of the finished object.
	var ownSemantic, ownABI, ownImplementation, ownStage, ownUnfolding string
	var fingerprinting sync.WaitGroup
	for _, compute := range []func(){
		func() { ownSemantic = semanticFingerprint(object) },
		func() { ownABI = abiFingerprint(object) },
		func() { ownImplementation = implementationFingerprint(object) },
		func() { ownStage = ownStageFingerprint(object, object.Stage) },
		func() { ownUnfolding = canonicalDigest(job.unfoldable, object) },
	} {
		fingerprinting.Add(1)
		go func() {
			defer fingerprinting.Done()
			compute()
		}()
	}
	fingerprinting.Wait()
	semanticDeps, _ := dependencyFingerprints(module.Dependencies, known, func(s moduleSummary) string { return s.Semantic })
	abiDeps, _ := dependencyFingerprints(module.Dependencies, known, func(s moduleSummary) string { return s.ABI })
	stageDeps, _ := dependencyFingerprints(object.StageDependencies, known, func(s moduleSummary) string { return s.Stage })
	unfoldingDeps, _ := dependencyFingerprints(module.Dependencies, known, func(s moduleSummary) string { return s.Unfolding })
	object.OwnSemantic = ownSemantic
	object.OwnABI = ownABI
	object.Semantic = combinedFingerprint("semantic", ownSemantic, semanticDeps)
	object.ABI = combinedFingerprint("abi", ownABI, abiDeps)
	object.StageImplementation = ownStage
	object.Implementation = ownImplementation
	object.StageFingerprint = combinedFingerprint("stage", ownStage, stageDeps)
	object.OwnUnfolding = ownUnfolding
	object.Unfolding = combinedFingerprint("unfolding", ownUnfolding, unfoldingDeps)
	job.summary = moduleSummary{Semantic: object.Semantic, ABI: object.ABI, Stage: object.StageFingerprint, Unfolding: object.Unfolding}
}

// settle waits for the queued summary jobs and records their summaries.
func (i *Installer) settle() {
	if len(i.pending) == 0 {
		return
	}
	i.outstanding.Wait()
	for _, job := range i.pending {
		i.summaries[job.module.Name] = job.summary
	}
	i.pending = nil
}

// publish encodes and stores object in the background.
func (i *Installer) publish(owner, slot string, object *ModuleObject, record []byte) {
	p := &publication{owner: owner}
	i.publishMu.Lock()
	i.publishing = append(i.publishing, p)
	i.publishMu.Unlock()
	i.published.Add(1)
	go func() {
		defer i.published.Done()
		start := time.Now()
		p.bytes = storeCachedObject(i.cache, slot, object, record)
		p.elapsed = time.Since(start)
	}()
}

// flush waits for the group's background work and reports the artifacts it
// stored in module order. Each store answers for its share of the time the
// group spent waiting, not for time it overlapped with checking, so stages
// still add up to the build.
func (i *Installer) flush() {
	// A group that stops early leaves slots it never consulted.
	for _, r := range i.reading {
		<-r.done
	}
	i.reading = nil
	i.settle()
	if i.summarizing != nil {
		close(i.summarizing)
		i.summarizing = nil
	}
	if len(i.publishing) == 0 {
		return
	}
	start := time.Now()
	i.published.Wait()
	waited := time.Since(start)
	var busy time.Duration
	for _, p := range i.publishing {
		busy += p.elapsed
	}
	for _, p := range i.publishing {
		share := time.Duration(0)
		if busy > 0 {
			share = time.Duration(float64(waited) * float64(p.elapsed) / float64(busy))
		}
		i.observe.Report(compileevent.Event{Stage: "checked-cache-store", Owner: p.owner, Duration: share, Bytes: p.bytes})
	}
	i.publishing = nil
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
