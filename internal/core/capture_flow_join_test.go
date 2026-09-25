package core

import (
	"slices"
	"testing"
)

func TestJoinFlowUnionsSortedSets(t *testing.T) {
	for _, tc := range []struct {
		a, b, want []int
	}{
		{nil, nil, nil},
		{[]int{1, 3}, nil, []int{1, 3}},
		{nil, []int{2}, []int{2}},
		{[]int{1, 3, 5}, []int{2, 3, 6}, []int{1, 2, 3, 5, 6}},
		{[]int{1, 2, 3}, []int{2}, []int{1, 2, 3}},
		{[]int{2}, []int{1, 2, 3}, []int{1, 2, 3}},
		// Unsorted or duplicated inputs take the sort-and-compact path.
		{[]int{3, 1}, []int{2}, []int{1, 2, 3}},
		{[]int{1, 1}, nil, []int{1}},
	} {
		got := joinFlow(flowValue{refs: tc.a}, flowValue{caps: tc.a}, flowValue{refs: tc.b, caps: tc.b})
		if !slices.Equal(got.refs, tc.want) || !slices.Equal(got.caps, tc.want) {
			t.Errorf("joinFlow(%v, %v) = %v/%v, want %v", tc.a, tc.b, got.refs, got.caps, tc.want)
		}
	}
}

// An input returned as the union must not hand out spare capacity: the
// evaluator appends to results in place, and two appends to one shared
// backing array would overwrite each other.
func TestJoinFlowResultHasNoSpareCapacity(t *testing.T) {
	shared := make([]int, 2, 8)
	shared[0], shared[1] = 1, 2
	x := joinFlow(flowValue{refs: shared}, flowValue{refs: []int{1}})
	y := joinFlow(flowValue{refs: shared}, flowValue{})
	x.refs = append(x.refs, 7)
	y.refs = append(y.refs, 9)
	if x.refs[2] != 7 || y.refs[2] != 9 || len(shared) != 2 {
		t.Errorf("appends to joined results aliased: %v %v", x.refs, y.refs)
	}
}
