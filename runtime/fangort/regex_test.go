package fangort

import (
	"regexp"
	"sync"
	"testing"
)

func TestRegexLiteralConcurrentReuse(t *testing.T) {
	const pattern = `(?P<word>\p{L}+)\d*`
	results := make([]*regexp.Regexp, 32)
	var workers sync.WaitGroup
	for i := range results {
		workers.Add(1)
		go func(i int) { defer workers.Done(); results[i] = RegexLiteral(pattern) }(i)
	}
	workers.Wait()
	for _, result := range results {
		if result != results[0] || !result.MatchString("é二123") {
			t.Fatal("literal was not compiled and shared")
		}
	}
}
