package fangort

import (
	"regexp"
	"sync"
)

// RegexLiteral is compiler-only initialization for validated patterns. The
// winning entry's Once prevents duplicate compilation under concurrent forces.
// Dynamic Regex.compile calls do not use this cache.
func RegexLiteral(pattern string) *regexp.Regexp {
	candidate := &regexLiteralEntry{}
	value, _ := regexLiterals.LoadOrStore(pattern, candidate)
	entry := value.(*regexLiteralEntry)
	entry.once.Do(func() { entry.regex = regexp.MustCompile(pattern) })
	return entry.regex
}

type regexLiteralEntry struct {
	once  sync.Once
	regex *regexp.Regexp
}

var regexLiterals sync.Map
