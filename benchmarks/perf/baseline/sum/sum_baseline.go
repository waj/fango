// Idiomatic Go for sum.fango's job: build a collection of n ints, sum it,
// twenty times. A Go programmer reaches for a slice — this is the honest
// ceiling a cons list races (§11: list-heavy code gates at 3.0x).
package main

import "fmt"

func build(n int64) []int64 {
	xs := make([]int64, 0, n)
	for i := n; i >= 1; i-- {
		xs = append(xs, i)
	}
	return xs
}

func sum(xs []int64) int64 {
	var acc int64
	for _, x := range xs {
		acc += x
	}
	return acc
}

func main() {
	var acc int64
	for i := 0; i < 20; i++ {
		acc += sum(build(100000))
	}
	fmt.Println(acc)
}
