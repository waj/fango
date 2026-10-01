// Package lower makes the Go backend's statement and invocation boundaries
// explicit without changing the Core interpreted by the evaluator.
package lower

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

type Block []Statement

type Statement interface{ statement() }

type Bind struct{ Let *core.Let }
type Eval struct{ Value core.Expr }
type Branch struct {
	Condition  core.Expr
	Then, Else Block
}
type Match struct{ Case *core.Case }
type Return struct{ Value core.Expr }
type Continue struct{ Call *core.App }
type Loop struct{ Body Block }

func (Bind) statement()     {}
func (Eval) statement()     {}
func (Branch) statement()   {}
func (Match) statement()    {}
func (Return) statement()   {}
func (Continue) statement() {}
func (Loop) statement()     {}

type Function struct {
	Definition *core.Def
	Body       Block
	Loop       bool
}

type Program struct {
	Functions map[string]*Function
}

// Module lowers only owned definitions. Dependency headers are sufficient;
// their bodies are deliberately ignored even in whole-program emission.
func Module(p *core.Prog, owner string) (*Program, error) {
	owned := *p
	owned.Defs = nil
	var context []core.Def
	for _, d := range p.Defs {
		if d.Owner == owner {
			owned.Defs = append(owned.Defs, d)
		} else {
			d.Body = nil
			context = append(context, d)
		}
	}
	// Reconstruct contracts rather than trusting potentially stale serialized
	// decisions. This also catches uses accidentally added by later transforms.
	core.SummarizeABI(&owned, context)
	checkedABI := make(map[string]core.ABISummary, len(owned.Defs))
	for _, d := range owned.Defs {
		checkedABI[d.Name] = d.ABI
	}
	out := &Program{Functions: map[string]*Function{}}
	for i := range p.Defs {
		d := &p.Defs[i]
		if d.Owner != owner {
			continue
		}
		if d.ABI.Valid && !reflect.DeepEqual(d.ABI, checkedABI[d.Name]) {
			return nil, fmt.Errorf("lower: %s: stale callback/representation ABI", d.Name)
		}
		_, loop := core.DetectTailLoop(d)
		body := Tail(d, d.Body, loop)
		if loop {
			body = Block{Loop{body}}
		}
		f := &Function{Definition: d, Body: body, Loop: loop}
		if err := Verify(f); err != nil {
			return nil, err
		}
		out.Functions[d.Name] = f
	}
	return out, nil
}

func Tail(d *core.Def, e core.Expr, loop bool) Block {
	switch e := e.(type) {
	case *core.Let:
		return append(Block{Bind{e}}, Tail(d, e.Body, loop)...)
	case *core.Seq:
		return append(Block{Eval{e.First}}, Tail(d, e.Then, loop)...)
	case *core.If:
		return Block{Branch{e.Cond, Tail(d, e.Then, loop), Tail(d, e.Else, loop)}}
	case *core.Case:
		return Block{Match{e}}
	case *core.App:
		if loop && d != nil && core.IsTailLoopCall(d, e) {
			return Block{Continue{e}}
		}
	}
	return Block{Return{e}}
}

