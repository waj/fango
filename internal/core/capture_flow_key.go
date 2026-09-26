package core

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/waj/fango/internal/types"
)

// Keep only definition-site bindings. Invocation binders must not accidentally
// capture a previous activation's arguments, evidence, or residual row. The
// result shares the substitution: callers only read it, and a context stores
// it through snapshot or mergeEnv, which copy.
func (f *flowChecker) relevantFlowEnv(body *types.CaptureFlow, env flowEnv, bound []string, effects []int, row types.CaptureVar) flowEnv {
	out := f.filterFlowEnv(body, env, bound, effects, row)
	out.types = env.types
	return out
}

func (f *flowChecker) filterFlowEnv(body *types.CaptureFlow, env flowEnv, bound []string, effects []int, row types.CaptureVar) flowEnv {
	out := emptyFlowEnv()
	for name := range f.shape.free(body, bound) {
		if v, ok := env.values.lookup(name); ok {
			out.values.set(name, v)
		}
	}
	// The cached set is shared, so bound effects are skipped rather than
	// deleted from it.
	for ev := range f.shape.effects(body) {
		if slices.Contains(effects, ev) {
			continue
		}
		out.evidence[ev] = env.evidence[ev]
	}
	for r := range f.shape.rows(body) {
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
	if c.revision != f.heapRevision {
		c.fingerprint = f.flowFingerprint(c.entry)
		c.revision = f.heapRevision
	}
	return c.fingerprint
}

// Dynamic obligations participate in compatibility, but their source spans do
// not. Cached obligations are replayed at the actual invocation's location.
func (f *flowChecker) boundaryKey() string {
	var b strings.Builder
	for _, completion := range f.detached {
		fmt.Fprintf(&b, "completion%v:%v;", completion.site, completion.outer)
	}
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

// flowKey builds a sharing key. The buffer and the alias table are reused
// across calls: the key is rebuilt on nearly every context lookup, so the
// allocation dominates what it encodes.
type flowKey struct {
	b    []byte
	seen map[int]int
}

// appendInts renders a slice the way %v does, which is what the key used to
// contain and what its stored form must keep matching.
func appendInts(b []byte, xs []int) []byte {
	b = append(b, '[')
	for i, x := range xs {
		if i > 0 {
			b = append(b, ' ')
		}
		b = strconv.AppendInt(b, int64(x), 10)
	}
	return append(b, ']')
}

func appendBool(b []byte, v bool) []byte {
	if v {
		return append(b, "true"...)
	}
	return append(b, "false"...)
}

// typeJSON is json.Marshal for one type, memoized on the three pointer-shaped
// representations. Types are immutable once elaborated, so the encoding of a
// given node never changes. Row is a value type holding a slice, so it is not
// a legal map key and is encoded directly.
func (a *captureAnalyzer) typeJSON(t types.Type) []byte {
	var key types.Type
	switch t.(type) {
	case *types.TVar, *types.TCon, *types.TFun:
		key = t
	}
	if key != nil {
		if data, ok := a.typeKeys[key]; ok {
			return data
		}
	}
	data, err := json.Marshal(t)
	if err != nil {
		panic(err)
	}
	if key != nil {
		if a.typeKeys == nil {
			a.typeKeys = map[types.Type][]byte{}
		}
		a.typeKeys[key] = data
	}
	return data
}

// appendTypes writes what json.Marshal writes for a map[int]types.Type: null
// for a nil map, otherwise entries ordered by their quoted key, which is the
// order encoding/json itself emits. Encoding each type separately is what
// makes the memo above reachable.
func (a *captureAnalyzer) appendTypes(b []byte, m map[int]types.Type) []byte {
	if m == nil {
		return append(b, "null"...)
	}
	keys := make([]string, 0, len(m))
	index := make(map[string]int, len(m))
	for k := range m {
		s := strconv.Itoa(k)
		keys = append(keys, s)
		index[s] = k
	}
	slices.Sort(keys)
	b = append(b, '{')
	for i, s := range keys {
		if i > 0 {
			b = append(b, ',')
		}
		b = strconv.AppendQuote(b, s)
		b = append(b, ':')
		b = append(b, a.typeJSON(m[index[s]])...)
	}
	return append(b, '}')
}

// typeArgsJSON memoizes a code node's type arguments, which never change once
// the contract that carries them is built.
func (a *captureAnalyzer) typeArgsJSON(code *types.CaptureFlow) []byte {
	if data, ok := a.typeArgKeys[code]; ok {
		return data
	}
	data, err := json.Marshal(code.TypeArgs)
	if err != nil {
		panic(err)
	}
	if a.typeArgKeys == nil {
		a.typeArgKeys = map[*types.CaptureFlow][]byte{}
	}
	a.typeArgKeys[code] = data
	return data
}

// Canonical traversal numbers preserve aliases and terminate cyclic closure and
// constructor graphs. Concrete owner IDs are never structurally renumbered.
// The key is recomputed against the mutable heap on lookup, so late field or
// closure-environment growth cannot leave a stale key in the sharing index.
func (f *flowChecker) flowFingerprint(env flowEnv) string {
	f.key.b = f.key.b[:0]
	if f.key.seen == nil {
		f.key.seen = map[int]int{}
	} else {
		clear(f.key.seen)
	}
	f.keyEnv(env)
	return string(f.key.b)
}

func (f *flowChecker) keyEnv(e flowEnv) {
	b := f.key.b
	b = append(b, "env{"...)
	for _, name := range e.values.names() {
		b = strconv.AppendQuote(b, name)
		b = append(b, ':')
		f.key.b = b
		f.keyValue(e.values.get(name))
		b = f.key.b
	}
	for _, ev := range slices.Sorted(maps.Keys(e.evidence)) {
		if len(e.evidence[ev]) == 0 {
			continue
		}
		b = append(b, 'e')
		b = strconv.AppendInt(b, int64(ev), 10)
		b = append(b, ':')
		b = appendInts(b, e.evidence[ev])
		b = append(b, ';')
	}
	for _, r := range slices.Sorted(maps.Keys(e.rows)) {
		row := e.rows[r]
		if !row.unknown && len(row.evidence) == 0 {
			continue
		}
		b = append(b, 'r')
		b = strconv.AppendInt(b, int64(r), 10)
		b = append(b, ':')
		b = appendBool(b, row.unknown)
		b = append(b, '{')
		for _, ev := range slices.Sorted(maps.Keys(row.evidence)) {
			b = strconv.AppendInt(b, int64(ev), 10)
			b = append(b, ':')
			b = appendInts(b, row.evidence[ev])
			b = append(b, ';')
		}
		b = append(b, '}')
	}
	// JSON retains nominal uniques and rigid variable identities, unlike the
	// diagnostic type printer (which deliberately hides both).
	b = f.shape.appendTypes(b, e.types)
	f.key.b = append(b, '}')
}

func (f *flowChecker) keyValue(v flowValue) {
	b := f.key.b
	b = append(b, 'v')
	b = appendBool(b, v.unknown)
	b = append(b, ':')
	b = appendInts(b, v.caps)
	b = append(b, '[')
	for _, id := range v.refs {
		o := f.objects[id]
		if o.kind == "global" {
			b = append(b, "global:"...)
			b = strconv.AppendQuote(b, o.def)
			b = append(b, ':')
			b = append(b, f.shape.typeArgsJSON(o.code)...)
			b = append(b, ';')
			continue
		}
		if index := f.key.seen[id]; index != 0 {
			b = append(b, '@')
			b = strconv.AppendInt(b, int64(index), 10)
			b = append(b, ';')
			continue
		}
		f.key.seen[id] = len(f.key.seen) + 1
		b = append(b, 'o')
		b = strconv.AppendInt(b, int64(f.key.seen[id]), 10)
		b = append(b, ':')
		b = strconv.AppendQuote(b, o.kind)
		b = append(b, ':')
		b = strconv.AppendQuote(b, o.def)
		b = append(b, ':')
		b = strconv.AppendInt(b, int64(o.ctor), 10)
		b = append(b, ':')
		b = strconv.AppendInt(b, int64(o.owner), 10)
		b = append(b, '{')
		if o.kind == "completion-abort" {
			b = append(b, "exit"...)
			b = appendInts(b, o.code.Effects)
			b = strconv.AppendInt(b, int64(o.code.Index), 10)
		}
		if o.kind == "lambda" {
			b = append(b, "code"...)
			b = strconv.AppendInt(b, int64(o.code.ID), 10)
			b = append(b, ':')
			f.key.b = b
			effects := append(slices.Clone(o.code.Effects), o.code.Deferred...)
			f.keyEnv(f.relevantFlowEnv(o.code.Children[0], o.env, []string{o.code.Name}, effects, o.code.RowParam))
			b = f.key.b
		}
		for _, field := range o.fields {
			f.key.b = b
			f.keyValue(field)
			b = f.key.b
		}
		b = append(b, '}')
	}
	f.key.b = append(b, ']')
}

// The three syntactic summaries below are pure functions of a contract node,
// and the sharing key recomputes them for every closure it reaches. Contract
// nodes are immutable once built, so each answer is computed once per analysis.
// The cached sets are shared, so no caller may modify one.
type flowWalkKey struct {
	node  *types.CaptureFlow
	bound string
}

func (a *captureAnalyzer) free(n *types.CaptureFlow, bound []string) map[string]bool {
	key := flowWalkKey{n, strings.Join(bound, "\x00")}
	if out, ok := a.freeKeys[key]; ok {
		return out
	}
	out := flowFree(n, bound)
	if a.freeKeys == nil {
		a.freeKeys = map[flowWalkKey]map[string]bool{}
	}
	a.freeKeys[key] = out
	return out
}

func (a *captureAnalyzer) effects(n *types.CaptureFlow) map[int]bool {
	if out, ok := a.effectKeys[n]; ok {
		return out
	}
	out := flowEffects(n)
	if a.effectKeys == nil {
		a.effectKeys = map[*types.CaptureFlow]map[int]bool{}
	}
	a.effectKeys[n] = out
	return out
}

func (a *captureAnalyzer) rows(n *types.CaptureFlow) map[types.CaptureVar]bool {
	if out, ok := a.rowKeys[n]; ok {
		return out
	}
	out := flowRows(n)
	if a.rowKeys == nil {
		a.rowKeys = map[*types.CaptureFlow]map[types.CaptureVar]bool{}
	}
	a.rowKeys[n] = out
	return out
}
