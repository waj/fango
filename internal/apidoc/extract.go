package apidoc

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/check"
	"github.com/waj/fango/internal/infer"
	"github.com/waj/fango/internal/lexer"
	"github.com/waj/fango/internal/modules"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/token"
	"github.com/waj/fango/internal/types"
)

// SchemaVersion identifies the JSON contract in doc/reference/commands.md.
const SchemaVersion = 1

// Declaration kinds.
const (
	KindValue       = "value"
	KindType        = "type"
	KindConstructor = "constructor"
	KindField       = "field"
	KindClass       = "class"
	KindMethod      = "method"
	KindEffect      = "effect"
	KindOperation   = "operation"
)

type Document struct {
	SchemaVersion int      `json:"schemaVersion"`
	Modules       []Module `json:"modules"`
}

type Module struct {
	Name          string        `json:"name"`
	Documentation string        `json:"documentation"`
	Source        Location      `json:"source"`
	Declarations  []Declaration `json:"declarations"`
}

type Location struct {
	Path string `json:"path"`
	Line int    `json:"line"`
}

type Declaration struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Kind          string   `json:"kind"`
	Signature     string   `json:"signature"`
	Documentation string   `json:"documentation"`
	Source        Location `json:"source"`
	ParentID      string   `json:"parentId,omitempty"`
	TargetID      string   `json:"targetId,omitempty"`
	Fixity        string   `json:"fixity,omitempty"`
	Instances     []string `json:"instances,omitempty"`
}

// Missing is one public declaration, or a module, without documentation.
type Missing struct {
	Location Location
	ID       string
}

func (m Missing) String() string {
	return fmt.Sprintf("%s:%d: %s has no documentation", m.Location.Path, m.Location.Line, m.ID)
}

// Options control extraction. Path maps a source file to the path reported
// for it; it must not depend on the machine.
type Options struct {
	Modules []string
	Path    func(*source.File) string
}

// owned is one declaration as its defining module documents it, before the
// exporting module decides whether, and under which ID, it is public.
type owned struct {
	Declaration
	// id is the declaration's ID in its defining module.
	id string
	// parent is the canonical name of the type, class, or effect a member
	// belongs to.
	parent string
	// params are a type declaration's source parameters.
	params []ast.Param
}

type extractor struct {
	ck       *infer.Checker
	path     func(*source.File) string
	comments map[*source.File][]token.Comment
	// values, types, and ctors are keyed by canonical name. Fields are keyed
	// by the canonical type name and the field.
	values, types, ctors map[string]*owned
	fields               map[string]*owned
	qualify              func(string) string
}

// Extract documents the selected modules of a checked graph. Every module
// named must be in it. Declarations come from each module's resolved export
// interface; types come from the checker.
func Extract(result *check.Result, options Options) (*Document, []Missing) {
	x := &extractor{ck: result.Checker, path: options.Path, comments: map[*source.File][]token.Comment{},
		values: map[string]*owned{}, types: map[string]*owned{}, ctors: map[string]*owned{}, fields: map[string]*owned{}}
	x.qualify = qualifier(x.ck)
	byName := map[string]modules.ResolvedModule{}
	for _, m := range result.Graph.Modules {
		if m.Source == nil || m.Module.Header == nil {
			continue
		}
		byName[m.Name] = m
		x.collect(m)
	}
	doc := &Document{SchemaVersion: SchemaVersion, Modules: []Module{}}
	var missing []Missing
	names := append([]string(nil), options.Modules...)
	sort.Strings(names)
	names = slices.Compact(names)
	for _, name := range names {
		m, ok := byName[name]
		if !ok {
			continue
		}
		module, gaps := x.module(m)
		doc.Modules = append(doc.Modules, module)
		missing = append(missing, gaps...)
	}
	return doc, missing
}

func (x *extractor) location(sp source.Span) Location {
	return Location{Path: x.path(sp.File), Line: sp.StartPos().Line}
}

func (x *extractor) leading(anchor source.Span, attributes ...ast.AttributeGroup) string {
	return Leading(x.comments[anchor.File], anchor, attributes...)
}

// member documents a constructor or field only when it begins its own line.
func (x *extractor) member(anchor source.Span, attributes ...ast.AttributeGroup) string {
	if !StartsLine(anchor, attributes...) {
		return ""
	}
	return x.leading(anchor, attributes...)
}

func (x *extractor) fixity(name string) string {
	surface := types.SurfaceName(name)
	if ast.Spelling(surface) == surface {
		return ""
	}
	f, ok := x.ck.Fixity[surface]
	if !ok {
		return ""
	}
	return fmt.Sprintf("%s %d", f.Assoc, f.Prec)
}

