package main

import "fmt"

type evidence struct {
	get func() int64
	put func(int64)
}

func run(initial int64, body func(evidence) int64) int64 {
	current := initial
	ev := evidence{
		get: func() int64 { return current },
		put: func(next int64) { current = next },
	}
	body(ev)
	return current
}

func advance(ev evidence, n int64) int64 {
	for n > 0 {
		ev.put(ev.get() + 1)
		n--
	}
	return ev.get()
}

func main() {
	fmt.Println(run(0, func(ev evidence) int64 { return advance(ev, 5000000) }))
}
