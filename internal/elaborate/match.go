package elaborate

import (
	"fmt"
	"strings"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/types"
)

// Decision-tree compilation (DESIGN.md §8.5): Maranget-style — each
// scrutinee position is examined at most once per path. Exhaustiveness is
// checked FIRST via the usefulness algorithm (which reconstructs a witness
// like `Just Nothing`, not just the leaf constructor); tree compilation then
// runs on a known-exhaustive matrix, so an empty default matrix is an
// internal error, and a branch that never reaches a leaf is redundant.

// caseExpr elaborates a surface case into a Core Case. The scrutinee is
// bound once; branch bodies are elaborated once each and may be shared by
// several leaves (a body reached via the default path and via a
// specialization is the same immutable Core subtree).
func (el *elab) caseExpr(e *ast.Case, ty types.Type) core.Expr {
	scrut := el.expr(e.Scrutinee)
	bind := fmt.Sprintf("_s%d", el.tmp)
	el.tmp++

	m := &matcher{
		el:     el,
		bodies: make([]core.Expr, len(e.Branches)),
		spans:  make([]source.Span, len(e.Branches)),
		used:   make([]bool, len(e.Branches)),
	}
	rows := make([]row, len(e.Branches))
	witnessMatrix := make([][]ast.Pattern, len(e.Branches))
	for i := range e.Branches {
		br := &e.Branches[i]
		m.bodies[i] = el.expr(br.Body)
		m.spans[i] = br.Pattern.Span()
		rows[i] = row{pats: []ast.Pattern{br.Pattern}, idx: i}
		witnessMatrix[i] = []ast.Pattern{br.Pattern}
	}

	if w := m.witness([]types.Type{scrut.Type()}, witnessMatrix); w != nil {
		el.errs = append(el.errs, diag.Errorf(e.Sp, "MISSING PATTERNS",
			"This `case` does not cover every possible value. For example, it\ndoes not handle:\n\n    %s\n\nAdd a branch for it (or a catch-all `_` branch).", w[0]))
		return scrut // errors abort before lint/codegen; keep the shape sane
	}

	tree := m.compile([]occurrence{{name: bind, ty: scrut.Type()}}, rows)
	for i, u := range m.used {
		if !u {
			el.errs = append(el.errs, diag.Errorf(m.spans[i], "REDUNDANT PATTERN",
				"This pattern can never match — the branches above it already\ncover every value it would catch."))
		}
	}
	if leaf, ok := tree.(*core.Leaf); ok {
		// Irrefutable first branch: no discrimination needed, but the
		// scrutinee still evaluates (strictness).
		return &core.Let{Name: bind, Rhs: scrut, Body: leaf.Body, Ty: leaf.Body.Type()}
	}
	return &core.Case{Scrut: scrut, Bind: bind, Tree: tree, Ty: ty}
}

// occurrence is one already-bound scrutinee position: a variable name the
// tree may test, with its (ground) type.
type occurrence struct {
	name string
	ty   types.Type
}

// row is one matrix row: a pattern per occurrence, the variable bindings
// accumulated while specializing, and the branch it belongs to.
type row struct {
	pats  []ast.Pattern
	binds []patBind
	idx   int
}

type patBind struct {
	name string
	occ  string
	ty   types.Type
}

type matcher struct {
	el     *elab
	bodies []core.Expr
	spans  []source.Span
	used   []bool
}

func irrefutable(p ast.Pattern) bool {
	switch p.(type) {
	case *ast.PVar, *ast.PWildcard:
		return true
	}
	return false
}

func (m *matcher) adtOf(t types.Type) *types.ADTInfo {
	if con, ok := t.(*types.TCon); ok {
		return m.el.ck.ADTs[con.Unique]
	}
	return nil
}

// ---------------------------------------------------------------------------
// Exhaustiveness: usefulness of the all-wildcard vector, with witness
// reconstruction (Maranget's U/I algorithm specialized to q = _…_).

// witness returns a rendered example value vector matched by NO row, or nil
// if the matrix is exhaustive.
func (m *matcher) witness(tys []types.Type, matrix [][]ast.Pattern) []string {
	if len(tys) == 0 {
		if len(matrix) == 0 {
			return []string{}
		}
		return nil
	}

	heads := map[string]bool{}
	for _, r := range matrix {
		if pc, ok := r[0].(*ast.PCtor); ok {
			heads[pc.Name] = true
		}
	}
	adt := m.adtOf(tys[0])

	if adt != nil && len(heads) == len(adt.Ctors) && len(heads) > 0 {
		// Complete signature: a witness must hide under some constructor.
		for _, c := range adt.Ctors {
			spec := specializeWitness(c, matrix)
			if w := m.witness(append(append([]types.Type{}, c.Fields...), tys[1:]...), spec); w != nil {
				head := renderCtor(c.Name, w[:len(c.Fields)])
				return append([]string{head}, w[len(c.Fields):]...)
			}
		}
		return nil
	}

	// Incomplete signature (or a literal / function / never-tested column):
	// a witness exists iff the default matrix has one.
	var def [][]ast.Pattern
	for _, r := range matrix {
		if irrefutable(r[0]) {
			def = append(def, r[1:])
		}
	}
	w := m.witness(tys[1:], def)
	if w == nil {
		return nil
	}
	head := "_"
	if adt != nil {
		for _, c := range adt.Ctors {
			if !heads[c.Name] {
				head = renderCtor(c.Name, underscores(len(c.Fields)))
				break
			}
		}
	}
	return append([]string{head}, w...)
}

