package codegen

import (
	"fmt"
	goast "go/ast"
	gotoken "go/token"
	"sort"

	"github.com/waj/fango/internal/core"
	machineir "github.com/waj/fango/internal/machine"
	"github.com/waj/fango/internal/types"
)

func (g *gen) machineDecls(p *machineir.Prog) ([]goast.Decl, error) {
	var workers []*machineir.Worker
	for i := range p.Workers {
		if p.Workers[i].Owner == g.unit {
			workers = append(workers, &p.Workers[i])
		}
	}
	sort.Slice(workers, func(i, j int) bool { return workers[i].Name < workers[j].Name })
	var decls []goast.Decl
	for _, worker := range workers {
		d := g.defs[worker.Name]
		if d == nil {
			return nil, fmt.Errorf("codegen: machine worker %q has no semantic Core definition", worker.Name)
		}
		if len(d.TyParams) != 0 {
			return nil, fmt.Errorf("codegen: generic Machine worker %q is not implemented", worker.Name)
		}
		if len(d.EffectParams) != 0 {
			return nil, fmt.Errorf("codegen: Machine evidence parameters for %q are not implemented", worker.Name)
		}
		if err := directMachineTerms(worker); err != nil {
			return nil, err
		}
		decls = append(decls, g.machineWorkerDecls(worker)...)
	}
	return decls, nil
}

func directMachineTerms(worker *machineir.Worker) error {
	check := func(e core.Expr) error {
		if control := core.ExprControl(e); control != (types.Control{}) {
			return fmt.Errorf("codegen: Machine worker %q still has %s expression control", worker.Name, core.ControlName(control))
		}
		return nil
	}
	for _, block := range worker.Blocks {
		switch term := block.Term.(type) {
		case *machineir.Eval:
			if err := check(term.Value); err != nil {
				return err
			}
		case *machineir.Branch:
			if err := check(term.Cond); err != nil {
				return err
			}
		case *machineir.SwitchCtor:
		case *machineir.SwitchLit:
			for _, c := range term.Cases {
				if err := check(c.Lit); err != nil {
					return err
				}
			}
		case *machineir.Suspend:
			if err := check(term.Request); err != nil {
				return err
			}
		case *machineir.Call:
			if len(term.EvidenceArgs) != 0 {
				return fmt.Errorf("codegen: Machine call from %q carries evidence", worker.Name)
			}
			for _, arg := range term.Args {
				if err := check(arg); err != nil {
					return err
				}
			}
		case *machineir.PushCleanup:
			if err := check(term.Acquire); err != nil {
				return err
			}
			if err := check(term.Release); err != nil {
				return err
			}
		case *machineir.PopCleanup:
		case *machineir.Return:
			if err := check(term.Value); err != nil {
				return err
			}
		}
	}
	return nil
}

func (g *gen) machineWorkerDecls(worker *machineir.Worker) []goast.Decl {
	g.usesFangort = true
	frameName := machineFrameName(worker.Name)
	stored := machineStoredLocals(worker)
	fields := []*goast.Field{{Names: []*goast.Ident{ident("PC")}, Type: ident("int")}}
	for _, local := range stored {
		fields = append(fields, &goast.Field{Names: []*goast.Ident{ident(machineFieldName(local.Name))}, Type: g.goType(local.Ty)})
	}
	frameDecl := &goast.GenDecl{Tok: gotoken.TYPE, Specs: []goast.Spec{&goast.TypeSpec{
		Name: ident(frameName), Type: &goast.StructType{Fields: &goast.FieldList{List: fields}},
	}}}

	params := make([]paramSpec, len(worker.Params))
	ctorFields := []goast.Expr{&goast.KeyValueExpr{Key: ident("PC"), Value: intLit(int64(worker.Entry))}}
	for i, param := range worker.Params {
		name := machineLocalName(param.Name)
		params[i] = paramSpec{name: name, typ: g.goType(param.Ty)}
		ctorFields = append(ctorFields, &goast.KeyValueExpr{Key: ident(machineFieldName(param.Name)), Value: ident(name)})
	}
	ctor := workerDecl(machineConstructorName(worker.Name), params, selector("fangort", "MachineFrame"), []goast.Stmt{
		returnStmt(&goast.UnaryExpr{Op: gotoken.AND, X: &goast.CompositeLit{Type: ident(frameName), Elts: ctorFields}}),
	})

	clear := &goast.FuncDecl{
		Recv: &goast.FieldList{List: []*goast.Field{{Names: []*goast.Ident{ident("f")}, Type: &goast.StarExpr{X: ident(frameName)}}}},
		Name: ident("Clear"), Type: &goast.FuncType{Params: &goast.FieldList{}},
		Body: &goast.BlockStmt{List: []goast.Stmt{&goast.AssignStmt{
			Lhs: []goast.Expr{&goast.StarExpr{X: ident("f")}}, Tok: gotoken.ASSIGN,
			Rhs: []goast.Expr{&goast.CompositeLit{Type: ident(frameName)}},
		}}},
	}
	step := g.machineStepDecl(worker, frameName, stored)
	return []goast.Decl{frameDecl, ctor, clear, step}
}

