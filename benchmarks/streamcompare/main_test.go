package main

import "testing"

func TestChecksums(t *testing.T) {
	for n := int64(2); n < 12; n++ {
		var sum, pipe, take int64
		for v := n; v > 0; v-- {
			sum += v
			if v+1 > n/2 {
				pipe += v + 1
			}
			if n-v < n/2 {
				take += v
			}
		}
		want := []int64{sum, sum, pipe, sum + 4*n, sum, 2 * sum, take, 36 * n, take + n/2 + 1000, sum, sum, sum, sum, sum}
		for c, v := range want {
			if got := expected(c, n); got != v {
				t.Fatalf("case %d n %d: %d != %d", c, n, got, v)
			}
		}
	}
}
func TestParityVerdictIncludesAllocationsAndUncertainty(t *testing.T) {
	base := make([]sample, 15)
	fast := make([]sample, 15)
	for i := range base {
		base[i] = sample{NS: 100, Bytes: 100, Allocs: 10, Repetitions: 1}
		fast[i] = sample{NS: 90, Bytes: 90, Allocs: 9, Repetitions: 1}
	}
	if c := compare(base, fast); c.Verdict != "pass" {
		t.Fatal(c)
	}
	for i := range fast {
		fast[i].Bytes = 101
	}
	if c := compare(base, fast); c.Verdict != "fail" {
		t.Fatal(c)
	}
	for i := range fast {
		fast[i] = base[i]
		fast[i].NS = 110
	}
	if c := compare(base, fast); c.Verdict != "fail" {
		t.Fatal(c)
	}
	for i := range fast {
		fast[i] = base[i]
	}
	if c := compare(base, fast); c.Verdict != "pass" {
		t.Fatal(c)
	}
}
