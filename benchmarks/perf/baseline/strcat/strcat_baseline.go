// Go at strcat.fango's own algorithm: right-associated naive concatenation,
// gating fango's per-operation string overhead at equal asymptotics. A
// strings.Builder version is the future arbiter for builder-based
// derived show, once show is user-callable.
package main

import "fmt"

func rep(n int64, s string) string {
	if n < 1 {
		return ""
	}
	return s + rep(n-1, s)
}

func main() {
	a := rep(20000, "abcdefgh")
	b := rep(20000, "abcdefgh")
	if a == b {
		fmt.Println("True")
	} else {
		fmt.Println("False")
	}
}