// collect records every declaration a module owns, public or not, with its
// documentation and checked signature.
func (x *extractor) collect(m modules.ResolvedModule) {
	_, comments, _ := lexer.LexWithComments(m.Source)
	x.comments[m.Source] = comments
	ck := x.ck
	for _, decl := range m.Module.Decls {
		switch d := decl.(type) {
		case *ast.ValueDecl:
			anchor := d.NameSpan
			if d.Ann != nil {
				anchor = d.Ann.Sp
			}
			sig := ""
			if sch, ok := ck.Env.Lookup(d.Name); ok {
				sig = ScopedSignature(d.Name, sch, types.NewPrinter())
			}
			x.values[d.Name] = &owned{id: KindValue + ":" + d.Name, Declaration: Declaration{Kind: KindValue, Name: ast.Spelling(types.SurfaceName(d.Name)),
				Signature: sig, Documentation: x.leading(anchor), Source: x.location(anchor), Fixity: x.fixity(d.Name)}}
		case *ast.PatternDecl:
			for _, b := range patternBinders(d.Pattern) {
				sig := ""
				if sch, ok := ck.Env.Lookup(b.Name); ok {
					sig = Signature(b.Name, sch, types.NewPrinter())
				}
				x.values[b.Name] = &owned{id: KindValue + ":" + b.Name, Declaration: Declaration{Kind: KindValue, Name: types.SurfaceName(b.Name),
					Signature: sig, Documentation: x.leading(b.Sp), Source: x.location(b.Sp)}}
			}
		case *ast.TypeDecl:
			x.typeDecl(d)
		case *ast.ClassDecl:
			x.types[d.Name] = &owned{id: KindClass + ":" + d.Name, Declaration: Declaration{Kind: KindClass, Name: types.SurfaceName(d.Name),
				Signature: Head("class", d.Name, []ast.Param{d.Param}), Documentation: x.leading(d.NameSpan), Source: x.location(d.NameSpan),
				Instances: instancesOf(ck, func(i *infer.InstanceInfo) bool { return i.Class.Name == d.Name }, nil, x.qualify)}}
			var param *types.TVar
			if class := ck.Classes[d.Name]; class != nil {
				param = class.Param
			}
			for _, method := range d.Methods {
				sig := ""
				if sch, ok := ck.Env.Lookup(method.Name); ok {
					p := types.NewPrinter()
					if param != nil {
						p.Bind(param, d.Param.Name)
					}
					sig = Signature(method.Name, sch, p)
				}
				x.values[method.Name] = &owned{id: KindMethod + ":" + d.Name + "." + types.SurfaceName(method.Name), parent: d.Name, Declaration: Declaration{Kind: KindMethod, Name: ast.Spelling(types.SurfaceName(method.Name)),
					Signature: sig, Documentation: x.leading(method.NameSpan), Source: x.location(method.NameSpan), Fixity: x.fixity(method.Name)}}
			}
		case *ast.EffectDecl:
			x.types[d.Name] = &owned{id: KindEffect + ":" + d.Name, Declaration: Declaration{Kind: KindEffect, Name: types.SurfaceName(d.Name),
				Signature: Head("effect", d.Name, d.Params), Documentation: x.leading(d.NameSpan), Source: x.location(d.NameSpan)}}
			effect := ck.Effects[d.Name]
			for _, op := range d.Ops {
				sig := ""
				if info := ck.Operations[op.Name]; info != nil {
					p := types.NewPrinter()
					if effect != nil {
						bindParams(p, effect.Params, d.Params)
					}
					sig = Signature(op.Name, info.Scheme, p)
					if info.Abort {
						sig = "abort " + sig
					}
				}
				x.values[op.Name] = &owned{id: KindOperation + ":" + d.Name + "." + types.SurfaceName(op.Name), parent: d.Name, Declaration: Declaration{Kind: KindOperation, Name: ast.Spelling(types.SurfaceName(op.Name)),
					Signature: sig, Documentation: x.leading(op.NameSpan), Source: x.location(op.NameSpan)}}
			}
		}
	}
}

func (x *extractor) adt(name string) *types.ADTInfo {
	if tc, ok := x.ck.TypeNames[name].(*types.TCon); ok {
		return x.ck.ADTs[tc.Unique]
	}
	return nil
}

