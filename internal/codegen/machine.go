package codegen

import (
	"fmt"
	goast "go/ast"
	gotoken "go/token"
	"slices"
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
			d = worker.Def
		}
		if d == nil {
			return nil, fmt.Errorf("codegen: machine worker %q has no semantic Core definition", worker.Name)
		}
		if err := directMachineTerms(worker); err != nil {
			return nil, err
		}
		decls = append(decls, g.machineWorkerDecls(worker)...)
	}
	return decls, nil
}

func directMachineTerms(worker *machineir.Worker) error {
	check := func(e core.Expr, allowExit bool) error {
		control := core.ExprControl(e)
		if control.Polymorphic || control.Transport == types.Machine || (!allowExit && control.Transport != types.Direct) {
			return fmt.Errorf("codegen: Machine worker %q still has %s expression control", worker.Name, core.ControlName(control))
		}
		return nil
	}
	for _, block := range worker.Blocks {
		switch term := block.Term.(type) {
		case *machineir.Eval:
			if err := check(term.Value, true); err != nil {
				return err
			}
		case *machineir.Branch:
			if err := check(term.Cond, false); err != nil {
				return err
			}
		case *machineir.SwitchCtor:
		case *machineir.SwitchLit:
			for _, c := range term.Cases {
				if err := check(c.Lit, false); err != nil {
					return err
				}
			}
		case *machineir.Suspend:
			if err := check(term.Request, false); err != nil {
				return err
			}
		case *machineir.CursorAdvance:
			if err := check(term.Cursor, false); err != nil {
				return err
			}
		case *machineir.CursorOpen:
			if err := check(term.Producer, false); err != nil {
				return err
			}
		case *machineir.CursorClose:
		case *machineir.Call:
			for _, arg := range term.Args {
				if err := check(arg, false); err != nil {
					return err
				}
			}
		case *machineir.Handle:
		case *machineir.StateResume:
			if err := check(term.Value, false); err != nil {
				return err
			}
			if err := check(term.NextState, false); err != nil {
				return err
			}
		case *machineir.PushCleanup:
			if err := check(term.Acquire, true); err != nil {
				return err
			}
			if err := check(term.Release, true); err != nil {
				return err
			}
		case *machineir.PopCleanup:
		case *machineir.Return:
			if err := check(term.Value, true); err != nil {
				return err
			}
		}
	}
	return nil
}

func (g *gen) machineWorkerDecls(worker *machineir.Worker) []goast.Decl {
	g.usesFangort = true
	oldControl, oldABI := g.control, g.abi
	g.control, g.abi = types.Machine, types.Machine
	defer func() { g.control, g.abi = oldControl, oldABI }()
	oldNames := g.tyParamNames
	g.tyParamNames = tyParamNames(worker.TyParams)
	defer func() { g.tyParamNames = oldNames }()
	frameName := machineFrameName(worker.Name)
	stored := machineStoredLocals(worker)
	fields := []*goast.Field{{Names: []*goast.Ident{ident("PC")}, Type: ident("int")}}
	for _, param := range worker.TyParams {
		fields = append(fields, &goast.Field{Names: []*goast.Ident{ident("T_" + g.tyParamNames[param.ID])}, Type: g.descriptorType()})
	}
	for i := range worker.Rows {
		fields = append(fields, &goast.Field{Names: []*goast.Ident{ident(fmt.Sprintf("R%d", i))}, Type: g.rowType()})
	}
	if worker.StateToken {
		fields = append(fields, &goast.Field{Names: []*goast.Ident{ident("StateToken")}, Type: ident("int")})
	}
	for _, ev := range worker.EffectParams {
		fields = append(fields, &goast.Field{
			Names: []*goast.Ident{ident(machineEvidenceFieldName(ev))},
			Type:  g.effectTypeMode(ev, types.Machine),
		})
	}
	for _, local := range stored {
		fields = append(fields, &goast.Field{Names: []*goast.Ident{ident(machineFieldName(local.Name))}, Type: g.goType(local.Ty)})
	}
	frameSpec := &goast.TypeSpec{Name: ident(frameName), Type: &goast.StructType{Fields: &goast.FieldList{List: fields}}}
	frameSpec.TypeParams = g.typeParamFields(worker.TyParams)
	frameDecl := &goast.GenDecl{Tok: gotoken.TYPE, Specs: []goast.Spec{frameSpec}}

	params := g.descriptorParams(worker.TyParams)
	ctorFields := []goast.Expr{&goast.KeyValueExpr{Key: ident("PC"), Value: intLit(int64(worker.Entry))}}
	for _, param := range worker.TyParams {
		ctorFields = append(ctorFields, &goast.KeyValueExpr{Key: ident("T_" + g.tyParamNames[param.ID]), Value: g.typeDescriptor(param)})
	}
	for _, ev := range worker.EffectParams {
		name := machineEvidenceName(ev)
		params = append(params, paramSpec{name: name, typ: g.effectTypeMode(ev, types.Machine)})
		ctorFields = append(ctorFields, &goast.KeyValueExpr{Key: ident(machineEvidenceFieldName(ev)), Value: ident(name)})
	}
	for i := range worker.Rows {
		name := fmt.Sprintf("machineRow%d", i)
		params = append(params, paramSpec{name: name, typ: g.rowType()})
		ctorFields = append(ctorFields, &goast.KeyValueExpr{Key: ident(fmt.Sprintf("R%d", i)), Value: ident(name)})
	}
	if worker.StateToken {
		params = append(params, paramSpec{name: "machineStateToken", typ: ident("int")})
		ctorFields = append(ctorFields, &goast.KeyValueExpr{Key: ident("StateToken"), Value: ident("machineStateToken")})
	}
	for _, param := range worker.Params {
		name := machineLocalName(param.Name)
		params = append(params, paramSpec{name: name, typ: g.goType(param.Ty)})
		ctorFields = append(ctorFields, &goast.KeyValueExpr{Key: ident(machineFieldName(param.Name)), Value: ident(name)})
	}
	ctor := workerDecl(machineConstructorName(worker.Name), params, selector("fangort", "MachineFrame"), []goast.Stmt{
		returnStmt(&goast.UnaryExpr{Op: gotoken.AND, X: &goast.CompositeLit{Type: indexExpr(ident(frameName), machineTypeParamIdents(worker.TyParams)), Elts: ctorFields}}),
	})
	ctor.(*goast.FuncDecl).Type.TypeParams = g.typeParamFields(worker.TyParams)

	receiverType := indexExpr(ident(frameName), machineTypeParamIdents(worker.TyParams))
	clear := &goast.FuncDecl{
		Recv: &goast.FieldList{List: []*goast.Field{{Names: []*goast.Ident{ident("f")}, Type: &goast.StarExpr{X: receiverType}}}},
		Name: ident("Clear"), Type: &goast.FuncType{Params: &goast.FieldList{}},
		Body: &goast.BlockStmt{List: []goast.Stmt{&goast.AssignStmt{
			Lhs: []goast.Expr{&goast.StarExpr{X: ident("f")}}, Tok: gotoken.ASSIGN,
			Rhs: []goast.Expr{&goast.CompositeLit{Type: receiverType}},
		}}},
	}
	step := g.machineStepDecl(worker, frameName, stored)
	start := g.machineStartDecl(worker, params)
	return []goast.Decl{frameDecl, ctor, clear, step, start}
}

