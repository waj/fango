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
	row     int
	rows    map[int]int
}

func NewPrinter() *Printer {
	return &Printer{names: map[int]string{}}
}

func (p *Printer) Type(t Type) string {
	p.rows = map[int]int{}
	p.countRows(t)
	return p.render(t)
}

func (p *Printer) render(t Type) string {
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
		if c, ok := t.Arg.(*TCon); ok && c.Name == "()" && (len(t.Eff.Labels) > 0 || t.Eff.Tail != nil) {
			return p.rowText(t.Eff) + " " + p.render(t.Ret)
		}
		arrow := "->"
		showEff := len(t.Eff.Labels) > 0
		if v, ok := t.Eff.Tail.(*TVar); ok && p.rows[v.ID] > 1 {
			showEff = true
		}
		if showEff {
			parts := make([]string, len(t.Eff.Labels))
			for i, l := range t.Eff.Labels {
				parts[i] = l.Name
				for _, a := range l.Args {
					parts[i] += " " + p.atom(a)
				}
			}
			inside := strings.Join(parts, ", ")
			if v, ok := t.Eff.Tail.(*TVar); ok && p.rows[v.ID] > 1 {
				if inside != "" {
					inside += " | "
				}
				inside += p.varName(v)
			}
			arrow = "->{" + inside + "}"
		}
		return fmt.Sprintf("%s %s %s", p.funArg(t.Arg), arrow, p.render(t.Ret))
	case Row:
		return p.rowText(t)
	default:
		panic(fmt.Sprintf("types.Printer: unhandled %T", t))
	}
}

func (p *Printer) rowText(r Row) string {
	parts := make([]string, len(r.Labels))
	for i, l := range r.Labels {
		parts[i] = l.Name
		for _, a := range l.Args {
			parts[i] += " " + p.atom(a)
		}
	}
	inside := strings.Join(parts, ", ")
	if r.Tail != nil {
		if inside != "" {
			inside += " | "
		}
		inside += p.render(r.Tail)
	}
	return "{" + inside + "}"
}

func (p *Printer) countRows(t Type) {
	switch t := t.(type) {
	case *TCon:
		for _, a := range t.Args {
			p.countRows(a)
		}
	case *TFun:
		p.countRows(t.Arg)
		p.countRows(t.Eff)
		p.countRows(t.Ret)
	case Row:
		for _, l := range t.Labels {
			for _, a := range l.Args {
				p.countRows(a)
			}
		}
		if v, ok := t.Tail.(*TVar); ok {
			p.rows[v.ID]++
		}
	}
}

// Atom renders t parenthesized when it would be ambiguous in argument
// position — constructor-field dumps and similar atom contexts.
func (p *Printer) Atom(t Type) string { return p.atom(t) }

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
	case RowVar:
		p.row++
		if p.row == 1 {
			n = "e"
		} else {
			n = fmt.Sprintf("e%d", p.row)
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
