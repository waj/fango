package fangort

import (
	"math"
	"sync"
	"testing"
)

func listOf(xs ...int) List[int] {
	l := ListNil[int]()
	for i := len(xs) - 1; i >= 0; i-- {
		l = ListCons(xs[i], l)
	}
	return l
}

func elems[T any](l List[T]) []T {
	var out []T
	for ; !l.IsEmpty(); l = l.Tail() {
		out = append(out, l.Head())
	}
	return out
}

func eqInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestListEmptyAndSingleton(t *testing.T) {
	if !ListNil[int]().IsEmpty() {
		t.Fatal("ListNil is not empty")
	}
	var zero List[int]
	if !zero.IsEmpty() {
		t.Fatal("the zero value must be Nil")
	}
	one := ListCons(7, ListNil[int]())
	if one.IsEmpty() || one.Head() != 7 {
		t.Fatalf("singleton head = %v, empty = %v", one.Head(), one.IsEmpty())
	}
	if !one.Tail().IsEmpty() {
		t.Fatal("singleton tail must be Nil")
	}
}

// Building linearly must preserve order at varied sizes.
func TestListLinearBuild(t *testing.T) {
	for _, n := range []int{1, 32 - 1, 32, 32 + 1, 3*32 + 5, 1000} {
		want := make([]int, n)
		for i := range want {
			want[i] = i
		}
		if got := elems(listOf(want...)); !eqInts(got, want) {
			t.Fatalf("n=%d: got %v", n, got)
		}
	}
}

// Dropping k elements must preserve the remaining suffix.
func TestListTailAtEveryOffset(t *testing.T) {
	n := 3*32 + 7
	all := make([]int, n)
	for i := range all {
		all[i] = i
	}
	l := listOf(all...)
	for k := 0; k <= n; k++ {
		d := l
		for i := 0; i < k; i++ {
			d = d.Tail()
		}
		if got := elems(d); !eqInts(got, all[k:]) {
			t.Fatalf("drop %d: got %v want %v", k, got, all[k:])
		}
	}
}

// The invariant that a naive slice-backed prepend gets wrong: consing twice
// onto one value must produce two independent lists and leave the shared tail
// untouched. Exercised both on a value that owns its chunk's frontier and on
// one that does not.
func TestListBranchingIsIndependent(t *testing.T) {
	cases := map[string]func() (List[int], []int){
		"frontier": func() (List[int], []int) {
			return listOf(1, 2, 3), []int{1, 2, 3}
		},
		"mid-chunk": func() (List[int], []int) {
			return listOf(0, 1, 2, 3).Tail(), []int{1, 2, 3}
		},
		"chunk boundary": func() (List[int], []int) {
			all := make([]int, 32)
			for i := range all {
				all[i] = i
			}
			return listOf(all...), all
		},
		"empty": func() (List[int], []int) { return ListNil[int](), nil },
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			shared, base := mk()
			a := ListCons(91, shared)
			b := ListCons(92, shared)
			c := ListCons(93, a)

			wantA := append([]int{91}, base...)
			wantB := append([]int{92}, base...)
			wantC := append([]int{93}, wantA...)

			// Read them in an order that would expose a later cons clobbering
			// an earlier one's slot, then re-read the shared tail last.
			if got := elems(b); !eqInts(got, wantB) {
				t.Errorf("b = %v want %v", got, wantB)
			}
			if got := elems(c); !eqInts(got, wantC) {
				t.Errorf("c = %v want %v", got, wantC)
			}
			if got := elems(a); !eqInts(got, wantA) {
				t.Errorf("a = %v want %v", got, wantA)
			}
			if got := elems(shared); !eqInts(got, base) {
				t.Errorf("shared tail mutated: %v want %v", got, base)
			}
		})
	}
}

// Every child extends the same published value. The race detector checks the
// shared storage while these assertions check that no branch can see another
// branch's head or change the shared tail.
func TestListConcurrentBranching(t *testing.T) {
	shared := listOf(1, 2, 3)
	const children = 64
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make([]List[int], children)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i] = ListCons(100+i, shared)
			if got := elems(results[i]); !eqInts(got, []int{100 + i, 1, 2, 3}) {
				t.Errorf("child %d = %v", i, got)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	for i, result := range results {
		if got := elems(result); !eqInts(got, []int{100 + i, 1, 2, 3}) {
			t.Errorf("published child %d = %v", i, got)
		}
	}
	if got := elems(shared); !eqInts(got, []int{1, 2, 3}) {
		t.Errorf("shared tail = %v", got)
	}
}

