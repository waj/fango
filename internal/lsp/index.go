package lsp

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/check"
	"github.com/waj/fango/internal/infer"
	"github.com/waj/fango/internal/lexer"
	"github.com/waj/fango/internal/libroot"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/token"
	"github.com/waj/fango/internal/types"
)

type symbol struct {
	span     source.Span
	path     string
	typeText string
	docs     string
}

type occurrence struct {
	span   source.Span
	target string
}

type documentIndex struct {
	file *source.File
	uses []occurrence
}

type index struct {
	root      string
	symbols   map[string]symbol
	docs      map[*source.File][]token.Comment
	documents map[string]*documentIndex
	checker   *infer.Checker
}

func newIndex(root string, result *check.Result) *index {
	i := &index{root: root, symbols: map[string]symbol{}, docs: map[*source.File][]token.Comment{}, documents: map[string]*documentIndex{}, checker: result.Checker}
	for _, module := range result.Graph.Modules {
		if module.Source == nil {
			continue
		}
		path := i.path(module.Source)
		i.documents[path] = &documentIndex{file: module.Source}
		_, comments, _ := lexer.LexWithComments(module.Source)
		i.docs[module.Source] = comments
		if h := module.Module.Header; h != nil {
			i.define("module:"+h.Name, h.NameSpan, "module "+h.Name, nil)
		}
		for _, decl := range module.Module.Decls {
			i.declare(decl)
		}
	}
	for _, module := range result.Graph.Modules {
		if module.Source == nil {
			continue
		}
		i.module(module.Module)
	}
	for _, d := range i.documents {
		sort.Slice(d.uses, func(a, b int) bool {
			if d.uses[a].span.Start != d.uses[b].span.Start {
				return d.uses[a].span.Start < d.uses[b].span.Start
			}
			return d.uses[a].span.End-d.uses[a].span.Start < d.uses[b].span.End-d.uses[b].span.Start
		})
	}
	return i
}

func (i *index) path(f *source.File) string {
	if strings.HasPrefix(f.Name, "<stdlib>/") {
		root, err := libroot.Root()
		if err == nil {
			return filepath.Clean(filepath.Join(root, "stdlib", strings.TrimPrefix(f.Name, "<stdlib>/")))
		}
	}
	return filepath.Clean(filepath.Join(i.root, filepath.FromSlash(f.Name)))
}

func global(kind, name string) string { return kind + ":" + name }
func local(sp source.Span) string     { return "local:" + sp.File.Name + ":" + itoa(sp.Start) }

func (i *index) define(id string, sp source.Span, typeText string, ann *ast.TypeAnn, attributes ...ast.AttributeGroup) {
	if sp.File == nil {
		return
	}
	anchor := sp
	if ann != nil {
		anchor = ann.Sp
	}
	i.symbols[id] = symbol{span: sp, path: i.path(sp.File), typeText: typeText, docs: i.commentBefore(anchor, attributes...)}
	i.use(sp, id)
}

func (i *index) use(sp source.Span, id string) {
	if sp.File == nil || sp.End <= sp.Start {
		return
	}
	if d := i.documents[i.path(sp.File)]; d != nil {
		d.uses = append(d.uses, occurrence{sp, id})
	}
}

func (i *index) declare(decl ast.Decl) {
	ck := i.checker
	switch d := decl.(type) {
	case *ast.ValueDecl:
		text := ""
		if sch, ok := ck.Env.Lookup(d.Name); ok {
			text = ast.Spelling(types.SurfaceName(d.Name)) + " : " + types.ShowScheme(sch)
		}
		i.define(global("value", d.Name), d.NameSpan, text, d.Ann)
	case *ast.TypeDecl:
		i.define(global("type", d.Name), d.NameSpan, typeHead("type", d.Name, d.Params), nil, d.Attributes...)
		for _, c := range d.Ctors {
			text := ""
			if info := ck.Ctors[c.Name]; info != nil {
				text = types.SurfaceName(c.Name) + " : " + types.Show(info.ValueType())
			}
			i.define(global("ctor", c.Name), c.NameSpan, text, nil, c.Attributes...)
		}
		var adt *types.ADTInfo
		if tc, ok := ck.TypeNames[d.Name].(*types.TCon); ok {
			adt = ck.ADTs[tc.Unique]
		}
		for _, f := range d.RecordFields {
			text := ""
			if adt != nil {
				if _, field := adt.RecordField(f.Name); field != nil {
					text = f.Name + " : " + types.Show(field.Type)
				}
			}
			i.define(global("field", d.Name+"."+f.Name), f.NameSpan, text, nil, f.Attributes...)
		}
	case *ast.EffectDecl:
		i.define(global("type", d.Name), d.NameSpan, typeHead("effect", d.Name, d.Params), nil)
		for _, op := range d.Ops {
			text := ""
			if info := ck.Operations[op.Name]; info != nil {
				text = types.SurfaceName(op.Name) + " : " + types.ShowScheme(info.Scheme)
			}
			i.define(global("value", op.Name), op.NameSpan, text, nil)
		}
	case *ast.ClassDecl:
		i.define(global("type", d.Name), d.NameSpan, typeHead("class", d.Name, []ast.Param{d.Param}), nil)
		for _, method := range d.Methods {
			text := ""
			if sch, ok := ck.Env.Lookup(method.Name); ok {
				text = types.SurfaceName(method.Name) + " : " + types.ShowScheme(sch)
			}
			i.define(global("value", method.Name), method.NameSpan, text, nil)
		}
	case *ast.PatternDecl:
		for _, p := range patternBinders(d.Pattern) {
			text := ""
			if sch, ok := ck.Env.Lookup(p.Name); ok {
				text = p.Name + " : " + types.ShowScheme(sch)
			}
			i.define(global("value", p.Name), p.Sp, text, nil)
		}
	}
}

