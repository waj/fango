package ast

import (
	"fmt"
	"strconv"
	"strings"
)

// Dump renders the module as indented S-expressions — the golden-test
// format. Expressions print on one line; declarations get a line each.
// This format is frozen: changing it churns every parse golden.
func Dump(m *Module) string {
	var b strings.Builder
	b.WriteString("(module")
	if m.NoPrelude {
		b.WriteString(" (pragma no-prelude)")
	}
	if m.Header != nil {
		fmt.Fprintf(&b, " %s", m.Header.Name)
		b.WriteString(" (exposing")
		if m.Header.Exposing.All {
			b.WriteString(" ..")
		}
		for _, item := range m.Header.Exposing.Items {
			fmt.Fprintf(&b, " %s", item.Name)
			if item.All {
				b.WriteString("(..)")
			}
		}
		b.WriteString(")")
	}
	for _, im := range m.Imports {
		fmt.Fprintf(&b, "\n  (import %s", im.Module)
		if im.Alias != "" {
			fmt.Fprintf(&b, " (as %s)", im.Alias)
		}
		if im.Exposing != nil {
			b.WriteString(" (exposing")
			if im.Exposing.All {
				b.WriteString(" ..")
			}
			for _, item := range im.Exposing.Items {
				fmt.Fprintf(&b, " %s", item.Name)
				if item.All {
					b.WriteString("(..)")
				}
			}
			b.WriteString(")")
		}
		b.WriteString(")")
	}
	for _, d := range m.Decls {
		b.WriteString("\n  ")
		b.WriteString(dumpDecl(d))
	}
	b.WriteString(")\n")
	return b.String()
}

func dumpDecl(d Decl) string {
	switch d := d.(type) {
	case *ClassDecl:
		parts := []string{"(class", d.Name, d.Param.Name}
		for _, m := range d.Methods {
			parts = append(parts, "("+m.Name+" "+DumpTypeExpr(m.Type)+")")
		}
		for _, m := range d.Defaults {
			parts = append(parts, dumpDecl(m))
		}
		return strings.Join(parts, " ") + ")"
	case *InstanceDecl:
		parts := []string{"(instance", dumpPreds(d.Preds), d.Head.Class, DumpTypeExpr(d.Head.Ty)}
		for _, m := range d.Methods {
			parts = append(parts, dumpDecl(m))
		}
		return strings.Join(parts, " ") + ")"
	case *DeriverDecl:
		parts := []string{"(deriver", d.Class}
		for _, m := range d.Methods {
			parts = append(parts, dumpDecl(m))
		}
		return strings.Join(parts, " ") + ")"
	case *ValueDecl:
		var b strings.Builder
		fmt.Fprintf(&b, "(def %s", d.Name)
		if d.ScopedRow != "" {
			fmt.Fprintf(&b, " (pragma scoped %s)", d.ScopedRow)
		}
		if p := dumpPatternParams(d.Params); p != "" && len(d.Equations) == 0 {
			fmt.Fprintf(&b, " %s", p)
		}
		if d.Ann != nil {
			fmt.Fprintf(&b, " (ann %s%s)", dumpPreds(d.Ann.Preds), DumpTypeExpr(d.Ann.Type))
		}
		if d.Native != nil {
			b.WriteString(" (native")
			if d.Native.Template != nil {
				fmt.Fprintf(&b, " %q", *d.Native.Template)
			}
			b.WriteString(")")
		} else if len(d.Equations) > 0 {
			b.WriteString(" (equations")
			for _, eq := range d.Equations {
				fmt.Fprintf(&b, " (equation %s %s)", dumpEquationParams(eq.Params), DumpExpr(eq.Body))
			}
			b.WriteString(")")
		} else {
			fmt.Fprintf(&b, " %s", DumpExpr(d.Body))
		}
		b.WriteString(")")
		return b.String()
	case *PatternDecl:
		return fmt.Sprintf("(pattern-def %s %s)", DumpPattern(d.Pattern), DumpExpr(d.Body))
	case *TypeDecl:
		var b strings.Builder
		fmt.Fprintf(&b, "(type %s", d.Name)
		b.WriteString(dumpAttributes(d.Attributes))

		if d.Resource {
			b.WriteString(" (pragma resource)")
		}
		if p := dumpParams(d.Params); p != "" {
			fmt.Fprintf(&b, " %s", p)
		}
		if len(d.Deriving) > 0 {
			b.WriteString(" (deriving")
			for _, c := range d.Deriving {
				fmt.Fprintf(&b, " %s", c.Name)
			}
			b.WriteString(")")
		}
		if d.RecordFields != nil {
			b.WriteString(" (record")
			for _, f := range d.RecordFields {
				fmt.Fprintf(&b, " (field %s%s %s)", f.Name, dumpAttributes(f.Attributes), DumpTypeExpr(f.Type))
			}
			b.WriteString(")")
		}
		for _, c := range d.Ctors {
			fmt.Fprintf(&b, " (ctor %s%s", c.Name, dumpAttributes(c.Attributes))
			for i, a := range c.Args {
				if i < len(c.FieldAttributes) {
					b.WriteString(dumpAttributes(c.FieldAttributes[i]))
				}
				fmt.Fprintf(&b, " %s", DumpTypeExpr(a))
			}
			b.WriteString(")")
		}
		b.WriteString(")")
		return b.String()
	case *EffectDecl:
		var b strings.Builder
		fmt.Fprintf(&b, "(effect %s", d.Name)

		if p := dumpParams(d.Params); p != "" {
			fmt.Fprintf(&b, " %s", p)
		}
		for _, op := range d.Ops {
			kind := "op"
			if op.Abort {
				kind = "abort-op"
			}
			fmt.Fprintf(&b, " (%s %s %s", kind, op.Name, DumpTypeExpr(op.Type))
			if op.Native != nil {
				b.WriteString(" (native")
				if op.Native.Template != nil {
					fmt.Fprintf(&b, " %q", *op.Native.Template)
				}
				b.WriteString(")")
			}
			b.WriteString(")")
		}
		b.WriteString(")")
		return b.String()
	case *FixityDecl:
		return fmt.Sprintf("(%s %d %s)", d.Assoc, d.Prec, d.Op)
	default:
		panic(fmt.Sprintf("ast.dumpDecl: unhandled %T", d))
	}
}

