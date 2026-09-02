// The handwritten-Go baseline for benchmarks/perf/fib.fango — idiomatic
// Go, int64 like fango's Int, printing the same way fangort does.
package main

import "fmt"

func fib(n int64) int64 {
	if n < 2 {
		return n
	}
	return fib(n-1) + fib(n-2)
}

func main() {
	fmt.Println(fib(35))
}