func typeHead(kind, name string, params []ast.Param) string {
	text := kind + " " + types.SurfaceName(name)
	for _, p := range params {
		text += " " + p.Name
	}
	return text
}

func (i *index) module(m *ast.Module) {
	if h := m.Header; h != nil {
		for _, item := range h.Exposing.Items {
			if i.exposed(item, h.Name) {
				continue
			}
			// A re-exported name links to the import that supplied it.
			for _, im := range m.Imports {
				if im.Exposing != nil && i.exposed(item, im.Module) {
					break
				}
			}
		}
	}
	for _, im := range m.Imports {
		i.use(im.ModuleSpan, global("module", im.Module))
		if im.AliasSpan.File != nil {
			i.use(im.AliasSpan, global("module", im.Module))
		}
		if im.Exposing != nil {
			for _, item := range im.Exposing.Items {
				i.exposed(item, im.Module)
			}
		}
	}
	for _, decl := range m.Decls {
		switch d := decl.(type) {
		case *ast.ValueDecl:
			i.value(d, nil)
		case *ast.PatternDecl:
			i.pattern(d.Pattern, nil, false)
			i.expr(d.Body, nil)
		case *ast.TypeDecl:
			d.VisitAttributes(func(group *ast.AttributeGroup) {
				for _, expression := range group.Exprs {
					i.expr(expression, nil)
				}
			})
			for _, c := range d.Ctors {
				for _, arg := range c.Args {
					i.typ(arg)
				}
			}
			for _, f := range d.RecordFields {
				i.typ(f.Type)
			}
		case *ast.EffectDecl:
			for _, op := range d.Ops {
				i.typ(op.Type)
			}
		case *ast.ClassDecl:
			for _, method := range d.Methods {
				i.typ(method.Type)
			}
		case *ast.InstanceDecl:
			i.use(d.Head.Sp, global("type", d.Head.Class))
			i.typ(d.Head.Ty)
			for _, pred := range d.Preds {
				i.use(pred.Sp, global("type", pred.Class))
				i.typ(pred.Ty)
			}
			for _, method := range d.Methods {
				i.value(method, nil)
			}
		case *ast.DeriverDecl:
			i.use(d.ClassSpan, global("type", d.Class))
			for _, method := range d.Methods {
				i.value(method, nil)
			}
		}
	}
}

func (i *index) exposed(item ast.ExposeItem, module string) bool {
	for _, kind := range []string{"value", "type", "ctor"} {
		id := global(kind, module+"."+item.Name)
		if _, ok := i.symbols[id]; ok {
			i.use(item.Sp, id)
			return true
		}
	}
	return false
}

type scope map[string]string

func copyScope(s scope) scope {
	out := scope{}
	for k, v := range s {
		out[k] = v
	}
	return out
}

func (i *index) value(d *ast.ValueDecl, outer scope) {
	if d.Ann != nil {
		i.typ(d.Ann.Type)
		for _, p := range d.Ann.Preds {
			i.use(p.Sp, global("type", p.Class))
			i.typ(p.Ty)
		}
	}
	if len(d.Equations) > 0 {
		for _, eq := range d.Equations {
			s := copyScope(outer)
			for _, p := range eq.Params {
				i.pattern(p, s, true)
			}
			i.expr(eq.Body, s)
		}
		return
	}
	s := copyScope(outer)
	for _, p := range d.Params {
		i.pattern(p, s, true)
	}
	if d.Body != nil {
		i.expr(d.Body, s)
	}
}

