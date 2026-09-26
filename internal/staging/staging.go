// Package staging runs splices. It exists as its own package because
// evaluating a splice needs elaboration and the interpreter, which both sit
// above inference in the package graph, while the splice itself has to be
// expanded during inference. The checker holds the seam; this package fills
// it in.
package staging

import (
	"time"

	"context"
	"fmt"
	"github.com/waj/fango/internal/compileevent"
	"sort"
	"strings"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/elaborate"
	"github.com/waj/fango/internal/eval"
	"github.com/waj/fango/internal/infer"
	machineir "github.com/waj/fango/internal/machine"
	"github.com/waj/fango/internal/types"
)

// Install gives ck a compile-time evaluator. Both the batch pipeline and the
// REPL call it, so a deriver behaves the same at the prompt as in a build.
type Session struct{ ev *evaluator }

type Observer = compileevent.Observer

// Observe reports deferred stage sections as they are read, so a caller can
// tell reuse from work.
func (s *Session) Observe(observe Observer) { s.ev.observe = observe }

// deferred is one module's stage Core, recorded but not yet read. load also
// reports the encoded size of the section it read, for cache accounting.
type deferred struct {
	owner string
	load  func() ([]core.Def, []Group, int, error)
}

type Group struct {
	End    int
	Cutoff []infer.DeclRef
}

type SnapshotObject struct {
	Defs   []core.Def
	Groups []Group
}

func Install(ck *infer.Checker) *Session {
	ev := &evaluator{ck: ck, env: eval.NewEnv()}
	ev.env.Templates = ck.Templates
	ck.CompileTime = ev.run
	// A failed expansion rolls the checker back past declarations this
	// environment already holds, so the environment is rebuilt from the
	// restored completion log rather than left describing a program that no longer
	// exists.
	ck.CompileTimeRollback = func(checked, instances int) {
		if ev.installedDecls <= checked && ev.installedInstances <= instances {
			return // nothing this environment holds was discarded
		}
		ev.resetToBase()
	}
	return &Session{ev: ev}
}

// Defer records stage Core that has not been decoded yet. Completing a stage
// snapshot elaborates a module's declarations against the installed stage
// definitions of its dependencies, so a single module checked from source
// needs all of them; a compile whose modules all come from cache needs none.
func (s *Session) Defer(owner string, load func() ([]core.Def, []Group, int, error)) {
	s.ev.pending = append(s.ev.pending, deferred{owner: owner, load: load})
}

// Force installs every deferred section in the order it was recorded. Callers
// place it where no index into the definition list is live across it. A
// section that cannot be read is a violated compiler invariant, not a miss:
// the object it belongs to was already installed.
func (s *Session) Force() error { return s.ev.force() }

func (ev *evaluator) force() error {
	pending := ev.pending
	ev.pending = nil
	for _, section := range pending {
		start := time.Now()
		defs, groups, size, err := section.load()
		if err != nil {
			return fmt.Errorf("stage Core for module %s: %w", section.owner, err)
		}
		ev.observe.Report(compileevent.Event{Stage: "stage-section", Owner: section.owner, Duration: time.Since(start), Bytes: size})
		(&Session{ev: ev}).InstallCore(defs, groups)
	}
	return nil
}

// BeginModule resets compile-time dependency collection for one owner. Every
// splice and imported deriver contributes the complete executable Core closure
// it reaches; names stay symbolic in the cached Core itself. An owner answers
// to every name that denotes it, because a sidecar package need not share the
// module name.
func (s *Session) BeginModule(names ...string) {
	s.ev.currentOwner = ownerSet(names)
	s.ev.stageDependencies = map[string]bool{}
}

func ownerSet(names []string) map[string]bool {
	out := map[string]bool{}
	for _, name := range names {
		if name != "" {
			out[name] = true
		}
	}
	return out
}

func (s *Session) StageDependencies() []string {
	out := make([]string, 0, len(s.ev.stageDependencies))
	for owner := range s.ev.stageDependencies {
		out = append(out, owner)
	}
	sort.Strings(out)
	return out
}

