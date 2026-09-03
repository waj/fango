// Idiomatic Go for mapfilter.fango's job: filter, map, and sum a collection,
// as staged slice passes — the shape a Go programmer writes when asked for
// the same pipeline (a single fused loop would be a different program).
package main

import "fmt"

func upto(n int64) []int64 {
	xs := make([]int64, 0, n)
	for i := n; i >= 1; i-- {
		xs = append(xs, i)
	}
	return xs
}

func filterLess(n int64, xs []int64) []int64 {
	out := make([]int64, 0, len(xs))
	for _, x := range xs {
		if x < n {
			out = append(out, x)
		}
	}
	return out
}

func triple(xs []int64) []int64 {
	out := make([]int64, len(xs))
	for i, x := range xs {
		out[i] = x * 3
	}
	return out
}

func sum(xs []int64) int64 {
	var acc int64
	for _, x := range xs {
		acc += x
	}
	return acc
}

func chain(n int64) int64 {
	return sum(triple(filterLess(n, upto(n))))
}

func main() {
	var acc int64
	for i := 0; i < 20; i++ {
		acc += chain(30000)
	}
	fmt.Println(acc)
}