func (i *index) typ(t ast.TypeExpr) {
	switch t := t.(type) {
	case *ast.TName:
		i.use(t.Sp, global("type", t.Name))
	case *ast.TApp:
		if !t.Sugared {
			i.use(t.NameSp, global("type", t.Name))
		}
		for _, a := range t.Args {
			i.typ(a)
		}
	case *ast.TFunExpr:
		i.typ(t.Arg)
		i.typ(t.Ret)
		if t.Eff != nil {
			i.row(t.Eff)
		}
	case *ast.TRow:
		i.row(t.Row)
	}
}
func (i *index) row(r *ast.EffRow) {
	for _, l := range r.Labels {
		i.use(l.NameSp, global("type", l.Name))
		for _, a := range l.Args {
			i.typ(a)
		}
	}
}

func patternBinders(p ast.Pattern) []*ast.PVar {
	var out []*ast.PVar
	var visit func(ast.Pattern)
	visit = func(p ast.Pattern) {
		switch p := p.(type) {
		case *ast.PVar:
			out = append(out, p)
		case *ast.PCtor:
			for _, a := range p.Args {
				visit(a)
			}
		case *ast.PRecord:
			for _, f := range p.Fields {
				visit(f.Pattern)
			}
		}
	}
	visit(p)
	return out
}

func (i *index) pattern(p ast.Pattern, s scope, bind bool) {
	switch p := p.(type) {
	case *ast.PVar:
		if bind && s != nil {
			id := local(p.Sp)
			s[p.Name] = id
			text := ""
			if ty := i.checker.PatTypes[p]; ty != nil {
				text = p.Name + " : " + types.Show(i.checker.Sub.Apply(ty))
			}
			i.define(id, p.Sp, text, nil)
		}
	case *ast.PPin:
		i.named(p.NameSpan, p.Name, "value", s)
	case *ast.PCtor:
		if !p.Sugared {
			i.use(p.NameSpan, global("ctor", p.Name))
		}
		for _, a := range p.Args {
			i.pattern(a, s, bind)
		}
	case *ast.PRecord:
		if p.Name != "" {
			i.use(p.NameSpan, global("type", p.Name))
		}
		if adt := i.checker.RecordPatternUses[p]; adt != nil {
			for _, f := range p.Fields {
				i.field(f.NameSpan, adt.Con.Name, f.Name)
			}
		}
		for _, f := range p.Fields {
			i.pattern(f.Pattern, s, bind)
		}
	}
}

func (i *index) named(sp source.Span, name, kind string, s scope) {
	if id := s[name]; id != "" {
		i.use(sp, id)
	} else {
		i.use(sp, global(kind, name))
	}
}

func (i *index) field(sp source.Span, owner, name string) { i.use(sp, global("field", owner+"."+name)) }

