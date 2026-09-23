package feasibility

import (
	"reflect"
	"sort"
	"testing"
)

// E stands for a closed residual evidence row. A compiler-generated row
// package must provide the same typed abstraction for an open Fango row.
// The model uses neither any nor a cast in the heterogeneous task registry.
// This file isolates lifecycle under one common internal budget; distinct
// per-task rows and nullary queue packaging are covered in scoped_rows_test.go.
type delivery struct {
	target  string
	integer *int
	text    *string
}
type failure[E any] struct {
	replay     func(E) delivery // Receives fresh evidence, never a saved exit target.
	suppressed []failure[E]
}
type completion[A, E any] struct {
	value     A
	failed    *failure[E]
	cancelled bool // Private control outcome, not a Fail payload.
}
type cell[A, E any] struct {
	ready bool
	value completion[A, E]
}
type task[A, E any] struct {
	owner  *context[E]
	id     int
	result *cell[A, E]
}
type entry[E any] struct {
	run    func() *failure[E]
	cancel func() *failure[E]
}
type recorded[E any] struct {
	sequence int
	failure  failure[E]
}
type context[E any] struct {
	lifetime                     *lifetime
	next                         int
	live                         map[int]entry[E]
	ready                        []int
	failures                     []recorded[E]
	bodyDone, cancelling, closed bool
}

func newContext[E any](scope *lifetime) *context[E] {
	return &context[E]{lifetime: scope, live: map[int]entry[E]{}}
}

// Models C4 registration and C6a typed write-once storage. Native code does
// not call the closures: the Fango driver owns the homogeneous entry queue.
func spawn[A, E any](c *context[E], captures []capability, action func() completion[A, E], cleanup func() *failure[E]) (task[A, E], bool) {
	if c.cancelling || c.closed || retain(c.lifetime, captures, true) != nil {
		return task[A, E]{}, false
	}
	c.next++
	id := c.next
	result := &cell[A, E]{}
	finish := func(out completion[A, E]) *failure[E] {
		if result.ready {
			panic("duplicate completion")
		}
		if release := cleanup(); release != nil {
			if out.failed == nil {
				out.failed = release
			} else {
				combined := *out.failed
				combined.suppressed = append(append([]failure[E]{}, combined.suppressed...), *release)
				out.failed = &combined
			}
		}
		// Cleanup failure remains reportable even when cancellation initiated it.
		result.value, result.ready = out, true
		return out.failed
	}
	c.live[id] = entry[E]{
		run: func() *failure[E] { return finish(action()) },
		// An unstarted action acquires nothing, so cancellation must not call
		// its body or release. The slot itself can still publish cancellation.
		cancel: func() *failure[E] {
			result.value.cancelled, result.ready = true, true
			return nil
		},
	}
	c.ready = append(c.ready, id)
	return task[A, E]{c, id, result}, true // Publish only after registration.
}

func (c *context[E]) step() {
	id := c.ready[0]
	c.ready = c.ready[1:]
	job := c.live[id]
	var failed *failure[E]
	if c.cancelling {
		failed = job.cancel()
	} else {
		failed = job.run()
	}
	delete(c.live, id) // Drop execution closure; result survives only via handles.
	if failed != nil {
		c.failures = append(c.failures, recorded[E]{id, *failed})
		c.cancelling = true
	}
}

func (c *context[E]) drain() {
	for len(c.ready) > 0 {
		c.step()
	}
	c.closed = c.bodyDone
}

// Await does not consume completion or mutate the task's owning context.
func observe[A, E any](task task[A, E], caller *context[E]) (completion[A, E], bool) {
	if task.owner.closed || !outlives(task.owner.lifetime, caller.lifetime) || !task.result.ready {
		return completion[A, E]{}, false
	}
	return task.result.value, true
}

func selectFailure[E any](body *failure[E], children []recorded[E], releases []failure[E]) *failure[E] {
	ordered := append([]recorded[E]{}, children...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].sequence < ordered[j].sequence })
	var reports []failure[E]
	if body != nil {
		reports = append(reports, *body)
	}
	for _, child := range ordered {
		reports = append(reports, child.failure)
	}
	reports = append(reports, releases...) // Already in LIFO release-attempt order.
	if len(reports) == 0 {
		return nil
	}
	primary := reports[0]
	primary.suppressed = append(append([]failure[E]{}, primary.suppressed...), reports[1:]...)
	return &primary
}

