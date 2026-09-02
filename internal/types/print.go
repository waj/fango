package types

import (
	"fmt"
	"strings"
)

// Printer renders types for humans and goldens. Metavariables are
// normalized to a, b, c… in first-appearance order (Number-kinded ones
// print as `number`, `number2`, …); empty effect rows and empty Pred lists
// are omitted — the DESIGN.md §10.8 golden-stability discipline.
type Printer struct {
	names   map[int]string
	general int
	number  int
}

func NewPrinter() *Printer {
	return &Printer{names: map[int]string{}}
}

func (p *Printer) Type(t Type) string {
	switch t := t.(type) {
	case *TVar:
		return p.varName(t)
	case *TCon:
		if len(t.Args) == 0 {
			return t.Name
		}
		parts := []string{t.Name}
		for _, a := range t.Args {
			parts = append(parts, p.atom(a))
		}
		return strings.Join(parts, " ")
	case *TFun:
		arrow := "->"
		if !t.Eff.Empty() {
			arrow = "->{" + strings.Join(t.Eff.Labels, ", ") + "}"
		}
		return fmt.Sprintf("%s %s %s", p.funArg(t.Arg), arrow, p.Type(t.Ret))
	default:
		panic(fmt.Sprintf("types.Printer: unhandled %T", t))
	}
}

// atom parenthesizes types that would be ambiguous as a type-application
// argument: functions and nested applications (`Maybe (List a)`).
func (p *Printer) atom(t Type) string {
	switch t := t.(type) {
	case *TFun:
		return "(" + p.Type(t) + ")"
	case *TCon:
		if len(t.Args) > 0 {
			return "(" + p.Type(t) + ")"
		}
	}
	return p.Type(t)
}

// funArg parenthesizes only functions: type application binds tighter than
// `->`, so `Maybe a -> a` needs no parens.
func (p *Printer) funArg(t Type) string {
	if _, ok := t.(*TFun); ok {
		return "(" + p.Type(t) + ")"
	}
	return p.Type(t)
}

func (p *Printer) varName(v *TVar) string {
	if n, ok := p.names[v.ID]; ok {
		return n
	}
	var n string
	switch v.Kind {
	case Number:
		p.number++
		if p.number == 1 {
			n = "number"
		} else {
			n = fmt.Sprintf("number%d", p.number)
		}
	default:
		n = string(rune('a' + p.general%26))
		p.general++
	}
	p.names[v.ID] = n
	return n
}

// Show renders one type with a fresh printer (single-type contexts).
func Show(t Type) string { return NewPrinter().Type(t) }
