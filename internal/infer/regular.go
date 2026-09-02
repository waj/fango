package infer

import (
	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/types"
)

// checkRegularity rejects non-regular (nested) recursive ADTs (§7.2): within
// a recursive group, every occurrence of a group member in a constructor
// field must be applied to exactly the declaring type's own parameters, in
// order. `type T a = Node (T (Pair a a))` and mutual variants would demand
// unboundedly growing Go-generics instantiations (§8.4). The group is
// computed from the declared batch: an occurrence of C inside A is recursive
// iff C's fields reach back to A.
func (ck *Checker) checkRegularity(batch map[*ast.TypeDecl]*types.ADTInfo) []diag.Error {
	if len(batch) == 0 {
		return nil
	}
	// Direct edges among batch members, keyed by TCon unique.
	members := map[int]*types.ADTInfo{}
	for _, adt := range batch {
		members[adt.Con.Unique] = adt
	}
	edges := map[int]map[int]bool{}
	for _, adt := range batch {
		out := map[int]bool{}
		for _, c := range adt.Ctors {
			for _, f := range c.Fields {
				collectBatchCons(f, members, out)
			}
		}
		edges[adt.Con.Unique] = out
	}
	// reaches reports whether from can reach to through batch edges.
	reaches := func(from, to int) bool {
		seen := map[int]bool{}
		stack := []int{from}
		for len(stack) > 0 {
			u := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			for v := range edges[u] {
				if v == to {
					return true
				}
				if !seen[v] {
					seen[v] = true
					stack = append(stack, v)
				}
			}
		}
		return false
	}

	var errs []diag.Error
	for td, adt := range batch {
		for i, c := range adt.Ctors {
			// adt.Ctors and td.Ctors run parallel except when duplicate
			// constructors were skipped — indexes may then diverge, but a
			// batch with errors never elaborates, so span slippage is the
			// worst case.
			for j, f := range c.Fields {
				sp := td.NameSpan
				if i < len(td.Ctors) && j < len(td.Ctors[i].Args) {
					sp = td.Ctors[i].Args[j].Span()
				}
				errs = append(errs, ck.checkRegularOccurrences(f, adt, members, reaches, sp)...)
			}
		}
	}
	return errs
}

func collectBatchCons(t types.Type, members map[int]*types.ADTInfo, out map[int]bool) {
	switch t := t.(type) {
	case *types.TCon:
		if _, ok := members[t.Unique]; ok {
			out[t.Unique] = true
		}
		for _, a := range t.Args {
			collectBatchCons(a, members, out)
		}
	case *types.TFun:
		collectBatchCons(t.Arg, members, out)
		collectBatchCons(t.Ret, members, out)
	}
}

func (ck *Checker) checkRegularOccurrences(t types.Type, owner *types.ADTInfo,
	members map[int]*types.ADTInfo, reaches func(int, int) bool, sp source.Span) []diag.Error {
	var errs []diag.Error
	switch t := t.(type) {
	case *types.TCon:
		if occ, ok := members[t.Unique]; ok && reaches(occ.Con.Unique, owner.Con.Unique) {
			regular := len(t.Args) == len(owner.Params)
			if regular {
				for k, a := range t.Args {
					if !types.Equal(a, owner.Params[k]) {
						regular = false
						break
					}
				}
			}
			if !regular {
				errs = append(errs, diag.Errorf(sp, "NON-REGULAR TYPE",
					"`%s` recurses back to `%s`, so this occurrence must be applied to\nexactly `%s`'s own parameters, in order. Nested (non-regular)\nrecursive types are not supported.",
					occ.Con.Name, owner.Con.Name, owner.Con.Name))
				return errs // one error per occurrence site is enough
			}
		}
		for _, a := range t.Args {
			errs = append(errs, ck.checkRegularOccurrences(a, owner, members, reaches, sp)...)
		}
	case *types.TFun:
		errs = append(errs, ck.checkRegularOccurrences(t.Arg, owner, members, reaches, sp)...)
		errs = append(errs, ck.checkRegularOccurrences(t.Ret, owner, members, reaches, sp)...)
	}
	return errs
}
