package core

import (
	"fmt"
	"maps"
	"slices"

	"github.com/waj/fango/internal/types"
)

// Residual interpretations remain relational: an abstract exported row is
// unknown, while each checked invocation supplies the actual handler owners.
type flowRow struct {
	evidence map[int][]int
	unknown  bool
}

func emptyFlowEnv() flowEnv {
	return flowEnv{values: newFlowScope(), evidence: map[int][]int{}, types: map[int]types.Type{}, rows: map[types.CaptureVar]flowRow{}}
}

func joinFlowRow(a, b flowRow) flowRow {
	r := flowRow{evidence: maps.Clone(a.evidence), unknown: a.unknown || b.unknown}
	if r.evidence == nil {
		r.evidence = map[int][]int{}
	}
	for ev, owners := range b.evidence {
		ids := append(slices.Clone(r.evidence[ev]), owners...)
		slices.Sort(ids)
		r.evidence[ev] = slices.Compact(ids)
	}
	return r
}

func equalFlowRow(a, b flowRow) bool {
	return a.unknown == b.unknown && maps.EqualFunc(a.evidence, b.evidence, slices.Equal[[]int])
}

func invocationFlowRow(env flowEnv, arg *types.CaptureRow) flowEnv {
	inner := env.clone()
	inner.rows = maps.Clone(inner.rows)
	if inner.rows == nil {
		inner.rows = map[types.CaptureVar]flowRow{}
	}
	row := flowRow{evidence: map[int][]int{}}
	if arg != nil {
		if arg.From != 0 {
			row = joinFlowRow(row, env.rows[arg.From])
		}
		for _, ev := range arg.Effects {
			row.evidence[ev] = slices.Clone(env.evidence[ev])
		}
	}
	inner.rows[0] = row
	return inner
}

func (f *flowChecker) bindFlowRow(env *flowEnv, param types.CaptureVar, deferred []int, row flowRow) {
	if param == 0 {
		return
	}
	if env.rows == nil {
		env.rows = map[types.CaptureVar]flowRow{}
	}
	env.rows[param] = row
	for _, ev := range deferred {
		owners := slices.Clone(row.evidence[ev])
		if _, found := row.evidence[ev]; !found && !row.unknown {
			err := fmt.Errorf("def %s: residual invocation is missing deferred evidence %d", f.root, ev)
			f.errors[err.Error()] = err
		}
		if row.unknown {
			owners = append(owners, 0)
		}
		env.evidence[ev] = owners
	}
}

func flowRows(n *types.CaptureFlow) map[types.CaptureVar]bool {
	out := map[types.CaptureVar]bool{}
	var visit func(*types.CaptureFlow, map[types.CaptureVar]bool)
	visit = func(n *types.CaptureFlow, bound map[types.CaptureVar]bool) {
		if n == nil {
			return
		}
		if n.Row != nil && n.Row.From != 0 && !bound[n.Row.From] {
			out[n.Row.From] = true
		}
		if n.Kind == "lambda" {
			bound = maps.Clone(bound)
			bound[n.RowParam] = true
		}
		for _, child := range n.Children {
			visit(child, bound)
		}
		for _, clause := range n.Clauses {
			visit(clause.Body, bound)
		}
	}
	visit(n, map[types.CaptureVar]bool{})
	return out
}
