package native

import "testing"

func TestRegexNativeCapturesAndScalarOffsets(t *testing.T) {
	compiled := CompileRaw(`(?P<word>é二)(x)?()`)
	if !CompileSucceeded(compiled) {
		t.Fatal(CompileMessage(compiled))
	}
	batch := FindAllRaw(CompiledRegex(compiled), "😀é二!é二").(*regexMatchBatch)
	if len(batch.groups) != 2 {
		t.Fatal(batch.groups)
	}
	for i, start := range []int64{1, 4} {
		groups := batch.groups[i]
		if groups[0].text != "é二" || groups[0].start != start || groups[0].end != start+2 {
			t.Fatal(groups)
		}
		if groups[2].start != -1 || groups[3].start != start+2 || groups[3].text != "" {
			t.Fatal(groups)
		}
	}
	if batch.names[1] != "word" {
		t.Fatal(batch.names)
	}
	if CompileSucceeded(CompileRaw("[")) || CompileMessage(CompileRaw("[")) == "" {
		t.Fatal("invalid regex accepted")
	}
}

func TestRegexNativeEmptyMatchesAndReplacement(t *testing.T) {
	regex := CompiledRegex(CompileRaw(""))
	batch := FindAllRaw(regex, "é😀").(*regexMatchBatch)
	if len(batch.groups) != 3 || batch.groups[2][0].start != 2 {
		t.Fatal(batch.groups)
	}
	captured := CompiledRegex(CompileRaw(`(?P<word>\w+)`))
	if got := ReplaceAll(captured, "${word}!", "hi there"); got != "hi! there!" {
		t.Fatal(got)
	}
	if got := ReplaceAllLiteral(captured, "$1", "hi"); got != "$1" {
		t.Fatal(got)
	}
	if MatchCount(FindRaw(captured, "!")) != 0 {
		t.Fatal("unexpected match")
	}
}