// dumpParams renders "(params x y)" or "" — the clause appears only when
// non-empty, keeping nullary declarations' golden form compact.
func dumpParams(ps []Param) string {
	if len(ps) == 0 {
		return ""
	}
	names := make([]string, len(ps))
	for i, p := range ps {
		names[i] = p.Name
	}
	return "(params " + strings.Join(names, " ") + ")"
}

func dumpPatternParams(ps []Pattern) string {
	if len(ps) == 0 {
		return ""
	}
	legacy := true
	names := make([]string, len(ps))
	for i, p := range ps {
		switch p := p.(type) {
		case *PVar:
			names[i] = p.Name
		case *PWildcard:
			names[i] = "_"
		case *PUnit:
			names[i] = "()"
		default:
			legacy = false
		}
	}
	if legacy {
		return "(params " + strings.Join(names, " ") + ")"
	}
	return dumpEquationParams(ps)
}

// dumpEquationParams renders one row of an equation group. Groups always take
// the explicit pattern form: only an ungrouped definition keeps the historical
// identifier-only `(params …)` spelling.
func dumpEquationParams(ps []Pattern) string {
	if len(ps) == 0 {
		return ""
	}
	parts := make([]string, len(ps))
	for i, p := range ps {
		parts[i] = DumpPattern(p)
	}
	return "(patterns " + strings.Join(parts, " ") + ")"
}

