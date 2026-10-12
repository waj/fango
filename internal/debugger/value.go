package debugger

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	PreviewDepth       = 4
	PreviewItems       = 50
	PreviewNodes       = 512
	PreviewStringBytes = 256
)

type ValueKind string

const (
	NumberValue      ValueKind = "number"
	BoolValue        ValueKind = "bool"
	CharValue        ValueKind = "char"
	StringValue      ValueKind = "string"
	UnitValue        ValueKind = "unit"
	ConstructorValue ValueKind = "constructor"
	RecordValue      ValueKind = "record"
	TupleValue       ValueKind = "tuple"
	ListValue        ValueKind = "list"
	FunctionValue    ValueKind = "function"
	OpaqueValue      ValueKind = "opaque"
	UnavailableValue ValueKind = "unavailable"
	CycleValue       ValueKind = "cycle"
	LimitValue       ValueKind = "limit"
)

// Value is a bounded, detached description, never a target value or an address.
// Text holds scalar data, a constructor/type name, or an unavailable reason.
// Fields are ordered source children; names matter only for records. Truncated
// means more data exists beyond the supplied string or children. Lazy expansion
// is carried by Variable.Handle and remains owned by the paused backend.
type Value struct {
	Kind      ValueKind
	Text      string
	Fields    []ValueField
	Truncated bool
}

type ValueField struct {
	Name  string
	Value Value
}

// StringPreview bounds transport size as well as display size. A native reader
// can supply a partial UTF-8 suffix; omit that suffix rather than invent a rune.
func StringPreview(text string, truncated bool) Value {
	if len(text) > PreviewStringBytes {
		text, truncated = text[:PreviewStringBytes], true
	}
	if truncated && len(text) != 0 {
		start := len(text) - 1
		for start > 0 && !utf8.RuneStart(text[start]) {
			start--
		}
		if !utf8.FullRuneInString(text[start:]) {
			text = text[:start]
		}
	}
	return Value{Kind: StringValue, Text: text, Truncated: truncated}
}

// RenderValue formats source structure without fetching children or executing
// user code. Its own bounds also protect frontends from oversized descriptions.
func RenderValue(v Value) string {
	nodes := PreviewNodes
	return renderValue(v, PreviewDepth, &nodes)
}

func renderValue(v Value, depth int, nodes *int) string {
	if *nodes <= 0 || depth < 0 {
		return "…"
	}
	*nodes--
	switch v.Kind {
	case NumberValue:
		return v.Text
	case BoolValue:
		if v.Text == "true" {
			return "True"
		}
		return "False"
	case CharValue:
		r, _ := utf8.DecodeRuneInString(v.Text)
		return strconv.QuoteRune(r)
	case StringValue:
		v = StringPreview(v.Text, v.Truncated)
		out := strings.ReplaceAll(strconv.Quote(v.Text), "#{", "\\#{")
		if v.Truncated {
			out += "…"
		}
		return out
	case UnitValue:
		return "()"
	case FunctionValue:
		return "<function>"
	case OpaqueValue:
		if v.Text != "" {
			return "<opaque " + v.Text + ">"
		}
		return "<opaque>"
	case CycleValue:
		return "<cycle>"
	case LimitValue:
		return "…"
	case UnavailableValue, "":
		if v.Text != "" {
			return "<" + v.Text + ">"
		}
		return "<unavailable>"
	case ConstructorValue, RecordValue, TupleValue, ListValue:
		truncated := v.Truncated
		var parts []string
		if depth == 0 && len(v.Fields) > 0 {
			truncated = true
		} else {
			for i, field := range v.Fields {
				if i == PreviewItems || *nodes <= 0 {
					truncated = true
					break
				}
				part := renderValue(field.Value, depth-1, nodes)
				if v.Kind == RecordValue {
					part = field.Name + " = " + part
				}
				if v.Kind == ConstructorValue && field.Value.Kind == ConstructorValue && (len(field.Value.Fields) > 0 || field.Value.Truncated) {
					part = "(" + part + ")"
				}
				parts = append(parts, part)
			}
		}
		if truncated {
			parts = append(parts, "…")
		}
		switch v.Kind {
		case RecordValue:
			if len(parts) == 0 {
				return "{}"
			}
			return "{ " + strings.Join(parts, ", ") + " }"
		case TupleValue:
			return "(" + strings.Join(parts, ", ") + ")"
		case ListValue:
			return "[" + strings.Join(parts, ", ") + "]"
		default:
			if len(parts) == 0 {
				return v.Text
			}
			return v.Text + " " + strings.Join(parts, " ")
		}
	default:
		return "<opaque>"
	}
}