type evidence struct {
	integer func(int) delivery
	text    func(string) delivery
}

// Proposed shared-service evidence keeps context identity but receives the
// invocation's execution authority explicitly. This is a general C6c compiler
// contract extension, not today's ordinary captured handler representation.
type service[E any] struct{ context *context[E] }
type execution[E any] struct {
	active bool
	pause  func(*context[E])
}

func (s service[E]) request(current *execution[E]) bool {
	if current == nil || !current.active || s.context.closed {
		return false
	}
	current.pause(s.context)
	return true
}

func TestSharedServiceSeparatesContextFromProducerAuthority(t *testing.T) {
	root := &lifetime{name: "root"}
	outer := newContext[evidence](root)
	inner := newContext[evidence](&lifetime{"inner", root})
	captured := service[evidence]{outer}
	var destinations []*context[evidence]
	var producers []string
	parent := &execution[evidence]{true, func(c *context[evidence]) {
		destinations = append(destinations, c)
		producers = append(producers, "parent")
	}}
	child := &execution[evidence]{true, func(c *context[evidence]) {
		destinations = append(destinations, c)
		producers = append(producers, "child")
	}}
	stored := func(exec *execution[evidence]) bool { return captured.request(exec) }
	if !stored(parent) || !stored(child) || !(service[evidence]{inner}).request(child) {
		t.Fatal("live invocation rejected")
	}
	// A nested pull threads the task's service slot; it does not replace it
	// with the pull's differently typed local suspension callback.
	throughPull := func(slot *execution[evidence]) bool { return stored(slot) }
	if !throughPull(child) {
		t.Fatal("nested invocation rejected")
	}
	if !reflect.DeepEqual(destinations, []*context[evidence]{outer, outer, inner, outer}) || !reflect.DeepEqual(producers, []string{"parent", "child", "child", "child"}) {
		t.Fatal("context or producer authority selected from ambient state")
	}
	child.active = false
	if stored(child) || stored(nil) {
		t.Fatal("shared context granted producer authority")
	}
}

func intFailure(n int) *failure[evidence] {
	return &failure[evidence]{replay: func(e evidence) delivery { return e.integer(n) }}
}
func textFailure(s string) *failure[evidence] {
	return &failure[evidence]{replay: func(e evidence) delivery { return e.text(s) }}
}
func at(target string) evidence {
	return evidence{
		integer: func(n int) delivery { return delivery{target: target, integer: &n} },
		text:    func(s string) delivery { return delivery{target: target, text: &s} },
	}
}

func TestHeterogeneousTasksDynamicOwnershipAndReclamation(t *testing.T) {
	root := &lifetime{name: "root"}
	c := newContext[evidence](root)
	runs, releases := 0, 0
	cleanup := func() *failure[evidence] { releases++; return nil }
	integer, ok := spawn(c, nil, func() completion[int, evidence] {
		runs++
		return completion[int, evidence]{value: 42}
	}, cleanup)
	if !ok {
		t.Fatal("spawn int")
	}
	text, ok := spawn(c, nil, func() completion[string, evidence] {
		runs++
		return completion[string, evidence]{value: "answer"}
	}, cleanup)
	if !ok {
		t.Fatal("spawn string")
	}
	if runs != 0 || len(c.live) != 2 {
		t.Fatal("eager execution or unregistered publication")
	}
	c.drain()
	inner := newContext[evidence](&lifetime{"inner", root})
	read := func() int { out, _ := observe(integer, inner); return out.value }
	word, ready := observe(text, inner)
	if read() != 42 || read() != 42 || !ready || word.value != "answer" || integer.owner != c || runs != 2 || releases != 2 || len(c.live) != 0 {
		t.Fatal("typed completion, repeated await, ownership, or reclamation failed")
	}
	// Child creation after the context body finished extends the same drain.
	_, ok = spawn(c, nil, func() completion[int, evidence] {
		_, accepted := spawn(c, nil, func() completion[int, evidence] { return completion[int, evidence]{value: 2} }, cleanup)
		if !accepted {
			t.Fatal("grandchild rejected")
		}
		return completion[int, evidence]{value: 1}
	}, cleanup)
	if !ok {
		t.Fatal("child rejected")
	}
	c.bodyDone = true
	c.drain()
	if releases != 4 || len(c.live) != 0 || !c.closed {
		t.Fatal("missed descendant drain")
	}
	if _, ok := observe(integer, inner); ok {
		t.Fatal("completed handle escaped closed scope")
	}
	if _, ok := spawn(c, nil, func() completion[int, evidence] { return completion[int, evidence]{} }, cleanup); ok {
		t.Fatal("closed scope admitted allocation")
	}
	c = newContext[evidence](root)
	for range 1000 {
		_, _ = spawn(c, nil, func() completion[int, evidence] { return completion[int, evidence]{} }, cleanup)
		c.step()
		if len(c.live) != 0 {
			t.Fatal("dead execution retained")
		}
	}
	local := &lifetime{"helper", root}
	if _, ok := spawn(c, []capability{{owner: local}}, func() completion[int, evidence] { return completion[int, evidence]{} }, cleanup); ok {
		t.Fatal("helper-local resource retained")
	}
}