// dumpLocalBind renders one block statement. Identifier-only single bindings
// keep their historical shape; groups and destructuring get their own forms.
func dumpLocalBind(bind LocalBind) string {
	if bind.Pattern != nil {
		return fmt.Sprintf("(pattern-bind %s %s)", DumpPattern(bind.Pattern), DumpExpr(bind.Body))
	}
	var b strings.Builder
	if len(bind.Equations) > 0 {
		fmt.Fprintf(&b, "(bind-group %s", bind.Name)
		if bind.Ann != nil {
			fmt.Fprintf(&b, " (ann %s%s)", dumpPreds(bind.Ann.Preds), DumpTypeExpr(bind.Ann.Type))
		}
		for _, eq := range bind.Equations {
			fmt.Fprintf(&b, " (equation %s %s)", dumpEquationParams(eq.Params), DumpExpr(eq.Body))
		}
		b.WriteString(")")
		return b.String()
	}
	fmt.Fprintf(&b, "(bind %s", bind.Name)
	if p := dumpPatternParams(bind.Params); p != "" {
		fmt.Fprintf(&b, " %s", p)
	}
	if bind.Ann != nil {
		fmt.Fprintf(&b, " (ann %s%s)", dumpPreds(bind.Ann.Preds), DumpTypeExpr(bind.Ann.Type))
	}
	fmt.Fprintf(&b, " %s)", DumpExpr(bind.Body))
	return b.String()
}

func DumpTypeExpr(t TypeExpr) string {
	switch t := t.(type) {
	case *TName:
		return t.Name
	case *TVarName:
		return t.Name
	case *TFunExpr:
		if t.Eff != nil {
			return fmt.Sprintf("(-> %s %s %s)", DumpTypeExpr(t.Arg), dumpEffRow(t.Eff), DumpTypeExpr(t.Ret))
		}
		return fmt.Sprintf("(-> %s %s)", DumpTypeExpr(t.Arg), DumpTypeExpr(t.Ret))
	case *TApp:
		var b strings.Builder
		fmt.Fprintf(&b, "(%s", t.Name)
		for _, a := range t.Args {
			fmt.Fprintf(&b, " %s", DumpTypeExpr(a))
		}
		b.WriteString(")")
		return b.String()
	case *TRow:
		return dumpEffRow(t.Row)
	default:
		panic(fmt.Sprintf("ast.DumpTypeExpr: unhandled %T", t))
	}
}

// dumpEffRow renders a row identically wherever it stands: on an arrow, or
// alone as a type argument.
func dumpEffRow(r *EffRow) string {
	var b strings.Builder
	b.WriteString("(effects")
	for _, label := range r.Labels {
		fmt.Fprintf(&b, " (%s", label.Name)
		for _, arg := range label.Args {
			fmt.Fprintf(&b, " %s", DumpTypeExpr(arg))
		}
		b.WriteString(")")
	}
	if r.Tail != "" {
		fmt.Fprintf(&b, " (tail %s)", r.Tail)
	}
	b.WriteString(")")
	return b.String()
}

// DumpPattern renders a pattern: `_`, `(pvar x)`, `(pint 3)`, and
// `(pctor Just (pvar x))`.
func DumpPattern(p Pattern) string {
	switch p := p.(type) {
	case *PWildcard:
		return "_"
	case *PUnit:
		return "(punit)"
	case *PVar:
		return fmt.Sprintf("(pvar %s)", p.Name)
	case *PInt:
		return fmt.Sprintf("(pint %d)", p.Value)
	case *PFloat:
		return fmt.Sprintf("(pfloat %s)", strconv.FormatFloat(p.Value, 'g', -1, 64))
	case *PString:
		return fmt.Sprintf("(pstring %q)", p.Value)
	case *PChar:
		return fmt.Sprintf("(pchar %q)", p.Value)
	case *PPin:
		return fmt.Sprintf("(ppin %s)", p.Name)
	case *PRecord:
		var b strings.Builder
		if p.Name == "" {
			b.WriteString("(precord-inferred")
		} else {
			fmt.Fprintf(&b, "(precord %s", p.Name)
		}
		for _, f := range p.Fields {
			fmt.Fprintf(&b, " (%s %s)", f.Name, DumpPattern(f.Pattern))
		}
		b.WriteString(")")
		return b.String()
	case *PCtor:
		var b strings.Builder
		fmt.Fprintf(&b, "(pctor %s", p.Name)
		for _, a := range p.Args {
			fmt.Fprintf(&b, " %s", DumpPattern(a))
		}
		b.WriteString(")")
		return b.String()
	default:
		panic(fmt.Sprintf("ast.DumpPattern: unhandled %T", p))
	}
}