func (g *gen) machineStepDecl(worker *machineir.Worker, frameName string, stored []machineir.Local) goast.Decl {
	oldControl, oldABI, oldResult := g.control, g.abi, g.resultType
	g.control, g.abi, g.resultType = types.Direct, types.Machine, worker.Result
	defer func() { g.control, g.abi, g.resultType = oldControl, oldABI, oldResult }()
	for i, row := range worker.Rows {
		defer g.pushRow(row, ident(fmt.Sprintf("machineRow%d", i)))()
	}
	if worker.RowParam != 0 {
		defer g.bindDeferredEffects(worker.RowEffects, g.rowValue(worker.RowParam), types.Machine)()
	}
	for _, ev := range worker.EffectParams {
		name := machineEvidenceName(ev)
		g.evidence[ev.Unique] = append(g.evidence[ev.Unique], ident(name))
		g.evidenceModes[ev.Unique] = append(g.evidenceModes[ev.Unique], machineir.Mode(ev.Control))
	}
	defer func() {
		for _, ev := range worker.EffectParams {
			g.evidence[ev.Unique] = g.evidence[ev.Unique][:len(g.evidence[ev.Unique])-1]
			g.evidenceModes[ev.Unique] = g.evidenceModes[ev.Unique][:len(g.evidenceModes[ev.Unique])-1]
		}
	}()
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
	for i := range worker.Rows {
		name := fmt.Sprintf("machineRow%d", i)
		body = append(body, varDeclStmt(name, g.rowType(), selector("f", fmt.Sprintf("R%d", i))), assignBlank(ident(name)))
	}
	for _, param := range worker.TyParams {
		name := descriptorParamName(g.tyParamNames[param.ID])
		body = append(body, varDeclStmt(name, g.descriptorType(), &goast.SelectorExpr{X: ident("f"), Sel: ident("T_" + g.tyParamNames[param.ID])}), assignBlank(ident(name)))
	}
	if worker.StateToken {
		body = append(body, varDeclStmt("machineStateToken", ident("int"), &goast.SelectorExpr{X: ident("f"), Sel: ident("StateToken")}))
	}
	for _, ev := range worker.EffectParams {
		name := machineEvidenceName(ev)
		body = append(body,
			varDeclStmt(name, g.effectTypeMode(ev, types.Machine), machineFrameEvidenceField(ev)),
			assignBlank(ident(name)),
		)
	}
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
		stmts, resumes := g.machineBlockStmts(worker, frameName, &block, resumePC, stored)
		clauses = append(clauses, &goast.CaseClause{List: []goast.Expr{intLit(int64(block.ID))}, Body: stmts})
		for _, resume := range resumes {
			clauses = append(clauses, &goast.CaseClause{List: []goast.Expr{intLit(int64(resumePC))}, Body: resume})
			resumePC++
		}
	}
	clauses = append(clauses, &goast.CaseClause{Body: []goast.Stmt{returnStmt(callExpr(selector("fangort", "InvalidMachineStep"), stringLit("invalid generated machine PC")))}})
	body = append(body, &goast.ForStmt{Body: &goast.BlockStmt{List: []goast.Stmt{&goast.SwitchStmt{
		Tag: machinePC(), Body: &goast.BlockStmt{List: clauses},
	}}}})
	return &goast.FuncDecl{
		Recv: &goast.FieldList{List: []*goast.Field{{Names: []*goast.Ident{ident("f")}, Type: &goast.StarExpr{X: indexExpr(ident(frameName), machineTypeParamIdents(worker.TyParams))}}}},
		Name: ident("Step"),
		Type: &goast.FuncType{Params: paramFields([]paramSpec{{name: "m", typ: &goast.StarExpr{X: selector("fangort", "Machine")}}}),
			Results: &goast.FieldList{List: []*goast.Field{{Type: selector("fangort", "MachineStep")}}}},
		Body: &goast.BlockStmt{List: body},
	}
}