// StageReferences returns the module owners reachable from defs through the
// currently installed symbolic stage Core. This closure contributes to the
// module's stage fingerprint even when checking the module did not execute it.
func (s *Session) StageReferences(defs []core.Def, names ...string) []string {
	owner := ownerSet(names)
	byName := make(map[string]*core.Def, len(s.ev.defs))
	for i := range s.ev.defs {
		byName[s.ev.defs[i].Name] = &s.ev.defs[i]
	}
	queue := make([]string, 0, len(defs))
	for i := range defs {
		queue = append(queue, defs[i].Name)
	}
	reached := map[string]bool{}
	owners := map[string]bool{}
	for len(queue) != 0 {
		name := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		if reached[name] {
			continue
		}
		reached[name] = true
		def := byName[name]
		if def == nil {
			continue
		}
		defOwner := definitionOwner(*def)
		if defOwner != "" && !owner[defOwner] {
			owners[defOwner] = true
		}
		core.Inspect(def.Body, func(e core.Expr) {
			switch e := e.(type) {
			case *core.VarRef:
				if !e.Local && byName[e.Name] != nil {
					queue = append(queue, e.Name)
				}
			case *core.NativeCall:
				if e.Module != "" && !owner[e.Module] {
					owners[e.Module] = true
				}
			}
		})
	}
	out := make([]string, 0, len(owners))
	for dependency := range owners {
		out = append(out, dependency)
	}
	sort.Strings(out)
	return out
}

// Snapshot completes and returns the stage Core added since the prior
// snapshot. It is declarative Core; evaluator closures and memo cells are not
// exposed to module objects.
//
// proven names the definitions the module's runtime elaboration has just
// discharged. The stage elaboration of the same declarations still solves
// their summaries and contracts but does not discharge them again; whatever
// exists only here — compile-time-only declarations above all — gets its one
// discharge now. The set is consulted by this call alone: a splice's sync
// runs before the runtime elaboration and proves everything it elaborates.
func (s *Session) Snapshot(proven map[string]bool) (SnapshotObject, []diag.Error) {
	beforeDefs, beforeGroups := len(s.ev.defs), len(s.ev.groups)
	if errs := s.ev.syncProven(proven); len(errs) != 0 {
		return SnapshotObject{}, errs
	}
	groups := append([]Group(nil), s.ev.groups[beforeGroups:]...)
	for i := range groups {
		groups[i].End -= beforeDefs
	}
	return SnapshotObject{Defs: append([]core.Def(nil), s.ev.defs[beforeDefs:]...), Groups: groups}, nil
}

// InstallCore installs validated cached stage Core without replaying DeclInfo
// AST through elaboration. Installed definitions form the rollback base for
// later speculative checking.
func (s *Session) InstallCore(defs []core.Def, groups []Group) {
	if len(defs) == 0 {
		return
	}
	s.ev.env.DefineProg(&core.Prog{ADTs: s.ev.ck.ADTOrder, Defs: defs, Natives: s.ev.ck.Natives})
	s.ev.defs = append(s.ev.defs, defs...)
	s.ev.baseDefs = append(s.ev.baseDefs, defs...)
	base := len(s.ev.groups)
	for _, group := range groups {
		group.End += len(s.ev.defs) - len(defs)
		s.ev.groups = append(s.ev.groups, group)
	}
	s.ev.baseGroups = append([]Group(nil), s.ev.groups[:base+len(groups)]...)
	s.ev.baseInstances = len(s.ev.ck.Instances)
	s.ev.installedInstances = s.ev.baseInstances
	if s.ev.baseIntrinsics == nil {
		s.ev.baseIntrinsics = map[string]bool{}
	}
	if s.ev.installedIntrinsics == nil {
		s.ev.installedIntrinsics = map[string]bool{}
	}
	for _, def := range defs {
		if _, intrinsic := s.ev.ck.Intrinsics[def.Name]; intrinsic {
			s.ev.baseIntrinsics[def.Name], s.ev.installedIntrinsics[def.Name] = true, true
		}
	}
}

type evaluator struct {
	ck  *infer.Checker
	env *eval.Env

	// installed counts what the compile-time environment already holds, so
	// each splice elaborates only the completed groups added since the last
	// one. A program with no splices elaborates nothing twice.
	installedDecls     int
	installedGroups    int
	installedInstances int

	// installedIntrinsics records whether this environment already holds the
	// compiler intrinsics. They have no declaration prefix to follow.
	installedIntrinsics map[string]bool
	defs                []core.Def
	baseDefs            []core.Def
	baseInstances       int
	baseIntrinsics      map[string]bool
	groups              []Group
	baseGroups          []Group
	currentOwner        map[string]bool
	stageDependencies   map[string]bool
	pending             []deferred
	observe             Observer
}