func DumpExpr(e Expr) string {
	switch e := e.(type) {
	case *IntLit:
		return fmt.Sprintf("(int %d)", e.Value)
	case *FloatLit:
		return fmt.Sprintf("(float %s)", strconv.FormatFloat(e.Value, 'g', -1, 64))
	case *RegexLit:
		return fmt.Sprintf("(regex %q)", e.Value)
	case *StringLit:
		return fmt.Sprintf("(string %q)", e.Value)
	case *StringInterpolation:
		var b strings.Builder
		b.WriteString("(interpolation")
		for i, text := range e.Segments {
			fmt.Fprintf(&b, " %q", text)
			if i < len(e.Exprs) {
				b.WriteByte(' ')
				b.WriteString(DumpExpr(e.Exprs[i]))
			}
		}
		return b.String() + ")"
	case *CharLit:
		return fmt.Sprintf("(char %q)", e.Value)
	case *UnitLit:
		return "(unit)"
	case *Var:
		return fmt.Sprintf("(var %s)", e.Name)
	case *Ctor:
		if e.Witness != nil {
			return fmt.Sprintf("(witness %s)", DumpTypeExpr(e.Witness))
		}
		return fmt.Sprintf("(ctor %s)", e.Name)
	case *RecordLit:
		var b strings.Builder
		if e.Name == "" {
			b.WriteString("(record-inferred")
		} else {
			fmt.Fprintf(&b, "(record %s", e.Name)
		}
		for _, f := range e.Fields {
			fmt.Fprintf(&b, " (%s %s)", f.Name, DumpExpr(f.Value))
		}
		return b.String() + ")"
	case *RecordGet:
		return fmt.Sprintf("(field %s %s)", e.Field, DumpExpr(e.Record))
	case *RecordUpdate:
		var b strings.Builder
		fmt.Fprintf(&b, "(update %s", DumpExpr(e.Record))
		for _, f := range e.Fields {
			fmt.Fprintf(&b, " (%s %s)", f.Name, DumpExpr(f.Value))
		}
		return b.String() + ")"
	case *App:
		return fmt.Sprintf("(app %s %s)", DumpExpr(e.Fn), DumpExpr(e.Arg))
	case *Neg:
		return fmt.Sprintf("(neg %s)", DumpExpr(e.Operand))
	case *BinOp:
		return fmt.Sprintf("(binop %s %s %s)", e.Op, DumpExpr(e.L), DumpExpr(e.R))
	case *OpChain:
		// An unresolved chain only reaches a dump when fixity resolution was
		// skipped, so it prints its flat shape rather than pretending to be
		// a tree.
		var b strings.Builder
		b.WriteString("(opchain " + DumpExpr(e.Operands[0]))
		for i, op := range e.Ops {
			b.WriteString(" " + op.Op + " " + DumpExpr(e.Operands[i+1]))
		}
		b.WriteString(")")
		return b.String()
	case *If:
		return fmt.Sprintf("(if %s %s %s)", DumpExpr(e.Cond), DumpExpr(e.Then), DumpExpr(e.Else))
	case *Block:
		var b strings.Builder
		b.WriteString("(block")
		// A block whose statements are all bindings carries no Items; the
		// two spellings dump identically.
		if len(e.Items) > 0 {
			for _, item := range e.Items {
				if item.Expr != nil {
					fmt.Fprintf(&b, " (expr %s)", DumpExpr(item.Expr))
					continue
				}
				b.WriteString(" " + dumpLocalBind(e.Binds[item.BindIndex]))
			}
		} else {
			for _, bind := range e.Binds {
				b.WriteString(" " + dumpLocalBind(bind))
			}
		}
		if e.MissingResultAt.File != nil {
			b.WriteString(" (missing-result))")
		} else {
			fmt.Fprintf(&b, " %s)", DumpExpr(e.Result))
		}
		return b.String()
	case *Case:
		var b strings.Builder
		fmt.Fprintf(&b, "(case %s", DumpExpr(e.Scrutinee))
		for _, br := range e.Branches {
			fmt.Fprintf(&b, " (branch %s %s)", DumpPattern(br.Pattern), DumpExpr(br.Body))
		}
		b.WriteString(")")
		return b.String()
	case *Lambda:
		p := dumpPatternParams(e.Params)
		if strings.HasPrefix(p, "(params ") {
			p = "(" + strings.TrimSuffix(strings.TrimPrefix(p, "(params "), ")") + ")"
		}
		if e.Use != nil {
			return fmt.Sprintf("(use-lambda %s %s)", p, DumpExpr(e.Body))
		}
		return fmt.Sprintf("(lambda %s %s)", p, DumpExpr(e.Body))
	case *Handle:
		var b strings.Builder
		fmt.Fprintf(&b, "(handle %s", DumpExpr(e.Body))
		if e.State != nil {
			fmt.Fprintf(&b, " (state %s %s)", e.State.Name, DumpExpr(e.State.Initial))
		}
		for _, clause := range e.Clauses {
			sig := ""
			if clause.Signature != nil {
				sig = fmt.Sprintf(" (signature %s%s)", dumpPreds(clause.Signature.Preds), DumpTypeExpr(clause.Signature.Type))
			}
			if len(clause.Equations) > 0 {
				fmt.Fprintf(&b, " (clause-group %s%s", clause.Op, sig)
				for _, eq := range clause.Equations {
					fmt.Fprintf(&b, " (equation %s %s)", dumpEquationParams(eq.Params), DumpExpr(eq.Body))
				}
				b.WriteString(")")
			} else {
				fmt.Fprintf(&b, " (clause %s%s", clause.Op, sig)
				if p := dumpPatternParams(clause.Params); p != "" {
					fmt.Fprintf(&b, " %s", p)
				}
				fmt.Fprintf(&b, " %s)", DumpExpr(clause.Body))
			}
		}
		if e.Return != nil {
			if len(e.Return.Equations) > 0 {
				b.WriteString(" (return-group")
				for _, eq := range e.Return.Equations {
					fmt.Fprintf(&b, " (equation %s %s)", dumpEquationParams(eq.Params), DumpExpr(eq.Body))
				}
				b.WriteString(")")
			} else {
				name := DumpPattern(e.Return.Param)
				if v, ok := e.Return.Param.(*PVar); ok {
					name = v.Name
				} else if _, ok := e.Return.Param.(*PWildcard); ok {
					name = "_"
				} else if _, ok := e.Return.Param.(*PUnit); ok {
					name = "()"
				}
				fmt.Fprintf(&b, " (return %s %s)", name, DumpExpr(e.Return.Body))
			}
		}
		b.WriteString(")")
		return b.String()
	case *Resume:
		if e.NextState != nil {
			return fmt.Sprintf("(resume-with %s)", DumpExpr(e.NextState))
		}
		return "(resume)"
	case *Quote:
		return fmt.Sprintf("(quote %s)", DumpExpr(e.Body))
	case *Splice:
		return fmt.Sprintf("(splice %s)", DumpExpr(e.Operand))
	case *TypeOf:
		return fmt.Sprintf("(typeOf %s)", DumpTypeExpr(e.Ty))
	case *MetaValue:
		return "(meta-value)"
	default:
		panic(fmt.Sprintf("ast.DumpExpr: unhandled %T", e))
	}
}

func dumpAttributes(groups []AttributeGroup) string {
	var b strings.Builder
	for _, g := range groups {
		b.WriteString(" (attributes")
		for _, e := range g.Exprs {
			b.WriteString(" " + DumpExpr(e))
		}
		b.WriteString(")")
	}
	return b.String()
}