func Verify(f *Function) error {
	if f == nil || f.Definition == nil || f.Definition.Type == nil {
		return fmt.Errorf("lower: missing typed definition")
	}
	_, result := core.PeelFun(f.Definition.Type, len(f.Definition.Params))
	var check func(Block, bool) error
	var tree func(core.Tree, bool) error
	tree = func(t core.Tree, loop bool) error {
		switch t := t.(type) {
		case *core.Leaf:
			return check(Tail(f.Definition, t.Body, loop), loop)
		case *core.Guard:
			if t.Cond == nil || t.Cond.Type() == nil {
				return fmt.Errorf("lower: untyped guard")
			}
			if err := tree(t.Then, loop); err != nil {
				return err
			}
			return tree(t.Else, loop)
		case *core.SwitchCtor:
			for _, c := range t.Cases {
				if err := tree(c.Tree, loop); err != nil {
					return err
				}
			}
			if t.Default != nil {
				return tree(t.Default, loop)
			}
			return nil
		case *core.SwitchLit:
			for _, c := range t.Cases {
				if err := tree(c.Tree, loop); err != nil {
					return err
				}
			}
			return tree(t.Default, loop)
		case *core.Unreachable:
			return nil
		default:
			return fmt.Errorf("lower: invalid decision tree %T", t)
		}
	}
	check = func(block Block, loop bool) error {
		if len(block) == 0 {
			return fmt.Errorf("lower: missing terminator")
		}
		terminal := false
		for _, statement := range block {
			if terminal {
				return fmt.Errorf("lower: statement after terminator")
			}
			switch s := statement.(type) {
			case Loop:
				terminal = true
				if !f.Loop || loop {
					return fmt.Errorf("lower: %s: invalid loop", f.Definition.Name)
				}
				if err := check(s.Body, true); err != nil {
					return err
				}
			case Continue:
				terminal = true
				if !loop || !core.IsTailLoopCall(f.Definition, s.Call) {
					return fmt.Errorf("lower: %s: invalid continue", f.Definition.Name)
				}
			case Branch:
				terminal = true
				if s.Condition == nil || s.Condition.Type() == nil {
					return fmt.Errorf("lower: missing condition")
				}
				if err := check(s.Then, loop); err != nil {
					return err
				}
				if err := check(s.Else, loop); err != nil {
					return err
				}
			case Match:
				terminal = true
				if s.Case == nil || s.Case.Scrut == nil || s.Case.Scrut.Type() == nil {
					return fmt.Errorf("lower: missing match")
				}
				if err := tree(s.Case.Tree, loop); err != nil {
					return err
				}
			case Bind:
				if s.Let == nil || s.Let.Rhs == nil || s.Let.Rhs.Type() == nil {
					return fmt.Errorf("lower: missing binding")
				}
			case Eval:
				if s.Value == nil || s.Value.Type() == nil {
					return fmt.Errorf("lower: missing evaluation")
				}
			case Return:
				terminal = true
				if s.Value == nil || s.Value.Type() == nil {
					return fmt.Errorf("lower: untyped return")
				}
				if !types.Equal(s.Value.Type(), result) {
					return fmt.Errorf("lower: %s: return type differs from worker result", f.Definition.Name)
				}
			default:
				return fmt.Errorf("lower: unknown statement %T", statement)
			}
		}
		if !terminal {
			return fmt.Errorf("lower: missing terminator")
		}
		return nil
	}
	return check(f.Body, false)
}

// Dump records decisions without graph-local IDs or Go spelling.
func Dump(p *Program, order []core.Def) string {
	var b strings.Builder
	for _, d := range order {
		f := p.Functions[d.Name]
		if f == nil {
			continue
		}
		fmt.Fprintf(&b, "%s: %s loop=%t\n", d.Name, core.ControlName(d.Control), f.Loop)
		for i, c := range d.ABI.Callbacks {
			if c.Arity != 0 {
				fmt.Fprintf(&b, "  %s: arity=%d direct=%d exit=%d\n", d.Params[i], c.Arity, c.DirectUses, c.ExitUses)
			}
		}
		var walk func(Block, string)
		walk = func(block Block, indent string) {
			for _, s := range block {
				switch s := s.(type) {
				case Bind:
					fmt.Fprintf(&b, "%sbind %s: %s\n", indent, s.Let.Name, types.Show(s.Let.Rhs.Type()))
				case Eval:
					fmt.Fprintf(&b, "%seval %T\n", indent, s.Value)
				case Return:
					fmt.Fprintf(&b, "%sreturn %T %s\n", indent, s.Value, core.ControlName(core.ExprControl(s.Value)))
				case Continue:
					fmt.Fprintf(&b, "%scontinue\n", indent)
				case Match:
					fmt.Fprintf(&b, "%smatch\n", indent)
				case Branch:
					fmt.Fprintf(&b, "%sbranch\n", indent)
					walk(s.Then, indent+"  ")
					walk(s.Else, indent+"  ")
				case Loop:
					fmt.Fprintf(&b, "%sloop\n", indent)
					walk(s.Body, indent+"  ")
				}
			}
		}
		walk(f.Body, "  ")
	}
	return b.String()
}