func (x *extractor) typeDecl(d *ast.TypeDecl) {
	ck := x.ck
	adt := x.adt(d.Name)
	printer := func() *types.Printer {
		p := types.NewPrinter()
		if adt != nil {
			bindParams(p, adt.Params, d.Params)
		}
		return p
	}
	var instances []string
	if adt != nil {
		instances = instancesOf(ck, func(i *infer.InstanceInfo) bool {
			tc, ok := i.Head.(*types.TCon)
			return ok && tc.Unique == adt.Con.Unique
		}, d.Params, nil)
	}
	x.types[d.Name] = &owned{id: KindType + ":" + d.Name, params: d.Params, Declaration: Declaration{Kind: KindType, Name: types.SurfaceName(d.Name),
		Signature: Head("type", d.Name, d.Params), Documentation: x.leading(d.NameSpan, d.Attributes...), Source: x.location(d.NameSpan),
		Instances: instances}}
	for _, c := range d.Ctors {
		sig := ""
		if info := ck.Ctors[c.Name]; info != nil {
			sig = Typed(c.Name, info.ValueType(), printer())
		}
		x.ctors[c.Name] = &owned{id: KindConstructor + ":" + c.Name, parent: d.Name, Declaration: Declaration{Kind: KindConstructor, Name: types.SurfaceName(c.Name),
			Signature: sig, Documentation: x.member(c.NameSpan, c.Attributes...), Source: x.location(c.NameSpan)}}
	}
	for _, f := range d.RecordFields {
		sig := ""
		if adt != nil {
			if _, field := adt.RecordField(f.Name); field != nil {
				sig = Typed(f.Name, field.Type, printer())
			}
		}
		x.fields[d.Name+"."+f.Name] = &owned{id: KindField + ":" + d.Name + "." + f.Name, parent: d.Name, Declaration: Declaration{Kind: KindField, Name: f.Name,
			Signature: sig, Documentation: x.member(f.NameSpan, f.Attributes...), Source: x.location(f.NameSpan)}}
	}
}

// typeSignature renders a type declaration with exactly the representation
// its exporter publishes: constructors or fields only when they are exported.
func (x *extractor) typeSignature(canonical string, member *owned, ctors, fields []string) string {
	adt := x.adt(canonical)
	if adt == nil {
		return member.Signature
	}
	if len(fields) > 0 {
		parts := make([]string, 0, len(fields))
		for _, f := range fields {
			if field := x.fields[canonical+"."+f]; field != nil {
				parts = append(parts, field.Signature)
			}
		}
		return member.Signature + " = { " + strings.Join(parts, ", ") + " }"
	}
	if len(ctors) == 0 {
		return member.Signature
	}
	p := types.NewPrinter()
	bindParams(p, adt.Params, member.params)
	parts := make([]string, 0, len(ctors))
	for _, info := range adt.Ctors {
		if !slices.Contains(ctors, info.Name) && !slices.Contains(ctors, types.SurfaceName(info.Name)) {
			continue
		}
		text := types.SurfaceName(info.Name)
		for _, field := range info.Fields {
			text += " " + p.Atom(field)
		}
		parts = append(parts, text)
	}
	return member.Signature + " = " + strings.Join(parts, " | ")
}

// module documents one module's public interface: owned declarations under
// their own IDs, and re-exports pointing at their owners.
func (x *extractor) module(m modules.ResolvedModule) (Module, []Missing) {
	h := m.Module.Header
	out := Module{Name: m.Name, Documentation: x.leading(h.NameSpan), Source: x.location(h.NameSpan), Declarations: []Declaration{}}
	var missing []Missing
	if out.Documentation == "" {
		missing = append(missing, Missing{Location: out.Source, ID: "module:" + m.Name})
	}
	iface := m.Interface
	// add publishes member under this module's name for it. A member links
	// its parent when this module publishes the parent too. Fields, methods,
	// and operations also name the parent in their IDs; constructors share a
	// module-wide namespace and need not.
	add := func(local string, member *owned) {
		if member == nil {
			return
		}
		d := member.Declaration
		d.ID = d.Kind + ":" + m.Name + "." + local
		if member.parent != "" {
			parent := types.SurfaceName(member.parent)
			if member.Kind != KindConstructor {
				d.ID = d.Kind + ":" + m.Name + "." + parent + "." + local
			}
			if owner := x.types[member.parent]; owner != nil && iface.Types[parent] == member.parent {
				d.ParentID = owner.Kind + ":" + m.Name + "." + parent
			}
		}
		if member.id != d.ID {
			d.TargetID = member.id
		} else if d.Documentation == "" {
			missing = append(missing, Missing{Location: d.Source, ID: d.ID})
		}
		out.Declarations = append(out.Declarations, d)
	}
	for local, canonical := range iface.Values {
		add(local, x.values[canonical])
	}
	for local, canonical := range iface.Types {
		member := x.types[canonical]
		if member == nil {
			continue
		}
		copied := *member
		if member.Kind == KindType {
			copied.Signature = x.typeSignature(canonical, member, iface.TypeMembers[local], iface.RecordFields[local])
		}
		add(local, &copied)
	}
	for local, canonical := range iface.Ctors {
		add(local, x.ctors[canonical])
	}
	for typ, fields := range iface.RecordFields {
		for _, f := range fields {
			add(f, x.fields[iface.Types[typ]+"."+f])
		}
	}
	sort.Slice(out.Declarations, func(a, b int) bool { return out.Declarations[a].ID < out.Declarations[b].ID })
	sort.Slice(missing, func(a, b int) bool {
		if missing[a].Location.Line != missing[b].Location.Line {
			return missing[a].Location.Line < missing[b].Location.Line
		}
		return missing[a].ID < missing[b].ID
	})
	return out, missing
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
