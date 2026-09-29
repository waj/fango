package eval

import (
	"fmt"
	"github.com/waj/fango/internal/meta"
	"github.com/waj/fango/runtime/fangort"
)

// FreezeAttribute severs all links to executable interpreter state.
func FreezeAttribute(v any) (*meta.Data, error) {
	d := &meta.Data{}
	switch v := v.(type) {
	case int64:
		d.Kind, d.Integer = "int", v
	case float64:
		d.Kind, d.Float = "float", v
	case string:
		d.Kind, d.Text = "string", v
	case bool:
		d.Kind, d.Boolean = "bool", v
	case rune:
		d.Kind, d.Char = "char", v
	case *meta.Code:
		d.Kind, d.Code = "code", v
	case *meta.TypeRepr:
		d.Kind, d.Reflected = "type", v
	case struct{}:
		d.Kind = "unit"
	case *CtorVal:
		d.Kind, d.Ctor = "ctor", v.Ctor
		for _, f := range v.Fields {
			child, err := FreezeAttribute(f)
			if err != nil {
				return nil, err
			}
			d.Fields = append(d.Fields, child)
		}
	case fangort.List[Value]:
		d.Kind = "list"
		for ; !v.IsEmpty(); v = v.Tail() {
			child, err := FreezeAttribute(v.Head())
			if err != nil {
				return nil, err
			}
			d.Fields = append(d.Fields, child)
		}
	default:
		return nil, fmt.Errorf("Attributes can store data and quoted code, but not functions, native handles, or resources (found %T).", v)
	}
	return d, nil
}

func thawAttribute(d *meta.Data) any {
	switch d.Kind {
	case "int":
		return d.Integer
	case "float":
		return d.Float
	case "string":
		return d.Text
	case "bool":
		return d.Boolean
	case "char":
		return d.Char
	case "code":
		return d.Code
	case "type":
		return d.Reflected
	case "unit":
		return struct{}{}
	case "ctor":
		fields := make([]Value, len(d.Fields))
		for i, f := range d.Fields {
			fields[i] = thawAttribute(f)
		}
		return &CtorVal{Ctor: d.Ctor, Fields: fields}
	case "list":
		result := fangort.ListNil[Value]()
		for i := len(d.Fields) - 1; i >= 0; i-- {
			result = fangort.ListCons(thawAttribute(d.Fields[i]), result)
		}
		return result
	default:
		panic("eval: malformed stored attribute")
	}
}
