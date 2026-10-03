package native

import "strings"

func StandardHandle(endpoint int64) any        { return FangoStandardIOHandle(endpoint) }
func HandleHasInput(value any) (bool, error)   { return FangoIOHandleHasInput(FangoHost, value) }
func ReadHandleLine(value any) (string, error) { return FangoReadIOHandleLine(FangoHost, value) }
func ReadHandleBytes(value any, count int64) ([]byte, error) {
	return FangoReadIOHandleBytes(FangoHost, value, count)
}
func WriteHandle(value any, text string) error {
	return FangoWriteIOHandleBytes(FangoHost, value, []byte(text))
}
func WriteHandleBytes(value any, data []byte) error {
	return FangoWriteIOHandleBytes(FangoHost, value, data)
}

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
