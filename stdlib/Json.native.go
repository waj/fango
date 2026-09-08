package native

import (
	"encoding/json"
	"unicode/utf8"
)

func JsonString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func JsonFloat(v float64) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func StringTokenLength(text string) int64 {
	if len(text) == 0 || text[0] != '"' {
		return -1
	}
	escaped := false
	for i := 1; i < len(text); i++ {
		if escaped {
			escaped = false
			continue
		}
		switch text[i] {
		case '\\':
			escaped = true
		case '"':
			token := text[:i+1]
			var value string
			if json.Unmarshal([]byte(token), &value) != nil {
				return -1
			}
			return int64(utf8.RuneCountInString(token))
		}
	}
	return -1
}

func StringTokenValue(token string) string {
	var value string
	if err := json.Unmarshal([]byte(token), &value); err != nil {
		panic(err)
	}
	return value
}