func (g *gen) machineStepDecl(worker *machineir.Worker, frameName string, stored []machineir.Local) goast.Decl {
	oldControl, oldABI, oldResult := g.control, g.abi, g.resultType
	g.control, g.abi, g.resultType = types.Direct, types.Direct, worker.Result
	defer func() { g.control, g.abi, g.resultType = oldControl, oldABI, oldResult }()
	for _, local := range worker.Locals {
		g.caseVarTys[local.Name] = local.Ty
	}
	defer func() {
		for _, local := range worker.Locals {
			delete(g.caseVarTys, local.Name)
		}
	}()

	storedSet := map[string]bool{}
	for _, local := range stored {
		storedSet[local.Name] = true
	}
	var body []goast.Stmt
	for _, local := range worker.Locals {
		name := machineLocalName(local.Name)
		if storedSet[local.Name] {
			body = append(body, varDeclStmt(name, g.goType(local.Ty), machineFrameField(local.Name)))
		} else {
			body = append(body, varDeclNoValue(name, g.goType(local.Ty)))
		}
		body = append(body, assignBlank(ident(name)))
	}

	var clauses []goast.Stmt
	resumePC := len(worker.Blocks)
	for _, block := range worker.Blocks {
		stmts, resume := g.machineBlockStmts(worker, frameName, &block, resumePC, stored)
		clauses = append(clauses, &goast.CaseClause{List: []goast.Expr{intLit(int64(block.ID))}, Body: stmts})
		if resume != nil {
			clauses = append(clauses, &goast.CaseClause{List: []goast.Expr{intLit(int64(resumePC))}, Body: resume})
			resumePC++
		}
	}
	clauses = append(clauses, &goast.CaseClause{Body: []goast.Stmt{&goast.ExprStmt{X: callExpr(ident("panic"), stringLit("invalid generated machine PC"))}}})
	body = append(body, &goast.ForStmt{Body: &goast.BlockStmt{List: []goast.Stmt{&goast.SwitchStmt{
		Tag: machinePC(), Body: &goast.BlockStmt{List: clauses},
	}}}})
	return &goast.FuncDecl{
		Recv: &goast.FieldList{List: []*goast.Field{{Names: []*goast.Ident{ident("f")}, Type: &goast.StarExpr{X: ident(frameName)}}}},
		Name: ident("Step"),
		Type: &goast.FuncType{Params: paramFields([]paramSpec{{name: "m", typ: &goast.StarExpr{X: selector("fangort", "Machine")}}}),
			Results: &goast.FieldList{List: []*goast.Field{{Type: selector("fangort", "MachineStep")}}}},
		Body: &goast.BlockStmt{List: body},
	}
}