func (i *index) expr(e ast.Expr, s scope) {
	switch e := e.(type) {
	case *ast.Var:
		i.named(e.Sp, e.Name, "value", s)
	case *ast.Ctor:
		if !e.Sugared {
			i.use(e.Sp, global("ctor", e.Name))
		}
		if e.Witness != nil {
			i.typ(e.Witness)
		}
	case *ast.RecordLit:
		if e.Name != "" {
			i.use(e.NameSpan, global("type", e.Name))
		}
		if adt := i.checker.RecordUses[e]; adt != nil {
			for _, f := range e.Fields {
				i.field(f.NameSpan, adt.Con.Name, f.Name)
			}
		}
		for _, f := range e.Fields {
			i.expr(f.Value, s)
		}
	case *ast.RecordGet:
		i.expr(e.Record, s)
		if adt := i.checker.RecordUses[e]; adt != nil {
			i.field(e.FieldSpan, adt.Con.Name, e.Field)
		}
	case *ast.RecordUpdate:
		i.expr(e.Record, s)
		if adt := i.checker.RecordUses[e]; adt != nil {
			for _, f := range e.Fields {
				i.field(f.NameSpan, adt.Con.Name, f.Name)
			}
		}
		for _, f := range e.Fields {
			i.expr(f.Value, s)
		}
	case *ast.App:
		i.expr(e.Fn, s)
		i.expr(e.Arg, s)
	case *ast.Neg:
		i.expr(e.Operand, s)
	case *ast.If:
		i.expr(e.Cond, s)
		i.expr(e.Then, s)
		i.expr(e.Else, s)
	case *ast.BinOp:
		if e.Op != "&&" && e.Op != "||" {
			i.named(e.OpSpan, e.Op, "value", s)
		}
		i.expr(e.L, s)
		i.expr(e.R, s)
	case *ast.Lambda:
		inner := copyScope(s)
		for _, p := range e.Params {
			i.pattern(p, inner, true)
		}
		i.expr(e.Body, inner)
	case *ast.Block:
		inner := copyScope(s)
		for n := range e.Binds {
			b := &e.Binds[n]
			if b.Pattern != nil {
				i.expr(b.Body, inner)
				i.pattern(b.Pattern, inner, true)
				continue
			}
			id := local(b.NameSpan)
			text := ""
			if sch, ok := i.checker.BindSchemes[b]; ok {
				text = b.Name + " : " + types.ShowScheme(sch)
			}
			i.define(id, b.NameSpan, text, b.Ann)
			if b.Ann != nil {
				i.typ(b.Ann.Type)
			}
			if len(b.Equations) > 0 {
				for _, eq := range b.Equations {
					row := copyScope(inner)
					row[b.Name] = id
					for _, p := range eq.Params {
						i.pattern(p, row, true)
					}
					i.expr(eq.Body, row)
				}
			} else {
				row := copyScope(inner)
				if len(b.Params) > 0 {
					row[b.Name] = id
				}
				for _, p := range b.Params {
					i.pattern(p, row, true)
				}
				i.expr(b.Body, row)
			}
			inner[b.Name] = id
		}
		for _, item := range e.Items {
			if item.Expr != nil {
				i.expr(item.Expr, inner)
			}
		}
		i.expr(e.Result, inner)
	case *ast.Case:
		i.expr(e.Scrutinee, s)
		for _, b := range e.Branches {
			inner := copyScope(s)
			i.pattern(b.Pattern, inner, true)
			i.expr(b.Body, inner)
		}
	case *ast.Handle:
		i.expr(e.Body, s)
		if e.State != nil {
			i.expr(e.State.Initial, s)
		}
		for _, c := range e.Clauses {
			i.use(c.OpSpan, global("value", c.Op))
			rows := c.Equations
			if len(rows) == 0 {
				rows = []ast.Equation{{Params: c.Params, Body: c.Body}}
			}
			for _, row := range rows {
				inner := copyScope(s)
				if e.State != nil {
					id := local(e.State.NameSpan)
					i.define(id, e.State.NameSpan, "", nil)
					inner[e.State.Name] = id
				}
				for _, p := range row.Params {
					i.pattern(p, inner, true)
				}
				i.expr(row.Body, inner)
			}
		}
		if e.Return != nil {
			inner := copyScope(s)
			i.pattern(e.Return.Param, inner, true)
			i.expr(e.Return.Body, inner)
		}
	case *ast.Quote:
		i.expr(e.Body, s)
	case *ast.Splice:
		i.expr(e.Operand, s)
	case *ast.TypeOf:
		i.typ(e.Ty)
	case *ast.Resume:
		if e.NextState != nil {
			i.expr(e.NextState, s)
		}
	}
}

func (i *index) commentBefore(anchor source.Span, attributes ...ast.AttributeGroup) string {
	if anchor.File == nil {
		return ""
	}
	f := anchor.File
	line := anchor.StartPos().Line - 1
	comments := i.docs[f]
	var chunks []string
	for line > 0 {
		// Skip only this declaration's leading tags, using parsed spans so
		// nested brackets and comments inside multiline payloads stay opaque.
		// Trailing field tags cannot bridge documentation from another field.
		skipped := false
		for _, group := range attributes {
			if group.Sp.Start >= anchor.Start {
				continue
			}
			start, end := group.Sp.StartPos().Line, group.Sp.EndPos().Line
			if start <= line && line <= end {
				line = start - 1
				skipped = true
				break
			}
		}
		if skipped {
			continue
		}
		trim := strings.TrimSpace(f.Line(line))
		if strings.HasPrefix(trim, "{-#") && strings.HasSuffix(trim, "#-}") {
			line--
			continue
		}
		var found *token.Comment
		for n := range comments {
			if comments[n].Span.EndPos().Line == line {
				found = &comments[n]
				break
			}
		}
		if found == nil {
			break
		}
		start := found.Span.StartPos()
		lineStart := found.Span.Start - start.Col + 1
		if strings.TrimSpace(string(f.Content[lineStart:found.Span.Start])) != "" {
			break
		}
		chunks = append(chunks, cleanComment(found.Text, found.Block))
		line = found.Span.StartPos().Line - 1
	}
	for a, b := 0, len(chunks)-1; a < b; a, b = a+1, b-1 {
		chunks[a], chunks[b] = chunks[b], chunks[a]
	}
	return strings.TrimSpace(strings.Join(chunks, "\n"))
}

func cleanComment(s string, block bool) string {
	if block {
		return strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(s, "{-"), "-}"))
	}
	return strings.TrimSpace(strings.TrimPrefix(s, "--"))
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	at := len(buf)
	for n > 0 {
		at--
		buf[at] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[at:])
}