// specializeWitness is row specialization without bindings (witnesses don't
// bind): keep rows matching constructor c, expanding sub-patterns.
func specializeWitness(c *types.CtorInfo, matrix [][]ast.Pattern) [][]ast.Pattern {
	var out [][]ast.Pattern
	for _, r := range matrix {
		switch p := r[0].(type) {
		case *ast.PCtor:
			if p.Name == c.Name {
				out = append(out, splicePats(r, 0, p.Args))
			}
		case *ast.PVar, *ast.PWildcard:
			out = append(out, splicePats(r, 0, wildcards(len(c.Fields))))
		}
	}
	return out
}

func renderCtor(name string, fields []string) string {
	if len(fields) == 0 {
		return name
	}
	parts := []string{name}
	for _, f := range fields {
		if strings.Contains(f, " ") {
			f = "(" + f + ")"
		}
		parts = append(parts, f)
	}
	return strings.Join(parts, " ")
}

func underscores(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "_"
	}
	return out
}

// ---------------------------------------------------------------------------
// Tree compilation. Runs only on exhaustive matrices.

func (m *matcher) compile(occs []occurrence, rows []row) core.Tree {
	if len(rows) == 0 {
		panic("elaborate: empty pattern matrix — exhaustiveness was checked")
	}

	// First row all irrefutable: it wins. Bind its variables and leaf out.
	first := rows[0]
	col := -1
	for j, p := range first.pats {
		if !irrefutable(p) {
			col = j
			break
		}
	}
	if col == -1 {
		m.used[first.idx] = true
		binds := append([]patBind{}, first.binds...)
		for j, p := range first.pats {
			if pv, ok := p.(*ast.PVar); ok {
				binds = append(binds, patBind{name: pv.Name, occ: occs[j].name, ty: occs[j].ty})
			}
		}
		body := m.bodies[first.idx]
		for i := len(binds) - 1; i >= 0; i-- {
			b := binds[i]
			body = &core.Let{
				Name: b.name,
				Rhs:  &core.VarRef{Name: b.occ, Ty: b.ty},
				Body: body,
				Ty:   body.Type(),
			}
		}
		return &core.Leaf{Body: body}
	}

	// Classify the column by its first refutable pattern.
	for _, r := range rows {
		if pc, ok := r.pats[col].(*ast.PCtor); ok {
			info := m.el.ck.Ctors[pc.Name]
			if info == nil {
				panic("elaborate: unknown constructor in pattern — the checker should have rejected this")
			}
			return m.switchCtor(occs, rows, col, m.el.ck.ADTs[info.Result.Unique])
		}
		if !irrefutable(r.pats[col]) {
			return m.switchLit(occs, rows, col)
		}
	}
	panic("elaborate: refutable column vanished")
}

// switchCtor specializes the matrix per constructor of the column's ADT.
func (m *matcher) switchCtor(occs []occurrence, rows []row, col int, adt *types.ADTInfo) core.Tree {
	present := map[string]bool{}
	for _, r := range rows {
		if pc, ok := r.pats[col].(*ast.PCtor); ok {
			present[pc.Name] = true
		}
	}

	var cases []core.CtorCase
	for _, ctor := range adt.Ctors {
		if !present[ctor.Name] {
			continue
		}
		// Field occurrences: fresh temps spliced in place of the column.
		fieldOccs := make([]occurrence, len(ctor.Fields))
		for j, ft := range ctor.Fields {
			fieldOccs[j] = occurrence{name: fmt.Sprintf("_c%d", m.el.tmp), ty: ft}
			m.el.tmp++
		}
		newOccs := spliceOccs(occs, col, fieldOccs)

		var spec []row
		for _, r := range rows {
			switch p := r.pats[col].(type) {
			case *ast.PCtor:
				if p.Name != ctor.Name {
					continue
				}
				spec = append(spec, row{
					pats:  splicePats(r.pats, col, p.Args),
					binds: r.binds,
					idx:   r.idx,
				})
			case *ast.PVar:
				spec = append(spec, row{
					pats:  splicePats(r.pats, col, wildcards(len(ctor.Fields))),
					binds: append(append([]patBind{}, r.binds...), patBind{name: p.Name, occ: occs[col].name, ty: occs[col].ty}),
					idx:   r.idx,
				})
			case *ast.PWildcard:
				spec = append(spec, row{
					pats:  splicePats(r.pats, col, wildcards(len(ctor.Fields))),
					binds: r.binds,
					idx:   r.idx,
				})
			}
		}

		sub := m.compile(newOccs, spec)
		binds := make([]string, len(fieldOccs))
		for j, fo := range fieldOccs {
			if core.TreeMentions(sub, fo.name) {
				binds[j] = fo.name
			}
		}
		cases = append(cases, core.CtorCase{Ctor: ctor, Binds: binds, Tree: sub})
	}

	var def core.Tree
	if len(cases) < len(adt.Ctors) {
		var defRows []row
		for _, r := range rows {
			switch p := r.pats[col].(type) {
			case *ast.PVar:
				defRows = append(defRows, row{
					pats:  removePat(r.pats, col),
					binds: append(append([]patBind{}, r.binds...), patBind{name: p.Name, occ: occs[col].name, ty: occs[col].ty}),
					idx:   r.idx,
				})
			case *ast.PWildcard:
				defRows = append(defRows, row{pats: removePat(r.pats, col), binds: r.binds, idx: r.idx})
			}
		}
		def = m.compile(removeOcc(occs, col), defRows)
	}
	return &core.SwitchCtor{Scrut: occs[col].name, ADT: adt, Cases: cases, Default: def}
}

