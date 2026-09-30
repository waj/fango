package codegen

import (
	"fmt"
	goast "go/ast"
	"strings"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

type rowPreparation struct {
	inputs     map[goast.Expr]bool
	values     map[string]goast.Expr
	statements []goast.Stmt
}

// Only immutable row/evidence binders available at function entry may be
// commoned there. A nested handler or invocation binder is a different input.
func (g *gen) prepareRows() func([]goast.Stmt) []goast.Stmt {
	old := g.rowPreparation
	p := &rowPreparation{inputs: map[goast.Expr]bool{}, values: map[string]goast.Expr{}}
	for _, stack := range g.rows {
		if len(stack) != 0 {
			p.inputs[stack[len(stack)-1]] = true
		}
	}
	for _, stack := range g.evidence {
		if len(stack) != 0 {
			if _, ok := stack[len(stack)-1].(*goast.Ident); ok {
				p.inputs[stack[len(stack)-1]] = true
			}
		}
	}
	g.rowPreparation = p
	return func(body []goast.Stmt) []goast.Stmt {
		g.rowPreparation = old
		return append(p.statements, body...)
	}
}

func (g *gen) rowType() goast.Expr {
	g.usesFangort = true
	return selector("fangort", "EvidenceValue")
}

func (g *gen) rowValue(id types.CaptureVar) goast.Expr {
	stack := g.rows[id]
	if len(stack) == 0 {
		panic("codegen: unavailable residual row")
	}
	return stack[len(stack)-1]
}

func (g *gen) pushRow(id types.CaptureVar, value goast.Expr) func() {
	if g.rows == nil {
		g.rows = map[types.CaptureVar][]goast.Expr{}
	}
	g.rows[id] = append(g.rows[id], value)
	return func() { g.rows[id] = g.rows[id][:len(g.rows[id])-1] }
}

func (g *gen) rowArgument(row *core.RowArgument) goast.Expr {
	var tail goast.Expr = &goast.CompositeLit{Type: g.rowType()}
	if row == nil {
		return tail
	}
	if row.From != 0 {
		tail = g.rowValue(row.From)
	}
	if len(row.Effects) == 0 {
		return tail
	}
	args := []goast.Expr{tail}
	p := g.rowPreparation
	eligible := p != nil && (row.From == 0 || p.inputs[tail])
	var key strings.Builder
	fmt.Fprintf(&key, "%d", row.From)
	if row.From != 0 {
		fmt.Fprintf(&key, ":%p", tail)
	}
	for _, ev := range row.Effects {
		stack := g.evidence[ev.Key()]
		if len(stack) == 0 {
			panic("codegen: missing residual evidence")
		}
		args = append(args, &goast.SelectorExpr{X: stack[len(stack)-1], Sel: ident("Binding")})
		eligible = eligible && p.inputs[stack[len(stack)-1]]
		fmt.Fprintf(&key, ":%s:%p", ev.Key(), stack[len(stack)-1])
	}
	g.usesFangort = true
	value := callExpr(selector("fangort", "ExtendEvidenceValue"), args...)
	if !eligible {
		return value
	}
	if prior := p.values[key.String()]; prior != nil {
		return prior
	}
	name := fmt.Sprintf("t_row%d", g.tmp)
	g.tmp++
	result := ident(name)
	p.values[key.String()] = result
	p.statements = append(p.statements, varDeclStmt(name, g.rowType(), value))
	return result
}

// Projection remains an expression: a callback's invocation row is not read
// when the callback is constructed. Its operations need no wrapper closures.
func (g *gen) deferredEvidence(ev core.EffectInstance, row goast.Expr, mode types.Transport) goast.Expr {
	desired := ev
	desired.Control = types.Control{Transport: mode}
	args := []goast.Expr{row, stringLit(ev.Name), selector("fangort", memberName(mode)+"Evidence")}
	args = append(args, g.typeDescriptorArgs(ev.Args)...)
	return callExpr(indexExpr(selector("fangort", "ValueEvidence"), []goast.Expr{g.effectTypeMode(desired, mode)}), args...)
}

func (g *gen) bindDeferredEffects(effects []core.EffectInstance, row goast.Expr, mode types.Transport) func() {
	for _, ev := range effects {
		g.evidence[ev.Key()] = append(g.evidence[ev.Key()], g.deferredEvidence(ev, row, mode))
		g.evidenceModes[ev.Key()] = append(g.evidenceModes[ev.Key()], mode)
	}
	return func() {
		for _, ev := range effects {
			g.evidence[ev.Key()] = g.evidence[ev.Key()][:len(g.evidence[ev.Key()])-1]
			g.evidenceModes[ev.Key()] = g.evidenceModes[ev.Key()][:len(g.evidenceModes[ev.Key()])-1]
		}
	}
}
