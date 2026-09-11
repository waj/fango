package main

import "fmt"

func bracket[R, A any](acquire func() R, release func(R), use func(R) A) A {
	resource := acquire()
	result := use(resource)
	release(resource)
	return result
}

func step(total int64) int64 {
	return bracket(func() int64 { return total }, func(int64) {}, func(outer int64) int64 {
		return bracket(func() int64 { return outer + 1 }, func(int64) {}, func(inner int64) int64 {
			return inner + 1
		})
	})
}

func main() {
	var total int64
	for n := 20000000; n > 0; n-- {
		total = step(total)
	}
	fmt.Println(total)
}