func (ev *evaluator) resetToBase() {
	ev.env = eval.NewEnv()
	ev.env.Templates = ev.ck.Templates
	ev.defs = append([]core.Def(nil), ev.baseDefs...)
	ev.groups = append([]Group(nil), ev.baseGroups...)
	if len(ev.baseDefs) != 0 {
		ev.env.DefineProg(&core.Prog{ADTs: ev.ck.ADTOrder, Defs: ev.baseDefs, Natives: ev.ck.Natives})
	}
	ev.installedDecls, ev.installedGroups, ev.installedInstances = 0, 0, ev.baseInstances
	ev.installedIntrinsics = map[string]bool{}
	for name := range ev.baseIntrinsics {
		ev.installedIntrinsics[name] = true
	}
}

func (ev *evaluator) run(operand ast.Expr) (any, []diag.Error) {
	// A prompt reaches the evaluator without going through module
	// installation, so the deferred sections are forced here as well.
	if err := ev.force(); err != nil {
		return nil, []diag.Error{infer.CompileTimeError(err, operand.Span())}
	}
	if errs := ev.sync(); len(errs) > 0 {
		return nil, errs
	}
	body, aux, errs := elaborate.ExprIn(operand, ev.defs, ev.ck)
	if len(errs) > 0 {
		return nil, errs
	}
	// Check the actual elaborated dependency closure as well: instance methods
	// and dictionary factories can hide a forward call from surface references.
	execution := ev.executionDefs(body, aux)
	for _, d := range execution {
		ev.recordStageOwner(definitionOwner(d))
		core.Inspect(d.Body, func(e core.Expr) {
			if call, ok := e.(*core.NativeCall); ok {
				ev.recordStageOwner(call.Module)
			}
		})
		if es := ev.ck.CheckStageReference(d.Name, operand.Span()); len(es) > 0 {
			return nil, es
		}
	}
	if ev.ck.Intrinsics[types.CoroutineWithName].Body != nil {
		defs := ev.executionDefs(body, aux)
		p := ev.program(defs)
		if captureErrs := core.InferCaptures(p, ev.ck.B); len(captureErrs) > 0 {
			return nil, []diag.Error{diag.Errorf(operand.Span(), "INTERNAL CAPTURE INVARIANT", "%v", captureErrs[0])}
		}
		mp, lowerErrs := machineir.LowerStage(p, ev.ck.B)
		if len(lowerErrs) > 0 {
			return nil, []diag.Error{diag.Errorf(operand.Span(), "INTERNAL MACHINE INVARIANT", "%v", lowerErrs[0])}
		}
		if err := ev.env.DefineMachineProg(mp); err != nil {
			return nil, []diag.Error{infer.CompileTimeError(err, operand.Span())}
		}
	}
	for i := range aux {
		ev.env.DefineWorker(&aux[i])
	}
	v, err := eval.EvalCompileTime(context.Background(), body, ev.env, eval.DefaultBudget)
	if err != nil {
		return nil, []diag.Error{infer.CompileTimeError(err, operand.Span())}
	}
	return v, nil
}

func definitionOwner(d core.Def) string {
	if d.Owner != "" {
		return d.Owner
	}
	if i := strings.LastIndexByte(d.Name, '.'); i >= 0 {
		return d.Name[:i]
	}
	return ""
}

func (ev *evaluator) recordStageOwner(owner string) {
	if owner != "" && !ev.currentOwner[owner] {
		if ev.stageDependencies == nil {
			ev.stageDependencies = map[string]bool{}
		}
		ev.stageDependencies[owner] = true
	}
}