func (g *gen) machineBlockStmts(worker *machineir.Worker, frameName string, block *machineir.Block, resumePC int, stored []machineir.Local) ([]goast.Stmt, [][]goast.Stmt) {
	continueStmt := func(next machineir.BlockID) []goast.Stmt {
		return []goast.Stmt{assignMachinePC(int(next)), &goast.BranchStmt{Tok: gotoken.CONTINUE}}
	}
	resume := func(bind machineir.Local, next machineir.BlockID) []goast.Stmt {
		value := &goast.TypeAssertExpr{X: callExpr(&goast.SelectorExpr{X: ident("m"), Sel: ident("TakeResult")}), Type: g.goType(bind.Ty)}
		return append([]goast.Stmt{assignStmt(machineLocalName(bind.Name), value)}, continueStmt(next)...)
	}
	step := func(kind string, key string, value goast.Expr, extra ...goast.Expr) goast.Stmt {
		fields := []goast.Expr{&goast.KeyValueExpr{Key: ident("Kind"), Value: selector("fangort", kind)}}
		if key != "" {
			fields = append(fields, &goast.KeyValueExpr{Key: ident(key), Value: value})
		}
		return returnStmt(&goast.CompositeLit{Type: selector("fangort", "MachineStep"), Elts: append(fields, extra...)})
	}
	// A machine block may run a non-suspending Exit expression. Emit it with
	// the existing Outcome ABI, then turn a failed outcome into an explicit
	// dispatcher transition before exposing its normal value to the block.
	value := func(e core.Expr) ([]goast.Stmt, goast.Expr) {
		control := core.ExprControl(e)
		oldControl, oldResult := g.control, g.resultType
		if control.Transport == types.Exit {
			g.control, g.resultType = types.Exit, e.Type()
		}
		expr := g.machineExpr(e)
		g.control, g.resultType = oldControl, oldResult
		if control.Transport != types.Exit {
			return nil, expr
		}
		name := fmt.Sprintf("machineOutcome%d", g.tmp)
		g.tmp++
		stmts := []goast.Stmt{
			varDeclStmt(name, g.outcomeType(e.Type()), expr),
			&goast.IfStmt{
				Cond: &goast.BinaryExpr{X: selector(name, "Exit"), Op: gotoken.NEQ, Y: ident("nil")},
				Body: &goast.BlockStmt{List: []goast.Stmt{step("MachineExit", "Exit", selector(name, "Exit"))}},
			},
		}
		return stmts, selector(name, "Value")
	}
	save := func() []goast.Stmt {
		out := []goast.Stmt{&goast.AssignStmt{
			Lhs: []goast.Expr{&goast.StarExpr{X: ident("f")}}, Tok: gotoken.ASSIGN,
			Rhs: []goast.Expr{&goast.CompositeLit{Type: indexExpr(ident(frameName), machineTypeParamIdents(worker.TyParams))}},
		}}
		for _, param := range worker.TyParams {
			out = append(out, &goast.AssignStmt{
				Lhs: []goast.Expr{&goast.SelectorExpr{X: ident("f"), Sel: ident("T_" + g.tyParamNames[param.ID])}}, Tok: gotoken.ASSIGN,
				Rhs: []goast.Expr{g.typeDescriptor(param)},
			})
		}
		for i, row := range worker.Rows {
			out = append(out, &goast.AssignStmt{Lhs: []goast.Expr{selector("f", fmt.Sprintf("R%d", i))}, Tok: gotoken.ASSIGN, Rhs: []goast.Expr{g.rowValue(row)}})
		}
		for _, ev := range worker.EffectParams {
			out = append(out, &goast.AssignStmt{
				Lhs: []goast.Expr{machineFrameEvidenceField(ev)}, Tok: gotoken.ASSIGN,
				Rhs: []goast.Expr{ident(machineEvidenceName(ev))},
			})
		}
		if worker.StateToken {
			out = append(out, &goast.AssignStmt{Lhs: []goast.Expr{&goast.SelectorExpr{X: ident("f"), Sel: ident("StateToken")}},
				Tok: gotoken.ASSIGN, Rhs: []goast.Expr{ident("machineStateToken")}})
		}
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
		if match := machineir.ConstructorMatch(worker, term); match != nil {
			a := term.Value.(*core.App)
			var stmts []goast.Stmt
			var values []string
			for _, arg := range a.Args {
				name := fmt.Sprintf("machineField%d", g.tmp)
				g.tmp++
				values = append(values, name)
				stmts = append(stmts, varDeclStmt(name, g.goType(arg.Type()), g.machineExpr(arg)), assignBlank(ident(name)))
			}
			for i, bind := range match.Binds {
				if bind.Name != "" {
					stmts = append(stmts, assignStmt(machineLocalName(bind.Name), ident(values[i])))
				}
			}
			return append(stmts, continueStmt(match.Next)...), nil
		}
		prefix, normal := value(term.Value)
		prefix = append(prefix, assignStmt(machineLocalName(term.Bind.Name), normal))
		return append(prefix, continueStmt(term.Next)...), nil
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
		return stmts, [][]goast.Stmt{resume(term.Bind, term.Next)}
	case *machineir.CursorAdvance:
		stmts := append(save(), assignMachinePC(resumePC))
		extra := []goast.Expr{&goast.KeyValueExpr{Key: ident("Evidence"), Value: g.rowArgument(term.Row)}}
		if term.Reply != nil {
			extra = append(extra, &goast.KeyValueExpr{Key: ident("Reply"), Value: g.machineBoxedValue(term.Reply)})
		}
		if term.Close {
			extra = append(extra, &goast.KeyValueExpr{Key: ident("Close"), Value: ident("true")})
		}
		stmts = append(stmts, step("MachineAdvance", "Cursor", g.machineExpr(term.Cursor), extra...))
		name := fmt.Sprintf("machinePull%d", g.tmp)
		g.tmp++
		resultTy := term.Bind.Ty.(*types.TCon)
		resumed := []goast.Stmt{varDeclStmt(name, selector("fangort", "CursorResult"), &goast.TypeAssertExpr{X: callExpr(selector("m", "TakeResult")), Type: selector("fangort", "CursorResult")}),
			&goast.IfStmt{Cond: &goast.BinaryExpr{X: selector(name, "Exit"), Op: gotoken.NEQ, Y: ident("nil")}, Body: &goast.BlockStmt{List: []goast.Stmt{step("MachineExit", "Exit", selector(name, "Exit"))}}},
		}
		if term.Close {
			resumed = append(resumed, assignStmt(machineLocalName(term.Bind.Name), g.unitValue()), assignMachinePC(int(term.Next)), &goast.BranchStmt{Tok: gotoken.CONTINUE})
			return stmts, [][]goast.Stmt{resumed}
		}
		if match := machineir.AdvanceMatch(worker, term); match != nil {
			branches := make([][]goast.Stmt, 3)
			for i, ctor := range term.Result.Ctors {
				for _, c := range match.Cases {
					if c.Ctor != ctor {
						continue
					}
					if i < 2 && len(c.Binds) != 0 && c.Binds[0].Name != "" {
						branches[i] = append(branches[i], assignStmt(machineLocalName(c.Binds[0].Name), &goast.TypeAssertExpr{X: selector(name, "Value"), Type: g.goType(c.Binds[0].Ty)}))
					}
					branches[i] = append(branches[i], assignMachinePC(int(c.Next)), &goast.BranchStmt{Tok: gotoken.CONTINUE})
				}
			}
			resumed = append(resumed, ifStmt(selector(name, "Present"), branches[0], []goast.Stmt{ifStmt(selector(name, "Finished"), branches[1], branches[2])}))
			return stmts, [][]goast.Stmt{resumed}
		}
		assign := func(index int) []goast.Stmt {
			var args []goast.Expr
			if index < 2 {
				args = append(args, &goast.TypeAssertExpr{X: selector(name, "Value"), Type: g.goType(resultTy.Args[index])})
			}
			return []goast.Stmt{assignStmt(machineLocalName(term.Bind.Name), g.ctorValue(term.Result.Ctors[index], resultTy.Args, args...))}
		}
		resumed = append(resumed, ifStmt(selector(name, "Present"), assign(0), []goast.Stmt{ifStmt(selector(name, "Finished"), assign(1), assign(2))}), assignMachinePC(int(term.Next)), &goast.BranchStmt{Tok: gotoken.CONTINUE})
		return stmts, [][]goast.Stmt{resumed}
	case *machineir.Call:
		args := g.typeDescriptorArgs(term.TyArgs)
		for _, ev := range term.EvidenceArgs {
			stack := g.evidence[ev.Unique]
			if len(stack) == 0 {
				panic("codegen: missing lexical machine evidence")
			}
			// A generated frame takes each interpretation at the protocol its
			// own declaration pinned, which is not this argument's: a caller
			// holding a Direct activation still reaches a worker that declared
			// the capability abstractly through its Machine record. A function
			// value's Machine member takes them all at Machine, because that
			// ABI follows the arrow type and is shared with code that was
			// never lowered.
			want := types.Machine
			if callee := g.machineWorkers[term.Callee]; term.Callee != "" && callee != nil {
				for _, param := range callee.EffectParams {
					if param.Unique == ev.Unique {
						want = machineir.Mode(param.Control)
						break
					}
				}
			}
			args = append(args, g.evidenceArg(ev, stack[len(stack)-1], g.currentEvidenceMode(ev.Unique), want))
		}
		if term.Callee != "" {
			args = append(args, g.machineRowArgs(g.machineWorkers[term.Callee], g.rowArgument(term.Row))...)
		} else if term.Row != nil {
			args = append(args, g.rowArgument(term.Row))
		}
		for i, arg := range term.Args {
			if slices.Contains(term.SynchronousArgs, i) {
				args = append(args, g.synchronousMachineCallback(arg))
			} else {
				args = append(args, g.machineExpr(arg))
			}
		}
		if g.reusesMachineFrame(worker, term) {
			fields := []string{}
			for _, param := range worker.TyParams {
				fields = append(fields, "T_"+g.tyParamNames[param.ID])
			}
			for _, ev := range worker.EffectParams {
				fields = append(fields, machineEvidenceFieldName(ev))
			}
			for i := range worker.Rows {
				fields = append(fields, fmt.Sprintf("R%d", i))
			}
			for _, param := range worker.Params {
				fields = append(fields, machineFieldName(param.Name))
			}
			values := []goast.Expr{&goast.KeyValueExpr{Key: ident("PC"), Value: intLit(int64(worker.Entry))}}
			for i, field := range fields {
				values = append(values, &goast.KeyValueExpr{Key: ident(field), Value: args[i]})
			}
			// The complete RHS is evaluated before overwriting f. Omitted
			// fields are zeroed, and the dispatcher retains its step boundary.
			reset := &goast.AssignStmt{Lhs: []goast.Expr{&goast.StarExpr{X: ident("f")}}, Tok: gotoken.ASSIGN,
				Rhs: []goast.Expr{&goast.CompositeLit{Type: indexExpr(ident(frameName), machineTypeParamIdents(worker.TyParams)), Elts: values}}}
			return []goast.Stmt{reset, step("MachineContinue", "", nil)}, nil
		}
		var child goast.Expr
		if term.Operation != nil {
			stack := g.evidence[term.Effect.Unique]
			if len(stack) == 0 {
				panic("codegen: machine operation has no lexical evidence")
			}
			opArgs := make([]goast.Expr, 0, len(term.Args))
			for i, arg := range term.Args {
				if i < len(term.Operation.ParamTypes) && g.isUnit(term.Operation.ParamTypes[i]) {
					continue
				}
				opArgs = append(opArgs, g.machineExpr(arg))
			}
			child = callExpr(&goast.SelectorExpr{X: stack[len(stack)-1], Sel: ident("Op_" + linkName(term.Operation.Name))}, opArgs...)
		} else if term.Callee == "" {
			child = callExpr(callbackMember(g.machineExpr(term.CalleeExpr), types.Machine), args...)
		} else {
			child = callExpr(indexExpr(g.machineStartRef(term.Callee), g.goTypes(term.TyArgs)), args...)
		}
		if term.Capture {
			result := term.Bind.Ty.(*types.TCon).Args[0]
			child = callExpr(selector("fangort", "FrameStart"), callExpr(indexExpr(selector("fangort", "CompletionMachine"), []goast.Expr{g.goType(result)}), callExpr(selector("fangort", "StartFrame"), child)))
		}
		stmts := append(save(), assignMachinePC(resumePC))
		tail := "false"
		if term.Tail {
			tail = "true"
		}
		stmts = append(stmts, returnStmt(callExpr(selector("fangort", "StartCall"), child, ident(tail))))
		return stmts, [][]goast.Stmt{resume(term.Bind, term.Next)}
	case *machineir.Handle:
		h := term.Node
		var statePrefix []goast.Stmt
		stateToken := ""
		var state *handlerState
		if term.State != nil {
			initialPrefix, initial := value(term.State.Initial)
			initial = callExpr(g.goType(term.State.Ty), initial)
			if term.Ordinary {
				// The clause closures outlive this Step, so the state cannot be
				// one of its locals. They share one escaping variable, and the
				// machine holds a pointer to it, so unwinding still pops this
				// handler's state and the return clause reads the final value.
				cell := fmt.Sprintf("machineHandlerState%d", g.tmp)
				g.tmp++
				statePrefix = append(initialPrefix,
					varDeclStmt(cell, g.goType(term.State.Ty), initial),
					exprStmt(callExpr(&goast.SelectorExpr{X: ident("m"), Sel: ident("PushState")},
						&goast.UnaryExpr{Op: gotoken.AND, X: ident(cell)})))
				state = &handlerState{
					read:  func() goast.Expr { return ident(cell) },
					write: func(next goast.Expr) goast.Stmt { return assignStmt(cell, next) },
				}
			} else {
				stateToken = fmt.Sprintf("machineHandlerState%d", g.tmp)
				g.tmp++
				statePrefix = append(initialPrefix, varDeclStmt(stateToken, ident("int"),
					callExpr(&goast.SelectorExpr{X: ident("m"), Sel: ident("PushState")}, initial)))
			}
		}
		evidenceName := fmt.Sprintf("machineHandlerEvidence%d", g.tmp)
		g.tmp++
		evidenceType := g.effectTypeMode(core.EffectInstance{Unique: h.Effect.Unique, Name: h.Effect.Name, Args: h.Effect.Args,
			Control: types.Control{Transport: types.Machine}}, types.Machine)
		var record goast.Expr
		if term.Ordinary {
			// This activation's own protocol is Direct, so it installs the
			// ordinary record of clause closures: performs in the body worker
			// are plain calls, and a closure bound to it has a member to call.
			evidenceType, record = g.handlerEvidence(h, types.Direct, state)
		}
		elts := make([]goast.Expr, 0, len(term.Clauses))
		targetName := ""
		if term.Abort {
			targetName = fmt.Sprintf("machineHandlerTarget%d", g.tmp)
			g.tmp++
			elts = append(elts, &goast.KeyValueExpr{Key: ident("Target"), Value: ident(targetName)})
		}
		for _, clause := range term.Clauses {
			if term.Abort {
				continue
			}
			var source *core.HandlerClause
			for i := range h.Clauses {
				if h.Clauses[i].Op == clause.Op {
					source = &h.Clauses[i]
					break
				}
			}
			if source == nil {
				panic("codegen: machine handler clause has no source metadata")
			}
			var params []paramSpec
			clauseWorker := g.machineWorkers[clause.Worker]
			ctorArgs := g.descriptorParamArgs(clauseWorker.TyParams)
			for _, ev := range clauseWorker.EffectParams {
				stack := g.evidence[ev.Unique]
				if len(stack) == 0 {
					panic("codegen: machine handler clause captures unavailable evidence")
				}
				ctorArgs = append(ctorArgs, g.evidenceArg(ev, stack[len(stack)-1], g.currentEvidenceMode(ev.Unique), machineir.Mode(ev.Control)))
			}
			ctorArgs = append(ctorArgs, g.machineRowArgs(clauseWorker, ident("nil"))...)
			if clauseWorker.StateToken {
				ctorArgs = append(ctorArgs, ident(stateToken))
			}
			for _, capture := range clause.Captures {
				ctorArgs = append(ctorArgs, ident(machineLocalName(capture.Name)))
			}
			if clause.StateName != "" {
				state := &goast.TypeAssertExpr{X: callExpr(&goast.SelectorExpr{X: ident("m"), Sel: ident("State")}, ident(stateToken)), Type: g.goType(clause.StateTy)}
				ctorArgs = append(ctorArgs, state)
			}
			for i, param := range source.Params {
				if g.isUnit(source.ParamTypes[i]) {
					ctorArgs = append(ctorArgs, g.unitValue())
					continue
				}
				name := machineLocalName(param)
				params = append(params, paramSpec{name: name, typ: g.goType(source.ParamTypes[i])})
				ctorArgs = append(ctorArgs, ident(name))
			}
			ctor := indexExpr(g.machineStartRef(clause.Worker), machineTypeParamIdents(clauseWorker.TyParams))
			fn := funcLitParams(params, selector("fangort", "MachineStart"), []goast.Stmt{returnStmt(callExpr(ctor, ctorArgs...))})
			elts = append(elts, &goast.KeyValueExpr{Key: ident("Op_" + linkName(clause.Op.Name)), Value: fn})
		}
		if record == nil {
			record = &goast.CompositeLit{Type: evidenceType, Elts: elts}
		}
		decl := varDeclStmt(evidenceName, evidenceType, record)
		bodyWorker := g.machineWorkers[term.BodyWorker]
		bodyArgs := g.descriptorParamArgs(bodyWorker.TyParams)
		for _, ev := range bodyWorker.EffectParams {
			if ev.Unique == h.Effect.Unique {
				bodyArgs = append(bodyArgs, ident(evidenceName))
				continue
			}
			stack := g.evidence[ev.Unique]
			if len(stack) == 0 {
				panic("codegen: machine handler body captures unavailable evidence")
			}
			bodyArgs = append(bodyArgs, g.evidenceArg(ev, stack[len(stack)-1], g.currentEvidenceMode(ev.Unique), machineir.Mode(ev.Control)))
		}
		bodyArgs = append(bodyArgs, g.machineRowArgs(bodyWorker, ident("nil"))...)
		for _, capture := range term.BodyCaptures {
			bodyArgs = append(bodyArgs, ident(machineLocalName(capture.Name)))
		}
		child := callExpr(indexExpr(g.machineConstructorRef(term.BodyWorker), machineTypeParamIdents(bodyWorker.TyParams)), bodyArgs...)
		stmts := append([]goast.Stmt{}, statePrefix...)
		if term.Abort {
			target := &goast.UnaryExpr{Op: gotoken.AND, X: &goast.CompositeLit{Type: selector("fangort", "ExitTarget"), Elts: []goast.Expr{
				&goast.KeyValueExpr{Key: ident("Marker"), Value: intLit(1)},
			}}}
			stmts = append(stmts, varDeclStmt(targetName, &goast.StarExpr{X: selector("fangort", "ExitTarget")}, target))
		}
		stmts = append(stmts, decl, assignBlank(ident(evidenceName)))
		if term.Abort {
			stmts = append(stmts, exprStmt(callExpr(&goast.SelectorExpr{X: ident("m"), Sel: ident("PushHandler")}, ident(targetName))))
		}
		stmts = append(stmts, save()...)
		stmts = append(stmts, assignMachinePC(resumePC), step("MachineCall", "Frame", child))
		resumed := resume(term.Bind, term.Next)
		if term.Abort {
			// An exit caught by this handler has already unwound the body and
			// restored this frame. Start the matching abort clause; otherwise
			// consume the normal body return and leave the handler scope.
			caught := fmt.Sprintf("machineCaughtExit%d", g.tmp)
			g.tmp++
			first := []goast.Stmt{varDeclStmt(caught, &goast.StarExpr{X: selector("fangort", "ExitRequest")}, callExpr(&goast.SelectorExpr{X: ident("m"), Sel: ident("TakeCaughtExit")}))}
			var cases []goast.Stmt
			for _, clause := range term.Clauses {
				var source *core.HandlerClause
				for i := range h.Clauses {
					if h.Clauses[i].Op == clause.Op {
						source = &h.Clauses[i]
						break
					}
				}
				if source == nil {
					panic("codegen: abort clause has no source metadata")
				}
				clauseWorker := g.machineWorkers[clause.Worker]
				args := g.descriptorParamArgs(clauseWorker.TyParams)
				for _, ev := range clauseWorker.EffectParams {
					stack := g.evidence[ev.Unique]
					if len(stack) == 0 {
						panic("codegen: abort clause captures unavailable evidence")
					}
					args = append(args, g.evidenceArg(ev, stack[len(stack)-1], g.currentEvidenceMode(ev.Unique), machineir.Mode(ev.Control)))
				}
				args = append(args, g.machineRowArgs(clauseWorker, ident("nil"))...)
				if clauseWorker.StateToken {
					args = append(args, callExpr(&goast.SelectorExpr{X: ident("m"), Sel: ident("TopStateToken")}))
				}
				for _, capture := range clause.Captures {
					args = append(args, ident(machineLocalName(capture.Name)))
				}
				if clause.StateName != "" {
					token := callExpr(&goast.SelectorExpr{X: ident("m"), Sel: ident("TopStateToken")})
					args = append(args, &goast.TypeAssertExpr{X: callExpr(&goast.SelectorExpr{X: ident("m"), Sel: ident("State")}, token), Type: g.goType(clause.StateTy)})
				}
				for i := range source.Params {
					payload := &goast.IndexExpr{X: selector(caught, "Payload"), Index: intLit(int64(i))}
					args = append(args, &goast.TypeAssertExpr{X: payload, Type: g.goType(source.ParamTypes[i])})
				}
				if source.SuppressedParam != "" {
					args = append(args, callExpr(selector("fangort", "SnapshotSuppressed"), ident(caught)))
				}
				ctor := indexExpr(g.machineConstructorRef(clause.Worker), machineTypeParamIdents(clauseWorker.TyParams))
				body := append(save(), assignMachinePC(resumePC+1), step("MachineCall", "Frame", callExpr(ctor, args...)))
				cases = append(cases, &goast.CaseClause{List: []goast.Expr{intLit(int64(clause.Op.Index))}, Body: body})
			}
			cases = append(cases, &goast.CaseClause{Body: []goast.Stmt{returnStmt(callExpr(selector("fangort", "InvalidMachineStep"), stringLit("unknown caught machine exit")))}})
			first = append(first, &goast.IfStmt{Cond: binExpr(gotoken.NEQ, ident(caught), ident("nil")), Body: &goast.BlockStmt{List: []goast.Stmt{&goast.SwitchStmt{Tag: selector(caught, "Operation"), Body: &goast.BlockStmt{List: cases}}}}})
			first = append(first, exprStmt(callExpr(&goast.SelectorExpr{X: ident("m"), Sel: ident("PopHandler")})))
			first = append(first, resumed...)
			aborted := resume(term.AbortBind, term.AbortNext)
			if term.State != nil {
				aborted = append(aborted[:1], append([]goast.Stmt{assignStmt(machineLocalName(term.StateResult.Name), g.machineFinalState(term))}, aborted[1:]...)...)
			}
			return stmts, [][]goast.Stmt{first, aborted}
		}
		if term.State != nil {
			resumed = append(resumed[:1], append([]goast.Stmt{assignStmt(machineLocalName(term.StateResult.Name), g.machineFinalState(term))}, resumed[1:]...)...)
		}
		return stmts, [][]goast.Stmt{resumed}
	case *machineir.PushCleanup:
		acquirePrefix, acquired := value(term.Acquire)
		oldControl, oldResult := g.control, g.resultType
		releaseControl := core.ExprControl(term.Release)
		if releaseControl.Transport == types.Exit {
			g.control, g.resultType = types.Exit, term.Release.Type()
		}
		release := g.expr(term.Release, 0)
		g.control, g.resultType = oldControl, oldResult
		var releaseBody []goast.Stmt
		if releaseControl.Transport == types.Exit {
			name := fmt.Sprintf("machineRelease%d", g.tmp)
			g.tmp++
			releaseBody = []goast.Stmt{
				varDeclStmt(name, g.outcomeType(term.Release.Type()), release),
				returnStmt(selector(name, "Exit")),
			}
		} else {
			releaseBody = []goast.Stmt{assignBlank(release), returnStmt(ident("nil"))}
		}
		cleanup := funcLit(&goast.StarExpr{X: selector("fangort", "ExitRequest")}, releaseBody)
		stmts := append(acquirePrefix,
			assignStmt(machineLocalName(term.Resource.Name), acquired),
			exprStmt(callExpr(&goast.SelectorExpr{X: ident("m"), Sel: ident("PushCleanup")}, cleanup)),
		)
		return append(stmts, continueStmt(term.Next)...), nil
	case *machineir.CursorOpen:
		owner := fmt.Sprintf("machineOwner%d", block.ID)
		row := fmt.Sprintf("machineForwarding%d", block.ID)
		captured := fmt.Sprintf("machineCoroutine%d", block.ID)
		stmts := []goast.Stmt{
			varDeclStmt(owner, &goast.StarExpr{X: selector("fangort", "YieldOwner")}, callExpr(selector("fangort", "NewYieldOwner"))),
			varDeclStmt(row, &goast.StarExpr{X: selector("fangort", "CursorEvidence")}, callExpr(selector("fangort", "NewCursorEvidence"), g.rowArgument(term.Row))),
			varDeclStmt(captured, &goast.StarExpr{X: selector("fangort", "MachineIterator")}, g.coroutineStart(term.Producer, ident(owner), ident(row))),
			assignStmt(machineLocalName(term.Cursor.Name), ident(captured)),
		}
		if types.CoroutineScopeType(term.Cursor.Ty) {
			stmts = stmts[1:]
		}
		cleanup := funcLit(&goast.StarExpr{X: selector("fangort", "ExitRequest")}, []goast.Stmt{returnStmt(callExpr(selector("fangort", "CloseMachineIterator"), ident(captured)))})
		stmts = append(stmts, exprStmt(callExpr(selector("m", "PushCleanup"), cleanup)))
		return append(stmts, continueStmt(term.Next)...), nil
	case *machineir.CursorClose, *machineir.PopCleanup:
		exitName := fmt.Sprintf("machineCleanupExit%d", block.ID)
		exitValue := callExpr(&goast.SelectorExpr{X: ident("m"), Sel: ident("PopCleanup")})
		failure := []goast.Stmt{step("MachineExit", "Exit", ident(exitName))}
		stmts := []goast.Stmt{
			varDeclStmt(exitName, &goast.StarExpr{X: selector("fangort", "ExitRequest")}, exitValue),
			&goast.IfStmt{Cond: binExpr(gotoken.NEQ, ident(exitName), ident("nil")), Body: &goast.BlockStmt{List: failure}},
		}
		var next machineir.BlockID
		switch close := term.(type) {
		case *machineir.CursorClose:
			next = close.Next
		case *machineir.PopCleanup:
			next = close.Next
		}
		return append(stmts, continueStmt(next)...), nil
	case *machineir.StateResume:
		resultName := fmt.Sprintf("machineResumeResult%d", g.tmp)
		g.tmp++
		stmts := []goast.Stmt{
			varDeclStmt(resultName, g.goType(term.Value.Type()), g.machineExpr(term.Value)),
			exprStmt(callExpr(&goast.SelectorExpr{X: ident("m"), Sel: ident("SetState")}, ident("machineStateToken"), callExpr(g.goType(term.NextState.Type()), g.machineExpr(term.NextState)))),
			assignStmt(machineLocalName(term.Bind.Name), ident(resultName)),
		}
		return append(stmts, continueStmt(term.Next)...), nil
	case *machineir.Return:
		prefix, normal := value(term.Value)
		return append(prefix, step("MachineReturn", "Value", callExpr(g.goType(term.Value.Type()), normal))), nil
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
		return []goast.Stmt{returnStmt(callExpr(selector("fangort", "InvalidMachineStep"), stringLit("exhaustive generated machine match failed")))}
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
	if term.ADT.Repr == types.ReprBytes {
		// One nullary constructor, so the match discriminates nothing — the
		// same rule bytesSwitch applies to a Direct match. Only Bytes' own
		// module can name the constructor, and it has nothing machine
		// lowering reaches, so this stands against that changing rather than
		// leaving a type switch on []byte to miscompile.
		if len(term.Cases) > 0 {
			return caseBody(term.Cases[0], nil)
		}
		return defaultBody()
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
	return callExpr(g.goType(e.Type()), g.machineExpr(e))
}

func (g *gen) machineExpr(e core.Expr) goast.Expr {
	return g.expr(e, 0)
}

func (g *gen) machineLambdaExpr(lam *core.Lambda) goast.Expr {
	closure := g.machineClosures[lam]
	if closure == nil {
		panic("codegen: missing Machine callback member")
	}
	worker := g.machineWorkers[closure.Worker]
	if worker == nil {
		panic("codegen: machine closure has no worker")
	}
	params := make([]paramSpec, 0, len(closure.CallEvidence)+1)
	args := g.descriptorParamArgs(worker.TyParams)
	for _, ev := range closure.CapturedEvidence {
		stack := g.evidence[ev.Unique]
		if len(stack) == 0 {
			panic("codegen: machine closure captures unavailable evidence")
		}
		args = append(args, g.evidenceArg(ev, stack[len(stack)-1], g.currentEvidenceMode(ev.Unique), machineir.Mode(ev.Control)))
	}
	for _, ev := range closure.CallEvidence {
		name := machineClosureEvidenceName(ev)
		params = append(params, paramSpec{name: name, typ: g.effectTypeMode(ev, types.Machine)})
		args = append(args, ident(name))
	}
	var callRow goast.Expr = ident("nil")
	if closure.CallRow != 0 {
		name := fmt.Sprintf("closureRow%d", g.tmp)
		g.tmp++
		params = append(params, paramSpec{name: name, typ: g.rowType()})
		callRow = ident(name)
	}
	args = append(args, g.machineRowArgs(worker, callRow)...)
	for _, capture := range closure.Captures {
		args = append(args, ident(machineLocalName(capture.Name)))
	}
	fn := lam.Ty.(*types.TFun)
	paramName := machineLocalName(lam.Param)
	params = append(params, paramSpec{name: paramName, typ: g.goType(fn.Arg)})
	args = append(args, ident(paramName))
	ctor := indexExpr(g.machineStartRef(closure.Worker), machineTypeParamIdents(worker.TyParams))
	return funcLitParams(params, selector("fangort", "MachineStart"), []goast.Stmt{returnStmt(callExpr(ctor, args...))})
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
func machineEvidenceName(ev core.EffectInstance) string {
	return "machineEvidence_" + linkName(ev.Name)
}
func machineEvidenceFieldName(ev core.EffectInstance) string { return "E_" + linkName(ev.Name) }
func machineClosureEvidenceName(ev core.EffectInstance) string {
	return "machineClosureEvidence_" + linkName(ev.Name)
}

func machineTypeParamIdents(params []*types.TVar) []goast.Expr {
	out := make([]goast.Expr, len(params))
	for i := range params {
		out[i] = ident(fmt.Sprintf("A%d", i))
	}
	return out
}

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
func machineFrameEvidenceField(ev core.EffectInstance) goast.Expr {
	return &goast.SelectorExpr{X: ident("f"), Sel: ident(machineEvidenceFieldName(ev))}
}
func assignMachinePC(pc int) goast.Stmt {
	return &goast.AssignStmt{Lhs: []goast.Expr{machinePC()}, Tok: gotoken.ASSIGN, Rhs: []goast.Expr{intLit(int64(pc))}}
}

// machineFinalState reads a handler's state back after its body has returned.
// A machine-lowered handler stores the value itself; one whose clauses stayed
// ordinary stores a pointer to the variable they share.
func (g *gen) machineFinalState(term *machineir.Handle) goast.Expr {
	popped := callExpr(&goast.SelectorExpr{X: ident("m"), Sel: ident("PopState")})
	if term.Ordinary {
		return &goast.StarExpr{X: &goast.TypeAssertExpr{X: popped, Type: &goast.StarExpr{X: g.goType(term.State.Ty)}}}
	}
	return &goast.TypeAssertExpr{X: popped, Type: g.goType(term.State.Ty)}
}
