package natives

import (
	stdlib "github.com/waj/fango/stdlib"
)

// evalBasics implements the interpreter half of the scalar Basics natives.
func evalBasics(name string, left, right any) any {
	switch l := left.(type) {
	case int64:
		r := right.(int64)
		switch name {
		case "add":
			return l + r
		case "sub":
			return l - r
		case "mul":
			return l * r
		case "eq":
			return l == r
		case "lt":
			return l < r
		case "gt":
			return l > r
		case "le":
			return l <= r
		case "ge":
			return l >= r
		}
	case float64:
		r := right.(float64)
		switch name {
		case "add":
			return l + r
		case "sub":
			return l - r
		case "mul":
			return l * r
		case "eq":
			return l == r
		case "lt":
			return l < r
		case "gt":
			return l > r
		case "le":
			return l <= r
		case "ge":
			return l >= r
		}
	case string:
		r := right.(string)
		switch name {
		case "eq":
			return l == r
		case "lt":
			return l < r
		case "gt":
			return l > r
		case "le":
			return l <= r
		case "ge":
			return l >= r
		}
	case bool:
		r := right.(bool)
		if name == "eq" {
			return l == r
		}
	}
	panic("invalid Basics native application: " + name)
}

func lineText(text string) string { return stdlib.LineText(text) }

func lineEnding(text string) string { return stdlib.LineEnding(text) }