// switchLit specializes the matrix per distinct literal in the column.
func (m *matcher) switchLit(occs []occurrence, rows []row, col int) core.Tree {
	occ := occs[col]
	var cases []core.LitCase
	var caseKeys []string
	specs := map[string][]row{}

	litKey := func(lit core.Expr) string { return core.DumpExpr(lit) }

	for _, r := range rows {
		if irrefutable(r.pats[col]) {
			continue
		}
		lit := m.litExpr(r.pats[col], occ.ty)
		k := litKey(lit)
		if _, seen := specs[k]; !seen {
			caseKeys = append(caseKeys, k)
			cases = append(cases, core.LitCase{Lit: lit})
			specs[k] = nil
		}
	}
	for _, r := range rows {
		switch p := r.pats[col].(type) {
		case *ast.PVar:
			// Var rows match every literal and the default.
			nr := row{
				pats:  removePat(r.pats, col),
				binds: append(append([]patBind{}, r.binds...), patBind{name: p.Name, occ: occ.name, ty: occ.ty}),
				idx:   r.idx,
			}
			for _, k := range caseKeys {
				specs[k] = append(specs[k], nr)
			}
			specs["_default"] = append(specs["_default"], nr)
		case *ast.PWildcard:
			nr := row{pats: removePat(r.pats, col), binds: r.binds, idx: r.idx}
			for _, k := range caseKeys {
				specs[k] = append(specs[k], nr)
			}
			specs["_default"] = append(specs["_default"], nr)
		default:
			k := litKey(m.litExpr(p, occ.ty))
			specs[k] = append(specs[k], row{pats: removePat(r.pats, col), binds: r.binds, idx: r.idx})
		}
	}

	newOccs := removeOcc(occs, col)
	for i, k := range caseKeys {
		cases[i].Tree = m.compile(newOccs, specs[k])
	}
	def := m.compile(newOccs, specs["_default"])
	return &core.SwitchLit{Scrut: occ.name, Cases: cases, Default: def}
}

// litExpr builds the Core literal a literal pattern tests for. An integer
// pattern on a Float scrutinee is a Float test — Elm's number rule, exactly
// as literals elaborate.
func (m *matcher) litExpr(p ast.Pattern, ty types.Type) core.Expr {
	switch p := p.(type) {
	case *ast.PInt:
		if con, ok := ty.(*types.TCon); ok && con.Unique == m.el.ck.B.Float.Unique {
			return &core.FloatLit{Val: float64(p.Value), Ty: ty}
		}
		return &core.IntLit{Val: p.Value, Ty: ty}
	case *ast.PFloat:
		return &core.FloatLit{Val: p.Value, Ty: ty}
	case *ast.PString:
		return &core.StringLit{Val: p.Value, Ty: ty}
	default:
		panic(fmt.Sprintf("elaborate: pattern %T is not a literal", p))
	}
}

var sharedWildcard = &ast.PWildcard{}

func wildcards(n int) []ast.Pattern {
	out := make([]ast.Pattern, n)
	for i := range out {
		out[i] = sharedWildcard
	}
	return out
}

func splicePats(pats []ast.Pattern, col int, sub []ast.Pattern) []ast.Pattern {
	out := make([]ast.Pattern, 0, len(pats)-1+len(sub))
	out = append(out, pats[:col]...)
	out = append(out, sub...)
	return append(out, pats[col+1:]...)
}

func removePat(pats []ast.Pattern, col int) []ast.Pattern {
	return splicePats(pats, col, nil)
}

func spliceOccs(occs []occurrence, col int, sub []occurrence) []occurrence {
	out := make([]occurrence, 0, len(occs)-1+len(sub))
	out = append(out, occs[:col]...)
	out = append(out, sub...)
	return append(out, occs[col+1:]...)
}

func removeOcc(occs []occurrence, col int) []occurrence {
	return spliceOccs(occs, col, nil)
}
