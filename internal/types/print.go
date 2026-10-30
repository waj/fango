package types

import (
	"fmt"
	"slices"
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
	scoped  map[int]bool // the scheme's scoped row variable, only while printing it
	// bound holds the variables given a name by Bind, and the names they
	// took, which generated names then avoid.
	bound map[int]bool
	taken map[string]bool
	// TypeName spells a type constructor's canonical name. Nil uses its
	// surface name.
	TypeName func(canonical string) string
	// Ref, when set, receives each type, class, and effect name as printed,
	// with its canonical name, and returns the text to print instead.
	Ref func(canonical, text string) string

	aliases map[string]string // each alias shown so far, with its effects

	// message, set by ForMessage, renders for a diagnostic: variables an
	// annotation named keep their names, rows on rigid variables always show,
	// and inference's own row variables stay out of sight.
	message bool
	// clashing holds surface names that stand for several types in the
	// message, which print fully qualified so the two cannot look alike.
	clashing map[string]bool
}

func (p *Printer) ref(canonical, text string) string {
	if p.Ref != nil {
		return p.Ref(canonical, text)
	}
	return text
}

func (p *Printer) label(l EffLabel) string {
	text := p.ref(l.Name, p.surface(l.Name))
	for _, a := range l.Args {
		text += " " + p.atom(a)
	}
	return text
}

func (p *Printer) typeName(name string) string {
	if p.TypeName != nil {
		return p.ref(name, p.TypeName(name))
	}
	return p.ref(name, p.surface(name))
}

// surface spells a canonical name, qualified only when its short name would
// read the same as a different type in this message.
func (p *Printer) surface(name string) string {
	if p.clashing[SurfaceName(name)] {
		return name
	}
	return SurfaceName(name)
}

// ForMessage prepares a printer for a diagnostic about ts, which must include
// every type the message will print. See the message field.
func (p *Printer) ForMessage(ts ...Type) *Printer {
	p.message = true
	canonical := map[string]string{}
	p.clashing = map[string]bool{}
	note := func(name string) {
		short := SurfaceName(name)
		if seen, ok := canonical[short]; ok && seen != name {
			p.clashing[short] = true
		}
		canonical[short] = name
	}
	var walk func(Type)
	walk = func(t Type) {
		switch t := t.(type) {
		case *TVar:
			// A written name is claimed before any variable prints, so a
			// generated name never reuses a letter the author chose.
			if t.Name != "" {
				if p.taken == nil {
					p.taken = map[string]bool{}
				}
				p.names[t.ID] = t.Name
				p.taken[t.Name] = true
			}
		case *TCon:
			note(t.Name)
			for _, a := range t.Args {
				walk(a)
			}
		case *TFun:
			walk(t.Arg)
			walk(t.Eff)
			walk(t.Ret)
		case Row:
			for _, l := range t.Labels {
				note(l.Name)
				for _, a := range l.Args {
					walk(a)
				}
			}
			if t.Tail != nil {
				walk(t.Tail)
			}
		}
	}
	for _, t := range ts {
		if t != nil {
			walk(t)
		}
	}
	return p
}

func NewPrinter() *Printer {
	return &Printer{names: map[int]string{}}
}

// Bind displays v as name, as a declaration's own parameter is written. A
// bound row variable is always shown, even on an arrow that is its only
// occurrence, because the declaration that binds it gives it meaning.
func (p *Printer) Bind(v *TVar, name string) {
	if p.bound == nil {
		p.bound, p.taken = map[int]bool{}, map[string]bool{}
	}
	p.names[v.ID] = name
	p.bound[v.ID] = true
	p.taken[name] = true
}

func (p *Printer) shownRow(v *TVar) bool {
	if p.message && v.Name != "" {
		return true
	}
	return p.rows[v.ID] > 1 || p.bound[v.ID]
}

func (p *Printer) Type(t Type) string {
	p.rows = map[int]int{}
	p.countRows(t)
	return p.render(t)
}

