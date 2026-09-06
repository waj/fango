// Idiomatic Go for loop.fango's job: a tight scalar accumulation loop — the
// handwritten form of the tail-recursive driver the compiler must rewrite
// into a for statement. This is the TCO witness the runtime gate arbitrates;
// see doc/design.md, "Testing and performance".
package main

import "fmt"

func main() {
	var acc int64
	for i := int64(500000000); i >= 1; i-- {
		acc += i
	}
	fmt.Println(acc)
}