func (g *gen) machineBlockStmts(worker *machineir.Worker, frameName string, block *machineir.Block, resumePC int, stored []machineir.Local) ([]goast.Stmt, []goast.Stmt) {
	continueStmt := func(next machineir.BlockID) []goast.Stmt {
		return []goast.Stmt{assignMachinePC(int(next)), &goast.BranchStmt{Tok: gotoken.CONTINUE}}
	}
	resume := func(bind machineir.Local, next machineir.BlockID) []goast.Stmt {
		value := &goast.TypeAssertExpr{X: callExpr(&goast.SelectorExpr{X: ident("m"), Sel: ident("TakeResult")}), Type: g.goType(bind.Ty)}
		return append([]goast.Stmt{assignStmt(machineLocalName(bind.Name), value)}, continueStmt(next)...)
	}
	step := func(kind string, key string, value goast.Expr) goast.Stmt {
		fields := []goast.Expr{&goast.KeyValueExpr{Key: ident("Kind"), Value: selector("fangort", kind)}}
		if key != "" {
			fields = append(fields, &goast.KeyValueExpr{Key: ident(key), Value: value})
		}
		return returnStmt(&goast.CompositeLit{Type: selector("fangort", "MachineStep"), Elts: fields})
	}
	save := func() []goast.Stmt {
		out := []goast.Stmt{&goast.AssignStmt{
			Lhs: []goast.Expr{&goast.StarExpr{X: ident("f")}}, Tok: gotoken.ASSIGN,
			Rhs: []goast.Expr{&goast.CompositeLit{Type: ident(frameName)}},
		}}
		live := map[string]bool{}
		for _, name := range block.LiveOut {
			live[name] = true
		}
		for _, local := range stored {
			name := local.Name
			if live[name] {
				out = append(out, &goast.AssignStmt{Lhs: []goast.Expr{machineFrameField(name)}, Tok: gotoken.ASSIGN, Rhs: []goast.Expr{ident(machineLocalName(name))}})
			}
		}
		return out
	}
	switch term := block.Term.(type) {
	case *machineir.Eval:
		return append([]goast.Stmt{assignStmt(machineLocalName(term.Bind.Name), g.expr(term.Value, 0))}, continueStmt(term.Next)...), nil
	case *machineir.Branch:
		return []goast.Stmt{ifStmt(g.expr(term.Cond, 0), continueStmt(term.Then), continueStmt(term.Else))}, nil
	case *machineir.SwitchCtor:
		return g.machineCtorSwitch(term, continueStmt), nil
	case *machineir.SwitchLit:
		var clauses []goast.Stmt
		for _, c := range term.Cases {
			clauses = append(clauses, &goast.CaseClause{List: []goast.Expr{g.expr(c.Lit, 0)}, Body: continueStmt(c.Next)})
		}
		clauses = append(clauses, &goast.CaseClause{Body: continueStmt(term.Default)})
		return []goast.Stmt{&goast.SwitchStmt{Tag: ident(machineLocalName(term.Scrut)), Body: &goast.BlockStmt{List: clauses}}}, nil
	case *machineir.Suspend:
		stmts := append(save(), assignMachinePC(resumePC))
		stmts = append(stmts, step("MachineSuspend", "Request", g.machineBoxedValue(term.Request)))
		return stmts, resume(term.Bind, term.Next)
	case *machineir.Call:
		args := make([]goast.Expr, len(term.Args))
		for i, arg := range term.Args {
			args[i] = g.expr(arg, 0)
		}
		child := callExpr(g.machineConstructorRef(term.Callee), args...)
		if term.Tail {
			return []goast.Stmt{step("MachineTailCall", "Frame", child)}, nil
		}
		stmts := append(save(), assignMachinePC(resumePC))
		stmts = append(stmts, step("MachineCall", "Frame", child))
		return stmts, resume(term.Bind, term.Next)
	case *machineir.PushCleanup:
		cleanup := funcLit(&goast.StarExpr{X: selector("fangort", "ExitRequest")}, []goast.Stmt{
			assignBlank(g.expr(term.Release, 0)),
			returnStmt(ident("nil")),
		})
		stmts := []goast.Stmt{
			assignStmt(machineLocalName(term.Resource.Name), g.expr(term.Acquire, 0)),
			exprStmt(callExpr(&goast.SelectorExpr{X: ident("m"), Sel: ident("PushCleanup")}, cleanup)),
		}
		return append(stmts, continueStmt(term.Next)...), nil
	case *machineir.PopCleanup:
		exitName := fmt.Sprintf("machineCleanupExit%d", block.ID)
		exitValue := callExpr(&goast.SelectorExpr{X: ident("m"), Sel: ident("PopCleanup")})
		failure := []goast.Stmt{step("MachineExit", "Exit", ident(exitName))}
		stmts := []goast.Stmt{
			varDeclStmt(exitName, &goast.StarExpr{X: selector("fangort", "ExitRequest")}, exitValue),
			&goast.IfStmt{Cond: binExpr(gotoken.NEQ, ident(exitName), ident("nil")), Body: &goast.BlockStmt{List: failure}},
		}
		return append(stmts, continueStmt(term.Next)...), nil
	case *machineir.Return:
		return []goast.Stmt{step("MachineReturn", "Value", g.machineBoxedValue(term.Value))}, nil
	default:
		panic(fmt.Sprintf("codegen: unknown machine term %T", block.Term))
	}
}