// A deterministic walk mixing cons, tail, and branching, checked against a
// slice model. Keeping every intermediate list alive is the point: a
// representation that reused a published slot would diverge here.
func TestListAgainstSliceModel(t *testing.T) {
	type state struct {
		list  List[int]
		model []int
	}
	states := []state{{ListNil[int](), nil}}
	rng := uint64(1)
	next := func(n int) int {
		rng = rng*6364136223846793005 + 1442695040888963407
		return int(rng >> 33 % uint64(n))
	}
	for step := 0; step < 4000; step++ {
		s := states[next(len(states))]
		if next(3) == 0 && len(s.model) > 0 {
			states = append(states, state{s.list.Tail(), s.model[1:]})
			continue
		}
		v := step
		states = append(states, state{
			ListCons(v, s.list),
			append([]int{v}, s.model...),
		})
	}
	for i, s := range states {
		if got := elems(s.list); !eqInts(got, s.model) {
			t.Fatalf("state %d diverged:\n got %v\nwant %v", i, got, s.model)
		}
	}
}

func TestListEq(t *testing.T) {
	eq := func(a, b int) bool { return a == b }
	long := make([]int, 2*32+3)
	for i := range long {
		long[i] = i
	}
	cases := []struct {
		name string
		a, b List[int]
		want bool
	}{
		{"both empty", ListNil[int](), ListNil[int](), true},
		{"empty vs one", ListNil[int](), listOf(1), false},
		{"one vs empty", listOf(1), ListNil[int](), false},
		{"equal", listOf(1, 2, 3), listOf(1, 2, 3), true},
		{"differing element", listOf(1, 2, 3), listOf(1, 9, 3), false},
		{"prefix", listOf(1, 2), listOf(1, 2, 3), false},
		{"across chunks", listOf(long...), listOf(long...), true},
	}
	for _, c := range cases {
		if got := ListEq(eq, c.a, c.b); got != c.want {
			t.Errorf("%s: ListEq = %v, want %v", c.name, got, c.want)
		}
	}
}

// A representation-identity short circuit would make this list equal to
// itself, which Fango's Float equality says it is not.
func TestListEqHasNoIdentityShortCircuit(t *testing.T) {
	eq := func(a, b float64) bool { return a == b }
	nan := ListCons(math.NaN(), ListCons(1.0, ListNil[float64]()))
	if ListEq(eq, nan, nan) {
		t.Fatal("a list containing NaN must not compare equal to itself")
	}
	ok := ListCons(2.0, ListCons(1.0, ListNil[float64]()))
	if !ListEq(eq, ok, ok) {
		t.Fatal("a NaN-free list must compare equal to itself")
	}
}

// Iteration is allocation-free: Tail returns a value, so generated Go keeps it
// in locals rather than on the heap. doc/roadmap-list.md depends on this.
func TestListTraversalDoesNotAllocate(t *testing.T) {
	all := make([]int, 5*32)
	for i := range all {
		all[i] = i
	}
	l := listOf(all...)
	got := testing.AllocsPerRun(100, func() {
		sum := 0
		for c := l; !c.IsEmpty(); c = c.Tail() {
			sum += c.Head()
		}
		if sum == -1 {
			t.Fatal("unreachable")
		}
	})
	if got != 0 {
		t.Fatalf("traversal allocated %v times per run, want 0", got)
	}
}

// Linear building allocates one immutable node per element.
func TestListLinearBuildAllocatesPerElement(t *testing.T) {
	const n = 10 * 32
	got := testing.AllocsPerRun(100, func() {
		l := ListNil[int]()
		for i := 0; i < n; i++ {
			l = ListCons(i, l)
		}
		if l.IsEmpty() {
			t.Fatal("unreachable")
		}
	})
	if want := float64(n); got != want {
		t.Fatalf("building %d elements allocated %v times, want %v", n, got, want)
	}
}
