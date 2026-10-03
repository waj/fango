package native

import (
	"regexp"
)

type regexCompilation struct {
	regex   *regexp.Regexp
	message string
}

func CompileRaw(pattern string) any {
	regex, err := regexp.Compile(pattern)
	result := &regexCompilation{regex: regex}
	if err != nil {
		result.message = err.Error()
	}
	return result
}
func CompileSucceeded(result any) bool    { return result.(*regexCompilation).regex != nil }
func CompiledRegex(result any) any        { return result.(*regexCompilation).regex }
func CompileMessage(result any) string    { return result.(*regexCompilation).message }
func Pattern(regex any) string            { return regex.(*regexp.Regexp).String() }
func Matches(regex any, text string) bool { return regex.(*regexp.Regexp).MatchString(text) }
func ReplaceAll(regex any, replacement, text string) string {
	return regex.(*regexp.Regexp).ReplaceAllString(text, replacement)
}
func ReplaceAllLiteral(regex any, replacement, text string) string {
	return regex.(*regexp.Regexp).ReplaceAllLiteralString(text, replacement)
}
func Escape(text string) string { return regexp.QuoteMeta(text) }

type regexSpan struct {
	text       string
	start, end int64
}

type regexMatchBatch struct {
	groups [][]regexSpan
	names  []string
}

// Preserve unmatched captures with -1; an empty capture has equal nonnegative
// endpoints. Build scalar offsets in one input pass, never one prefix per group.
func regexFind(regex any, text string, limit int) any {
	compiled := regex.(*regexp.Regexp)
	indices := compiled.FindAllStringSubmatchIndex(text, limit)
	result := &regexMatchBatch{names: compiled.SubexpNames()}
	if len(indices) == 0 {
		return result
	}
	offsets := make([]int64, len(text)+1)
	var scalar int64
	for at := range text {
		offsets[at] = scalar
		scalar++
	}
	offsets[len(text)] = scalar
	for _, match := range indices {
		groups := make([]regexSpan, len(match)/2)
		for group := range groups {
			start, end := match[2*group], match[2*group+1]
			if start < 0 {
				groups[group] = regexSpan{start: -1, end: -1}
			} else {
				groups[group] = regexSpan{text: text[start:end], start: offsets[start], end: offsets[end]}
			}
		}
		result.groups = append(result.groups, groups)
	}
	return result
}
func FindRaw(regex any, text string) any        { return regexFind(regex, text, 1) }
func FindAllRaw(regex any, text string) any     { return regexFind(regex, text, -1) }
func MatchCount(batch any) int64                { return int64(len(batch.(*regexMatchBatch).groups)) }
func CaptureCount(batch any) int64              { return int64(len(batch.(*regexMatchBatch).names) - 1) }
func CaptureName(batch any, group int64) string { return batch.(*regexMatchBatch).names[group] }
func MatchText(batch any, match, group int64) string {
	return batch.(*regexMatchBatch).groups[match][group].text
}
func MatchStart(batch any, match, group int64) int64 {
	return batch.(*regexMatchBatch).groups[match][group].start
}
func MatchEnd(batch any, match, group int64) int64 {
	return batch.(*regexMatchBatch).groups[match][group].end
}
func SplitRaw(regex any, text string) any     { return regex.(*regexp.Regexp).Split(text, -1) }
func SplitCount(batch any) int64              { return int64(len(batch.([]string))) }
func SplitItem(batch any, index int64) string { return batch.([]string)[index] }
