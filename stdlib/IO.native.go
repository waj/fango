package native

import "strings"

func LineEnding(s string) string {
	if strings.HasSuffix(s, "\r\n") {
		return "\r\n"
	}
	if strings.HasSuffix(s, "\n") {
		return "\n"
	}
	return ""
}

func LineText(s string) string { return strings.TrimSuffix(s, LineEnding(s)) }