func TestTypedFailuresFreshEvidenceAndUnswallowableCancellation(t *testing.T) {
	c := newContext[evidence](&lifetime{name: "context"})
	releases := 0
	failed, _ := spawn(c, nil, func() completion[int, evidence] {
		return completion[int, evidence]{failed: intFailure(7)}
	}, func() *failure[evidence] { releases++; return textFailure("release") })
	started := false
	cancelled, _ := spawn(c, nil, func() completion[string, evidence] {
		started = true
		return completion[string, evidence]{value: "unreachable"}
	}, func() *failure[evidence] { t.Fatal("unstarted release"); return nil })
	c.drain()
	a, ok := observe(failed, c)
	b, _ := observe(failed, c)
	if !ok || a.failed == nil || len(c.failures) != 1 || releases != 1 || started {
		t.Fatal("failure lifecycle")
	}
	x, y := a.failed.replay(at("first await")), b.failed.replay(at("second await"))
	if x.target == y.target || *x.integer != 7 || *y.integer != 7 {
		t.Fatal("stored stale evidence or lost typed payload")
	}
	if *a.failed.suppressed[0].replay(at("cleanup observer")).text != "release" {
		t.Fatal("lost typed cleanup")
	}
	// Observing/catching await leaves the owner's failure ledger intact.
	if len(c.failures) != 1 {
		t.Fatal("await consumed context failure")
	}
	stopped, _ := observe(cancelled, c)
	if !stopped.cancelled || stopped.failed != nil {
		t.Fatal("cancellation became an ordinary failure")
	}
	if _, ok := spawn(c, nil, func() completion[int, evidence] { return completion[int, evidence]{} }, func() *failure[evidence] { return nil }); ok {
		t.Fatal("cancelling context admitted new work")
	}
	// An expected child error is a normal, typed result and never enters ledger.
	type result struct {
		error string
		value int
	}
	safe := newContext[evidence](&lifetime{name: "safe"})
	expected, _ := spawn(safe, nil, func() completion[result, evidence] {
		return completion[result, evidence]{value: result{error: "expected"}}
	}, func() *failure[evidence] { return nil })
	safe.drain()
	v, _ := observe(expected, safe)
	if v.value.error != "expected" || len(safe.failures) != 0 {
		t.Fatal("expected Result failed context")
	}
}

func TestFailureSelectionIsStableForTheRecordedSet(t *testing.T) {
	body, first, second, release := intFailure(99), intFailure(1), intFailure(2), intFailure(3)
	first.suppressed = []failure[evidence]{*textFailure("nested release")}
	for _, children := range [][]recorded[evidence]{
		{{2, *second}, {1, *first}}, {{1, *first}, {2, *second}},
	} {
		for _, body := range []*failure[evidence]{body, nil} {
			report := selectFailure(body, children, []failure[evidence]{*release})
			var values []int
			values = append(values, *report.replay(at("parent")).integer)
			for _, s := range report.suppressed {
				if n := s.replay(at("parent")).integer; n != nil {
					values = append(values, *n)
				}
			}
			want := []int{99, 1, 2, 3}
			if body == nil {
				want = want[1:]
			}
			if !reflect.DeepEqual(values, want) {
				t.Fatalf("order = %v, want %v", values, want)
			}
		}
	}
	if len(first.suppressed) != 1 || first.suppressed[0].replay(at("parent")).text == nil {
		t.Fatal("report tree mutated")
	}
}
