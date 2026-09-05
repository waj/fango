package native

// EvalBasics is the interpreter implementation of the inline Basics
// templates. equal supplies structural equality for interpreter ADT values.
func EvalBasics(name string, left, right any, equal func(any, any) bool) any {
	if l, ok := left.(int64); ok {
		if _, float := right.(float64); float {
			left = float64(l)
		}
	}
	if r, ok := right.(int64); ok {
		if _, float := left.(float64); float {
			right = float64(r)
		}
	}
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
		case "neq":
			return l != r
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
		case "fdiv":
			return l / r
		case "eq":
			return l == r
		case "neq":
			return l != r
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
		case "append":
			return l + r
		case "eq":
			return l == r
		case "neq":
			return l != r
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
		if name == "neq" {
			return l != r
		}
	}
	if name == "eq" {
		return equal(left, right)
	}
	if name == "neq" {
		return !equal(left, right)
	}
	panic("invalid Basics native application: " + name)
}
