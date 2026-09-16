package core

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/waj/fango/internal/types"
)

// Keep only definition-site bindings. Invocation binders must not accidentally
// capture a previous activation's arguments, evidence, or residual row.
func relevantFlowEnv(body *types.CaptureFlow, env flowEnv, bound []string, effects []int, row types.CaptureVar) flowEnv {
	out := emptyFlowEnv()
	out.types = maps.Clone(env.types)
	for name := range flowFree(body, bound) {
		if v, ok := env.values[name]; ok {
			out.values[name] = v
		}
	}
	used := flowEffects(body)
	for _, ev := range effects {
		delete(used, ev)
	}
	for ev := range used {
		out.evidence[ev] = env.evidence[ev]
	}
	for r := range flowRows(body) {
		if r != row {
			out.rows[r] = env.rows[r]
		}
	}
	return out
}

// Canonical traversal numbers preserve aliases and terminate cyclic closure and
// constructor graphs. Concrete owner IDs are never structurally renumbered.
// The key is recomputed against the mutable heap on lookup, so late field or
// closure-environment growth cannot leave a stale key in the sharing index.
func (f *flowChecker) flowFingerprint(env flowEnv) string {
	var b strings.Builder
	seen := map[int]int{}
	var value func(flowValue)
	var environment func(flowEnv)
	environment = func(e flowEnv) {
		b.WriteString("env{")
		for _, name := range slices.Sorted(maps.Keys(e.values)) {
			fmt.Fprintf(&b, "%q:", name)
			value(e.values[name])
		}
		for _, ev := range slices.Sorted(maps.Keys(e.evidence)) {
			if len(e.evidence[ev]) == 0 {
				continue
			}
			fmt.Fprintf(&b, "e%d:%v;", ev, e.evidence[ev])
		}
		for _, r := range slices.Sorted(maps.Keys(e.rows)) {
			row := e.rows[r]
			if !row.unknown && len(row.evidence) == 0 {
				continue
			}
			fmt.Fprintf(&b, "r%d:%t{", r, row.unknown)
			for _, ev := range slices.Sorted(maps.Keys(row.evidence)) {
				fmt.Fprintf(&b, "%d:%v;", ev, row.evidence[ev])
			}
			b.WriteString("}")
		}
		// JSON retains nominal uniques and rigid variable identities, unlike the
		// diagnostic type printer (which deliberately hides both).
		data, err := json.Marshal(e.types)
		if err != nil {
			panic(err)
		}
		b.Write(data)
		b.WriteString("}")
	}
	value = func(v flowValue) {
		fmt.Fprintf(&b, "v%t:%v[", v.unknown, v.caps)
		for _, id := range v.refs {
			o := f.objects[id]
			if o.kind == "global" {
				data, err := json.Marshal(o.code.TypeArgs)
				if err != nil {
					panic(err)
				}
				fmt.Fprintf(&b, "global:%q:%s;", o.def, data)
				continue
			}
			if index := seen[id]; index != 0 {
				fmt.Fprintf(&b, "@%d;", index)
				continue
			}
			seen[id] = len(seen) + 1
			fmt.Fprintf(&b, "o%d:%q:%q:%d:%d:%d{", seen[id], o.kind, o.def, o.ctor, o.owner, o.yieldEffect)
			if o.kind == "lambda" {
				fmt.Fprintf(&b, "code%d:", o.code.ID)
				effects := append(slices.Clone(o.code.Effects), o.code.Deferred...)
				environment(relevantFlowEnv(o.code.Children[0], o.env, []string{o.code.Name}, effects, o.code.RowParam))
			}
			for _, field := range o.fields {
				value(field)
			}
			b.WriteString("}")
		}
		b.WriteString("]")
	}
	environment(env)
	return b.String()
}

func (f *flowChecker) grow() {
	f.changed = true
	f.revision++
}

func (f *flowChecker) mergeAncestry(dst *[]string) {
	for _, c := range f.calls {
		if !slices.Contains(*dst, c.id) {
			*dst = append(*dst, c.id)
			f.grow()
		}
	}
}

// Sharing makes allocation ancestry a graph. Record invocation ancestry even
// on cached calls: an allocation's original caller is not its only ancestor.
func (f *flowChecker) contextAncestry(id string, matches func(*flowContext) bool) bool {
	seen := map[string]bool{}
	var visit func(string) bool
	visit = func(id string) bool {
		if seen[id] {
			return false
		}
		seen[id] = true
		c := f.contexts[id]
		if c == nil {
			return false
		}
		if matches(c) {
			return true
		}
		for _, ancestor := range c.ancestry {
			if visit(ancestor) {
				return true
			}
		}
		return false
	}
	return visit(id)
}

func (f *flowChecker) contextFingerprint(c *flowContext) string {
	if c.revision != f.revision {
		c.fingerprint = f.flowFingerprint(c.entry)
		c.revision = f.revision
	}
	return c.fingerprint
}

// Dynamic obligations participate in compatibility, but their source spans do
// not. Cached obligations are replayed at the actual invocation's location.
func (f *flowChecker) boundaryKey() string {
	var b strings.Builder
	for _, owner := range slices.Sorted(maps.Keys(f.active)) {
		if f.active[owner] != 0 {
			fmt.Fprintf(&b, "borrow%d;", owner)
		}
	}
	sync := func(obligations []synchronousFlow) {
		for _, s := range obligations {
			fmt.Fprintf(&b, "%q:%v;", s.phase, s.enclosing)
		}
	}
	sync(f.synchronous)
	for _, pull := range f.pulls {
		fmt.Fprintf(&b, "pull%d{", pull.owner)
		sync(pull.synchronous)
		b.WriteString("}")
	}
	return b.String()
}

type flowStatistics struct {
	contexts, objects, owners, generations int
	evaluations                            map[string]map[int]int
}

func (f *flowChecker) statistics() flowStatistics {
	s := flowStatistics{len(f.contexts), len(f.objects) - 1, len(f.owners) - 1, f.generation, map[string]map[int]int{}}
	for id, c := range f.contexts {
		s.evaluations[id] = maps.Clone(c.evaluations)
	}
	return s
}
