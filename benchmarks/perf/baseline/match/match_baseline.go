// The handwritten-Go baseline for benchmarks/perf/match.fango — the
// idiomatic Go shape for an enum: int constants and value switches (what
// §8.10's enum-as-int upgrade would emit; this ratio is its arbiter).
package main

import "fmt"

type op int

const (
	add op = iota
	sub
	mul
	nop
)

func tick(o op) op {
	switch o {
	case add:
		return sub
	case sub:
		return mul
	case mul:
		return nop
	default:
		return add
	}
}

func apply(o op, n, m int64) int64 {
	switch o {
	case add:
		return n + m
	case sub:
		return n - m
	case mul:
		return n*m + 3
	default:
		return n + 1
	}
}

func crunch(o op, n int64) int64 {
	if n < 2 {
		return apply(o, n, 1)
	}
	return apply(o, crunch(tick(o), n-1), crunch(tick(tick(o)), n-2))
}

func main() {
	fmt.Println(crunch(add, 37))
}
