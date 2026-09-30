package fangort

import (
	"strings"
	"unicode/utf8"
)

func StringFromList(chars List[rune]) string {
	return stringFromList(chars, func(char rune) rune { return char })
}

func StringFromValueList(chars List[any]) string {
	return stringFromList(chars, func(char any) rune { return char.(rune) })
}

func stringFromList[T any](chars List[T], char func(T) rune) string {
	var out strings.Builder
	var size int
	for rest := chars; !rest.IsEmpty(); rest = rest.Tail() {
		size += utf8.RuneLen(char(rest.Head()))
	}
	out.Grow(size)
	for ; !chars.IsEmpty(); chars = chars.Tail() {
		out.WriteRune(char(chars.Head()))
	}
	return out.String()
}

func StringConcat(parts List[string]) string {
	return stringConcat(parts, func(part string) string { return part })
}

func StringConcatValues(parts List[any]) string {
	return stringConcat(parts, func(part any) string { return part.(string) })
}

func stringConcat[T any](parts List[T], text func(T) string) string {
	var size int
	for rest := parts; !rest.IsEmpty(); rest = rest.Tail() {
		size += len(text(rest.Head()))
	}
	var out strings.Builder
	out.Grow(size)
	for ; !parts.IsEmpty(); parts = parts.Tail() {
		out.WriteString(text(parts.Head()))
	}
	return out.String()
}
