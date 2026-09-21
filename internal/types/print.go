package types

import (
	"fmt"
	"strings"
)

func SurfaceName(name string) string {
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		return name[i+1:]
	}
	return name
}

// Printer renders types for humans and goldens. Metavariables are
// normalized to a, b, c… in first-appearance order (Number-kinded ones
// print as `number`, `number2`, …); empty effect rows and empty Pred lists
// are omitted — the doc/design.md, "Core and evidence invariants" golden-stability discipline.
type Printer struct {
	names   map[int]string
	general int
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
			return SurfaceName(t.Name)
		}
		if n := tupleArity(t.Name); n == len(t.Args) {
			parts := make([]string, n)
			for i, a := range t.Args {
				parts[i] = p.render(a)
			}
			return "(" + strings.Join(parts, ", ") + ")"
		}
		parts := []string{SurfaceName(t.Name)}
		for _, a := range t.Args {
			parts = append(parts, p.atom(a))
		}
		return strings.Join(parts, " ")
	case *TFun:
		arrow := "->"
		showEff := len(t.Eff.Labels) > 0
		if v, ok := t.Eff.Tail.(*TVar); ok && p.rows[v.ID] > 1 {
			showEff = true
		}
		if showEff {
			parts := make([]string, len(t.Eff.Labels))
			for i, l := range t.Eff.Labels {
				parts[i] = SurfaceName(l.Name)
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
		parts[i] = SurfaceName(l.Name)
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

// A row variable is elided from an arrow that is its only occurrence, since
// there it only says the arrow performs whatever its caller allows. Counting
// every occurrence, an effect-indexed value's row argument included, keeps the
// arrow that shares one with such a value printing the row it performs.
func (p *Printer) countRows(t Type) {
	switch t := t.(type) {
	case *TVar:
		if t.Kind == RowVar {
			p.rows[t.ID]++
		}
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
		return "(" + p.render(t) + ")"
	case *TCon:
		// A tuple is already bracketed, so it never needs another pair.
		if len(t.Args) > 0 && tupleArity(t.Name) != len(t.Args) {
			return "(" + p.render(t) + ")"
		}
	}
	return p.render(t)
}

// funArg parenthesizes only functions: type application binds tighter than
// `->`, so `Maybe a -> a` needs no parens.
func (p *Printer) funArg(t Type) string {
	if _, ok := t.(*TFun); ok {
		return "(" + p.render(t) + ")"
	}
	return p.render(t)
}

func (p *Printer) varName(v *TVar) string {
	if n, ok := p.names[v.ID]; ok {
		return n
	}
	var n string
	switch v.Kind {
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

// Pred renders a constraint the way it is written in source: the class name
// applied to a type that is parenthesized when an application would otherwise
// read as two arguments, as in `Eq (List a)`.
func (p *Printer) Pred(class string, t Type) string {
	return SurfaceName(class) + " " + p.atom(t)
}

// ShowPred renders one constraint with a fresh printer.
func ShowPred(class string, t Type) string { return NewPrinter().Pred(class, t) }

func (p *Printer) Scheme(s Scheme) string {
	body := p.Type(s.Body)
	if len(s.Preds) == 0 {
		return body
	}
	parts := make([]string, len(s.Preds))
	for i, pred := range s.Preds {
		parts[i] = p.Pred(pred.Class, pred.Ty)
	}
	context := strings.Join(parts, ", ")
	if len(parts) > 1 {
		context = "(" + context + ")"
	}
	return context + " => " + body
}

func ShowScheme(s Scheme) string { return NewPrinter().Scheme(s) }

// tupleArity reports the element count of a bundled tuple type, whose surface
// spelling is `(a, b)` rather than its constructor name. The parser writes
// these same names when it lowers tuple syntax.
func tupleArity(name string) int {
	switch name {
	case "Tuple.Pair":
		return 2
	case "Tuple.Triple":
		return 3
	}
	return 0
}
