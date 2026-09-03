// Idiomatic Go for tree.fango's job: build a binary tree of pointer nodes
// and fold it — GC pressure at the same allocation shape.
package main

import "fmt"

type tree struct {
	left  *tree
	val   int64
	right *tree
}

func build(d, x int64) *tree {
	if d < 1 {
		return nil
	}
	return &tree{build(d-1, x*2), x, build(d-1, x*2+1)}
}

func total(t *tree) int64 {
	if t == nil {
		return 0
	}
	return total(t.left) + t.val + total(t.right)
}

func main() {
	var acc int64
	for i := 0; i < 20; i++ {
		acc += total(build(16, 1))
	}
	fmt.Println(acc)
}
