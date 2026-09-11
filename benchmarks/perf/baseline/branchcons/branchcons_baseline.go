// Idiomatic Go for branchcons.fango's job. A Go programmer who needs a shared,
// repeatedly extended prefix reaches for cons cells rather than a slice,
// because a slice has to copy on every branch — so this baseline is the
// pointer-per-element structure fango's array-backed list must not lose to.
// See doc/design.md, "Testing and performance", and doc/roadmap-list.md.
package main

import "fmt"

type cell struct {
	head int64
	tail *cell
}

func path(n int64) *cell {
	var xs *cell
	for i := int64(1); i <= n; i++ {
		xs = &cell{head: i, tail: xs}
	}
	return xs
}

func total(xs *cell) int64 {
	var acc int64
	for ; xs != nil; xs = xs.tail {
		acc += xs.head
	}
	return acc
}

func branches(shared *cell, width int64) int64 {
	var acc int64
	for w := width; w >= 1; w-- {
		acc += total(&cell{head: w, tail: shared})
	}
	return acc
}

func main() {
	var acc int64
	for i := 0; i < 60; i++ {
		acc += branches(path(200), 200)
	}
	fmt.Println(acc)
}
