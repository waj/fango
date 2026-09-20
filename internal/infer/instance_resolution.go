package infer

import (
	"fmt"
	"sort"
	"strings"

	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/types"
)

// InstanceResolution distinguishes deferred predicates, failed searches, and
// successful evidence. A success with no Instance denotes given evidence.
type InstanceResolution struct {
	Instance *InstanceInfo
	Bindings map[int]types.Type
	Blocked  bool
	Error    *diag.Error
}

// ResolveInstance selects evidence in a frozen source-order environment.
// Context failure permits another declaration of the SAME head, never a less
// specific head. Cycles and ambiguity are errors rather than failed guards.
func (ck *Checker) ResolveInstance(p types.Pred, owner string, limit int, given []types.Pred) InstanceResolution {
	return ck.resolveInstance(p, owner, limit, given, nil)
}

func (ck *Checker) resolveInstance(p types.Pred, owner string, limit int, given []types.Pred, path []types.Pred) InstanceResolution {
	p.Ty = ck.Sub.Apply(p.Ty)
	for _, q := range given {
		if p.Class == q.Class && types.Equal(p.Ty, ck.Sub.Apply(q.Ty)) {
			return InstanceResolution{}
		}
	}
	if hasTypeVars(p.Ty) {
		return InstanceResolution{Blocked: true}
	}
	if err := ResolutionPathError(path, p, source.Span{}); err != nil {
		return InstanceResolution{Error: err}
	}
	candidates := ck.matchingInstances(p, owner, limit)
	var applicable []instanceCandidate
	var missing *diag.Error
	for _, candidate := range candidates {
		valid := true
		for _, q := range types.SubstPreds(candidate.in.Preds, candidate.bindings) {
			r := ck.resolveInstance(q, owner, limit, given, append(path, p))
			if r.Blocked {
				return r
			}
			if r.Error != nil {
				if r.Error.Title != "MISSING INSTANCE" {
					return r
				}
				if missing == nil {
					missing = r.Error
				}
				valid = false
				break
			}
		}
		if valid {
			applicable = append(applicable, candidate)
		}
	}
	if len(applicable) == 0 {
		if missing == nil {
			err := diag.Errorf(source.Span{}, "MISSING INSTANCE", "No instance provides `%s`.", types.ShowPred(p.Class, p.Ty))
			missing = &err
		}
		return InstanceResolution{Error: missing}
	}
	// Context inclusion is a partial order. First remove every dominated
	// candidate; mixing pairwise inclusion and declaration order in a sort
	// comparator would be non-transitive.
	var maximal []instanceCandidate
	for i, a := range applicable {
		dominated := false
		for j, b := range applicable {
			if i != j && contextIncludes(b.in.Head, b.in.Preds, a.in.Head, a.in.Preds) && !contextIncludes(a.in.Head, a.in.Preds, b.in.Head, b.in.Preds) {
				dominated = true
				break
			}
		}
		if !dominated {
			maximal = append(maximal, a)
		}
	}
	// Preserve declaration order, retaining only the latest per owner.
	var finalists []instanceCandidate
	for i, a := range maximal {
		later := false
		for _, b := range maximal[i+1:] {
			if a.in.Owner == b.in.Owner {
				later = true
				break
			}
		}
		if !later {
			finalists = append(finalists, a)
		}
	}
	if len(finalists) > 1 {
		err := diag.Errorf(source.Span{}, "AMBIGUOUS INSTANCE", "Multiple modules provide equally preferred instances for `%s`.", types.ShowPred(p.Class, p.Ty))
		for _, c := range finalists {
			location := c.in.Owner
			if c.in.Span.File != nil {
				pos := c.in.Span.StartPos()
				location = fmt.Sprintf("%s:%d:%d", c.in.Span.File.Name, pos.Line, pos.Col)
			}
			err.Notes = append(err.Notes, "Candidate for "+types.SurfaceName(c.in.Class.Name)+" at "+location+": "+types.ShowScheme(types.Scheme{Preds: c.in.Preds, Body: c.in.Head}))
		}
		return InstanceResolution{Error: &err}
	}
	return InstanceResolution{Instance: finalists[0].in, Bindings: finalists[0].bindings}
}

type instanceCandidate struct {
	in       *InstanceInfo
	bindings map[int]types.Type
}

// matchingInstances returns only the most-specific equivalent head group.
// Callers probing numeric eligibility may use rigid variables in p; that
// probe is never used as runtime evidence.
func (ck *Checker) matchingInstances(p types.Pred, owner string, limit int) []instanceCandidate {
	var best []instanceCandidate
	for _, in := range ck.Instances[:limit] {
		if in.Class.Name != p.Class {
			continue
		}
		if ck.InstanceImports != nil && in.Owner != owner && !ck.InstanceImports[owner][in.Owner] {
			continue
		}
		bindings := map[int]types.Type{}
		if matchHead(in.Head, p.Ty, bindings) != headYes {
			continue
		}
		candidate := instanceCandidate{in, bindings}
		if len(best) == 0 {
			best = append(best, candidate)
			continue
		}
		newGeq := headAtLeastAsSpecific(in.Head, best[0].in.Head)
		oldGeq := headAtLeastAsSpecific(best[0].in.Head, in.Head)
		if newGeq && !oldGeq {
			best = []instanceCandidate{candidate}
		} else if newGeq && oldGeq {
			best = append(best, candidate)
		}
	}
	return best
}

// instanceOverlap reports why an instance with this head and context may not
// coexist with old, or "" when they may. Registration and the whole-graph
// recheck share it so the two cannot drift apart.
func instanceOverlap(old *InstanceInfo, head types.Type, preds []types.Pred) string {
	oldGeq := headAtLeastAsSpecific(old.Head, head)
	newGeq := headAtLeastAsSpecific(head, old.Head)
	at := old.Span.StartPos()
	switch {
	case oldGeq && newGeq && contextIncludes(head, preds, old.Head, old.Preds) && contextIncludes(old.Head, old.Preds, head, preds):
		return fmt.Sprintf("duplicates the instance declared at %v.", at)
	case !oldGeq && !newGeq && headsUnify(old.Head, head):
		return fmt.Sprintf("overlaps the instance declared at %v; neither is more specific, so some uses would be ambiguous.", at)
	case oldGeq != newGeq && conHead(old.Head) && conHead(head):
		// One head specializes the other's arguments. Evidence for a
		// constructed type is composed from its arguments' evidence wherever
		// those arguments are not yet known, and composition cannot consult
		// such a specialization, so the two would disagree by call site.
		return fmt.Sprintf("specializes the instance declared at %v. An instance for a constructed type is composed from its arguments' instances, so a specialized argument would be ignored wherever that argument is not yet known.", at)
	}
	return ""
}

// contextIncludes aligns equivalent heads before comparing predicate sets.
// No implication through other instances participates in this ordering.
func contextIncludes(a types.Type, ap []types.Pred, b types.Type, bp []types.Pred) bool {
	m := map[int]types.Type{}
	if matchHead(b, a, m) != headYes {
		return false
	}
	for _, q := range types.SubstPreds(bp, m) {
		found := false
		for _, p := range ap {
			if p.Class == q.Class && types.Equal(p.Ty, q.Ty) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func canonicalContextKey(head types.Type, ps []types.Pred) string {
	vars := map[int]int{}
	canonicalTypeKey(head, vars)
	var keys []string
	for _, p := range ps {
		keys = append(keys, p.Class+"("+canonicalTypeKey(p.Ty, vars)+")")
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}