// An in-progress deriving group can have a dictionary declaration whose
// methods are still being expanded. Such a dictionary cannot be called by the
// current operand. Check and lower the operand's dependency closure, retaining
// source order, rather than pretending the entire prefix is a finished module.
func (ev *evaluator) executionDefs(body core.Expr, aux []core.Def) []core.Def {
	all := append(append([]core.Def(nil), ev.defs...), aux...)
	root := core.Def{Name: "_stage_expression", Type: body.Type(), SourceType: body.Type(), Control: core.ExprControl(body), Body: body}
	all = append(all, root)
	byName := make(map[string]*core.Def, len(all))
	for i := range all {
		byName[all[i].Name] = &all[i]
	}
	reached := map[string]bool{}
	queue := []string{root.Name}
	for len(queue) > 0 {
		name := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		if reached[name] {
			continue
		}
		reached[name] = true
		if def := byName[name]; def != nil {
			core.Inspect(def.Body, func(e core.Expr) {
				if ref, ok := e.(*core.VarRef); ok && !ref.Local {
					queue = append(queue, ref.Name)
				}
			})
		}
	}
	var defs []core.Def
	for i := range all {
		if reached[all[i].Name] && byName[all[i].Name] == &all[i] {
			defs = append(defs, all[i])
		}
	}
	return defs
}

// sync elaborates newly completed groups together, so capture analysis and
// evaluator installation see every recursive member. Instance registration
// stays source-ordered and is bounded by the current splice's cutoff.
func (ev *evaluator) sync() []diag.Error { return ev.syncProven(nil) }

func (ev *evaluator) syncProven(proven map[string]bool) []diag.Error {
	var defs []core.Def
	var errs []diag.Error
	add := func(ds []core.Def, es []diag.Error) {
		defs = append(defs, ds...)
		errs = append(errs, es...)
	}
	missingIntrinsic := false
	for name := range ev.ck.Intrinsics {
		missingIntrinsic = missingIntrinsic || !ev.installedIntrinsics[name]
	}
	if missingIntrinsic {
		for _, d := range elaborate.IntrinsicDefs(ev.ck) {
			if !ev.installedIntrinsics[d.Name] {
				defs = append(defs, d)
			}
		}
	}
	nextInstances := ev.ck.StageInstanceLimit()
	if n := nextInstances; n > ev.installedInstances {
		add(elaborate.Instances(ev.ck.Instances[ev.installedInstances:], ev.ck))
	}
	nextDecls := len(ev.ck.Checked)
	nextGroups := len(ev.ck.CompletionGroups)
	var groupEnds []int
	var groupCutoffs [][]infer.DeclRef
	for i := ev.installedGroups; i < nextGroups; i++ {
		group := ev.ck.CompletionGroups[i]
		context := append(append([]core.Def(nil), ev.defs...), defs...)
		add(elaborate.DeclsProvenIn(group.Infos, context, proven, ev.ck))
		groupEnds = append(groupEnds, len(defs))
		groupCutoffs = append(groupCutoffs, group.Cutoff)
	}
	if len(errs) > 0 {
		return errs
	}
	ev.env.DefineProg(&core.Prog{ADTs: ev.ck.ADTOrder, Defs: defs, Natives: ev.ck.Natives})
	if ev.installedIntrinsics == nil {
		ev.installedIntrinsics = map[string]bool{}
	}
	for name := range ev.ck.Intrinsics {
		ev.installedIntrinsics[name] = true
	}
	base := len(ev.defs)
	ev.defs = append(ev.defs, defs...)
	for i, end := range groupEnds {
		ev.groups = append(ev.groups, Group{End: base + end, Cutoff: append([]infer.DeclRef(nil), groupCutoffs[i]...)})
	}
	if len(defs) != 0 && len(groupEnds) == 0 {
		cutoff := make([]infer.DeclRef, len(ev.ck.Instances))
		for i, in := range ev.ck.Instances {
			cutoff[i] = in.Ref
		}
		ev.groups = append(ev.groups, Group{End: len(ev.defs), Cutoff: cutoff})
	}
	ev.installedDecls, ev.installedGroups, ev.installedInstances = nextDecls, nextGroups, max(ev.installedInstances, nextInstances)
	return nil
}

func (ev *evaluator) program(defs []core.Def) *core.Prog {
	effects := make([]*types.EffectInfo, 0, len(ev.ck.EffectsByUnique))
	for _, effect := range ev.ck.EffectsByUnique {
		effects = append(effects, effect)
	}
	intrinsics := make(map[string]bool, len(ev.ck.Intrinsics))
	for name := range ev.ck.Intrinsics {
		intrinsics[name] = true
	}
	return &core.Prog{ADTs: ev.ck.ADTOrder, Effects: effects, Defs: defs, Natives: ev.ck.Natives, Intrinsics: intrinsics, ObserveFlow: ev.ck.ObserveFlow}
}