func (g *gen) machineCtorSwitch(term *machineir.SwitchCtor, jump func(machineir.BlockID) []goast.Stmt) []goast.Stmt {
	scrut := ident(machineLocalName(term.Scrut))
	scrutTy := g.caseVarTys[term.Scrut].(*types.TCon)
	caseByName := map[string]machineir.CtorCase{}
	for _, c := range term.Cases {
		caseByName[types.SurfaceName(c.Ctor.Name)] = c
	}
	defaultBody := func() []goast.Stmt {
		if term.Default != nil {
			return jump(*term.Default)
		}
		return []goast.Stmt{&goast.ExprStmt{X: callExpr(ident("panic"), stringLit("exhaustive generated machine match failed"))}}
	}
	caseBody := func(c machineir.CtorCase, fields []goast.Expr) []goast.Stmt {
		var out []goast.Stmt
		for i, bind := range c.Binds {
			if bind.Name != "" {
				out = append(out, assignStmt(machineLocalName(bind.Name), fields[i]))
			}
		}
		return append(out, jump(c.Next)...)
	}

	if g.unique(scrutTy) == g.b.Bool.Unique {
		truth, hasTruth := caseByName["True"]
		falsity, hasFalsity := caseByName["False"]
		thenBody, elseBody := defaultBody(), defaultBody()
		if hasTruth {
			thenBody = caseBody(truth, nil)
		}
		if hasFalsity {
			elseBody = caseBody(falsity, nil)
		}
		return []goast.Stmt{ifStmt(scrut, thenBody, elseBody)}
	}
	if term.ADT.Repr == types.ReprList {
		nilCase, hasNil := caseByName["Nil"]
		consCase, hasCons := caseByName["Cons"]
		emptyBody, consBody := defaultBody(), defaultBody()
		if hasNil {
			emptyBody = caseBody(nilCase, nil)
		}
		if hasCons {
			consBody = caseBody(consCase, []goast.Expr{
				callExpr(&goast.SelectorExpr{X: scrut, Sel: ident("Head")}),
				callExpr(&goast.SelectorExpr{X: scrut, Sel: ident("Tail")}),
			})
		}
		return []goast.Stmt{ifStmt(callExpr(&goast.SelectorExpr{X: scrut, Sel: ident("IsEmpty")}), emptyBody, consBody)}
	}

	tagArgs := g.goTypes(runtimeADTArgs(term.ADT, scrutTy.Args))
	tsName := fmt.Sprintf("machineCase%d", g.tmp)
	g.tmp++
	var clauses []goast.Stmt
	for _, c := range term.Cases {
		fields := make([]goast.Expr, len(c.Binds))
		for i := range fields {
			fields[i] = &goast.SelectorExpr{X: ident(tsName), Sel: ident(fieldName(i))}
		}
		body := append([]goast.Stmt{assignBlank(ident(tsName))}, caseBody(c, fields)...)
		tag := &goast.StarExpr{X: indexExpr(g.ctorRef(c.Ctor), tagArgs)}
		clauses = append(clauses, &goast.CaseClause{List: []goast.Expr{tag}, Body: body})
	}
	clauses = append(clauses, &goast.CaseClause{Body: defaultBody()})
	return []goast.Stmt{&goast.TypeSwitchStmt{
		Assign: &goast.AssignStmt{Lhs: []goast.Expr{ident(tsName)}, Tok: gotoken.DEFINE, Rhs: []goast.Expr{&goast.TypeAssertExpr{X: scrut}}},
		Body:   &goast.BlockStmt{List: clauses},
	}}
}

func (g *gen) machineBoxedValue(e core.Expr) goast.Expr {
	return callExpr(g.goType(e.Type()), g.expr(e, 0))
}

func machineStoredLocals(worker *machineir.Worker) []machineir.Local {
	seen := map[string]bool{}
	var out []machineir.Local
	for _, group := range [][]machineir.Local{worker.Params, worker.Frame} {
		for _, local := range group {
			if !seen[local.Name] {
				seen[local.Name] = true
				out = append(out, local)
			}
		}
	}
	return out
}

func machineFrameName(name string) string       { return "machineFrame_" + linkName(name) }
func machineConstructorName(name string) string { return "MachineFrame_" + linkName(name) }
func machineFieldName(name string) string       { return "L_" + linkName(name) }
func machineLocalName(name string) string       { return mangleValue(name) }

func (g *gen) machineConstructorRef(name string) goast.Expr {
	owner := symbolOwner(name)
	if d := g.defs[name]; d != nil {
		owner = d.Owner
	}
	return g.qualified(owner, machineConstructorName(name))
}

func machinePC() goast.Expr { return &goast.SelectorExpr{X: ident("f"), Sel: ident("PC")} }
func machineFrameField(name string) goast.Expr {
	return &goast.SelectorExpr{X: ident("f"), Sel: ident(machineFieldName(name))}
}
func assignMachinePC(pc int) goast.Stmt {
	return &goast.AssignStmt{Lhs: []goast.Expr{machinePC()}, Tok: gotoken.ASSIGN, Rhs: []goast.Expr{intLit(int64(pc))}}
}