func (p *Printer) render(t Type) string {
	switch t := t.(type) {
	case *TVar:
		if p.scoped[t.ID] {
			return p.rowText(Row{Tail: t})
		}
		return p.varName(t)
	case *TCon:
		if len(t.Args) == 0 {
			return p.typeName(t.Name)
		}
		if n := tupleArity(t.Name); n == len(t.Args) {
			parts := make([]string, n)
			for i, a := range t.Args {
				parts[i] = p.render(a)
			}
			return "(" + strings.Join(parts, ", ") + ")"
		}
		parts := []string{p.typeName(t.Name)}
		for _, a := range t.Args {
			parts = append(parts, p.atom(a))
		}
		return strings.Join(parts, " ")
	case *TFun:
		arrow := "->"
		showEff := len(t.Eff.Labels) > 0
		if v, ok := t.Eff.Tail.(*TVar); ok && p.shownRow(v) {
			showEff = true
		}
		if showEff {
			row := t.Eff
			if v, ok := row.Tail.(*TVar); ok && !p.shownRow(v) {
				row.Tail = nil
			}
			arrow = "->" + p.rowText(row)
		}
		return fmt.Sprintf("%s %s %s", p.funArg(t.Arg), arrow, p.render(t.Ret))
	case Row:
		if p.message && len(t.Labels) == 0 {
			if v, ok := t.Tail.(*TVar); ok && v.Rigid {
				return p.varName(v)
			}
		}
		return p.rowText(t)
	default:
		panic(fmt.Sprintf("types.Printer: unhandled %T", t))
	}
}

func (p *Printer) rowText(r Row) string {
	parts := p.foldedLabels(r.Labels)
	if v, ok := r.Tail.(*TVar); ok && p.scoped[v.ID] {
		parts = append(parts, "#"+p.varName(v))
	} else if v, ok := r.Tail.(*TVar); ok && p.message && !v.Rigid {
		// Inference's own tail is an implementation detail of a message.
	} else if r.Tail != nil {
		parts = append(parts, p.render(r.Tail))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// foldedLabels prints a row's labels, showing an alias in place of its members
// when a label came from it and all of its members are present.
func (p *Printer) foldedLabels(labels []EffLabel) []string {
	present := map[string]int{}
	for i, l := range labels {
		present[LabelText(l)] = i
	}
	folded := map[int]bool{}
	shown := map[string]bool{}
	var parts []string
	for _, l := range labels {
		for _, via := range l.Via {
			missing := func(m string) bool { _, ok := present[m]; return !ok }
			if shown[via.Name] || slices.ContainsFunc(via.Members, missing) {
				continue
			}
			shown[via.Name] = true
			members := make([]string, len(via.Members))
			for i, m := range via.Members {
				folded[present[m]] = true
				members[i] = p.label(labels[present[m]])
			}
			text := p.ref(via.Name, SurfaceName(via.Name))
			parts = append(parts, text)
			if p.aliases == nil {
				p.aliases = map[string]string{}
			}
			p.aliases[text] = "{" + strings.Join(members, ", ") + "}"
		}
	}
	for i, l := range labels {
		if !folded[i] {
			parts = append(parts, p.label(l))
		}
	}
	return parts
}

// AliasNotes explains each alias this printer has shown in place of its
// effects, for a report that printed types with it.
func (p *Printer) AliasNotes() []string {
	names := make([]string, 0, len(p.aliases))
	for name := range p.aliases {
		names = append(names, name)
	}
	slices.Sort(names)
	notes := make([]string, len(names))
	for i, name := range names {
		notes[i] = "Note: `" + name + "` is an alias for " + p.aliases[name]
	}
	return notes
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
	for n == "" || p.taken[n] {
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
	return p.ref(class, SurfaceName(class)) + " " + p.atom(t)
}

// ShowPred renders one constraint with a fresh printer.
func ShowPred(class string, t Type) string { return NewPrinter().Pred(class, t) }

func (p *Printer) Scheme(s Scheme) string {
	if _, ambient, _, ok := ScopedCallback(s.Body, s.ScopedArity, s.ScopedParam); s.ScopedRow != nil && ok {
		base, _ := ambient.Tail.(*TVar)
		{
			old := p.scoped
			p.scoped = map[int]bool{s.ScopedRow.ID: true}
			defer func() { p.scoped = old }()
			p.Bind(s.ScopedRow, "s")
			if base != nil {
				p.Bind(base, "e")
			}
		}
	}
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
// IsTupleName reports whether name is the bundled tuple constructor or type.
func IsTupleName(name string) bool { return tupleArity(name) != 0 }

func tupleArity(name string) int {
	switch name {
	case "Tuple.Pair":
		return 2
	case "Tuple.Triple":
		return 3
	}
	return 0
}
