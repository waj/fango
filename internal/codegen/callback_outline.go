package codegen

import (
	"fmt"
	goast "go/ast"
	gotoken "go/token"
	"maps"
	"slices"
	"sort"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

// The linker override retains comparison controls without adding a source-language
// or command-line option. Compiler build IDs isolate their cache namespaces.
var effectBodyStrategy = "selective"

type outlinedCallbackKey struct {
	lambda *core.Lambda
	mode   types.Transport
}

type outlinedCallback struct {
	name            string
	captures, types []int
}

func (g *gen) directLambdaMember(lam *core.Lambda, mode types.Transport) goast.Expr {
	if g.disableOptimizations || g.bodyStrategy == "" || g.bodyStrategy == "baseline" {
		return g.literalLambdaMember(lam, mode)
	}
	return g.outlinedLambda(lam, mode)
}

// Core binders are unique within an owner. Collecting definitions independently
// of uses therefore also covers recursive lets and pattern-tree scrutinees.
func (g *gen) callbackCaptures(lam *core.Lambda) []paramSpec {
	bound := map[string]bool{}
	used := map[string]types.Type{}
	var tree func(core.Tree)
	tree = func(t core.Tree) {
		switch t := t.(type) {
		case *core.SwitchCtor:
			if used[t.Scrut] == nil {
				used[t.Scrut] = g.caseVarTys[t.Scrut]
			}
			for _, c := range t.Cases {
				for _, name := range c.Binds {
					bound[name] = true
				}
				tree(c.Tree)
			}
			tree(t.Default)
		case *core.SwitchLit:
			if used[t.Scrut] == nil {
				used[t.Scrut] = g.caseVarTys[t.Scrut]
			}
			for _, c := range t.Cases {
				tree(c.Tree)
			}
			tree(t.Default)
		case *core.Guard:
			tree(t.Then)
			tree(t.Else)
		}
	}
	var visit func(core.Expr)
	visit = func(root core.Expr) {
		core.InspectPruned(root, func(e core.Expr) bool {
			switch e := e.(type) {
			case *core.App:
				if e.CalleeKind == core.Ctor {
					for _, arg := range e.Args {
						visit(arg)
					}
					return false
				}
			case *core.Lambda:
				bound[e.Param] = true
			case *core.Let:
				bound[e.Name] = true
			case *core.Bracket:
				bound[e.Resource] = true
			case *core.Case:
				bound[e.Bind] = true
				tree(e.Tree)
			case *core.Handle:
				if e.State != nil {
					bound[e.State.Name] = true
				}
				if e.Return != nil {
					bound[e.Return.Param] = true
				}
				for _, c := range e.Clauses {
					for _, name := range c.Params {
						bound[name] = true
					}
					bound[c.TraceParam], bound[c.SuppressedParam] = true, true
				}
			case *core.VarRef:
				if e.Local || g.defs[e.Name] == nil {
					used[e.Name] = e.Ty
				}
			}
			return true
		})
	}
	visit(lam)
	var out []paramSpec
	for _, name := range slices.Sorted(maps.Keys(used)) {
		if bound[name] || name == "_" || name == "" {
			continue
		}
		t := used[name]
		if t == nil {
			panic("codegen: missing outlined capture type: " + name)
		}
		if g.isUnit(t) {
			continue
		}
		out = append(out, paramSpec{mangleValue(name), g.goType(t)})
	}
	return out
}

func (g *gen) outlinedLambda(lam *core.Lambda, mode types.Transport) goast.Expr {
	freeEvidence := core.FreeEvidence(lam)
	key := outlinedCallbackKey{lam, mode}
	if g.outlinedCallbacks == nil {
		g.outlinedCallbacks = map[outlinedCallbackKey]*outlinedCallback{}
	}
	entry := g.outlinedCallbacks[key]
	fresh := entry == nil
	if fresh {
		name := fmt.Sprintf("callbackBodyShared%d", len(g.outlinedCallbacks))
		if g.currentWorker != "" {
			name += "__" + g.topValueName(g.currentWorker)
		}
		entry = &outlinedCallback{name: name}
		g.outlinedCallbacks[key] = entry
	}
	name := entry.name

	// Closure construction stays in the original lexical scope. The factory
	// receives explicit captures and emits the body once per transport.
	params := g.callbackCaptures(lam)
	var captures []goast.Expr
	var cells []goast.Stmt
	for i, p := range params {
		if g.initializingCells[p.name] {
			// Recursive bindings are assigned after closure construction.
			// Capture their cell and read it only when the closure runs.
			cell := "capturedCell_" + p.name
			params[i] = paramSpec{cell, &goast.StarExpr{X: p.typ}}
			captures = append(captures, &goast.UnaryExpr{Op: gotoken.AND, X: ident(p.name)})
			cells = append(cells, varDeclStmt(p.name, p.typ, &goast.StarExpr{X: ident(cell)}))
		} else {
			captures = append(captures, ident(p.name))
		}
	}
	var typeArgs []goast.Expr
	typeFields := &goast.FieldList{}
	ids := slices.Sorted(maps.Keys(g.tyParamNames))
	// Sort by positional names, not graph-local IDs, for module stability.
	sort.SliceStable(ids, func(i, j int) bool { return g.tyParamNames[ids[i]] < g.tyParamNames[ids[j]] })
	for _, id := range ids {
		n := g.tyParamNames[id]
		// A polymorphic operation's uniform request variables may be in
		// scope while its concrete argument is emitted. They have no caller
		// descriptor and are not runtime captures of that argument.
		if n == "any" && g.polyDescriptorNames[id] == "" {
			continue
		}
		if n != "any" {
			typeArgs = append(typeArgs, ident(n))
			typeFields.List = append(typeFields.List, &goast.Field{Names: []*goast.Ident{ident(n)}, Type: ident("any")})
		}
		descriptor := descriptorParamName(n)
		if local := g.polyDescriptorNames[id]; local != "" {
			descriptor = local
		}
		params = append(params, paramSpec{descriptor, g.descriptorType()})
		captures = append(captures, ident(descriptor))
	}
	if len(typeFields.List) == 0 {
		typeFields = nil
	}

	evidenceKeys := slices.Sorted(maps.Keys(freeEvidence))
	// Use nominal names and the same applied-evidence order as handler forks.
	// EffectKey's length-prefixed numbers preserve that order on cache rebasing.
	sort.SliceStable(evidenceKeys, func(i, j int) bool {
		a, b := freeEvidence[evidenceKeys[i]], freeEvidence[evidenceKeys[j]]
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return evidenceKeys[i] < evidenceKeys[j]
	})
	oldEvidence := g.outlinedEvidence
	g.outlinedEvidence = maps.Clone(oldEvidence)
	if g.outlinedEvidence == nil {
		g.outlinedEvidence = map[goast.Expr]goast.Expr{}
	}
	defer func() { g.outlinedEvidence = oldEvidence }()
	for i, key := range evidenceKeys {
		ev := freeEvidence[key]
		ev.Control = types.Control{Transport: types.Exit}
		stack := g.evidence[key]
		value := stack[len(stack)-1]
		canonical := oldEvidence[value]
		if canonical == nil {
			canonical = g.evidenceArg(ev, value, g.currentEvidenceMode(key), types.Exit)
		}
		captureName := fmt.Sprintf("capturedEvidence%d", i)
		params = append(params, paramSpec{captureName, g.effectTypeMode(ev, types.Exit)})
		captures = append(captures, canonical)
		canonicalLocal := ident(captureName)
		var local goast.Expr = canonicalLocal
		if mode == types.Direct {
			local = selector(captureName, "Direct")
		}
		g.evidence[key] = append(g.evidence[key], local)
		g.evidenceModes[key] = append(g.evidenceModes[key], mode)
		g.outlinedEvidence[local] = canonicalLocal
		defer func() {
			g.evidence[key] = g.evidence[key][:len(g.evidence[key])-1]
			g.evidenceModes[key] = g.evidenceModes[key][:len(g.evidenceModes[key])-1]
		}()
	}
	for i, id := range slices.Sorted(maps.Keys(core.FreeRows(lam))) {
		n := fmt.Sprintf("capturedRow%d", i)
		params = append(params, paramSpec{n, g.rowType()})
		captures = append(captures, g.rowValue(id))
		defer g.pushRow(id, ident(n))()
	}

	if fresh {
		oldABI, oldFlat, oldForwarders := g.abi, g.flatCallbacks, g.forwarders
		g.abi, g.flatCallbacks, g.forwarders = mode, nil, nil
		body := g.literalLambdaMember(lam, mode).(*goast.FuncLit)
		body.Body.List = append(cells, body.Body.List...)
		g.abi, g.flatCallbacks, g.forwarders = oldABI, oldFlat, oldForwarders
		if g.callbackFuncTypes == nil {
			g.callbackFuncTypes = map[*goast.FuncType]bool{}
		}
		g.callbackFuncTypes[body.Type] = true
		// Capture only the values and type parameters actually present
		// in emitted code. In particular, an unrelated enclosing generic
		// parameter must not specialize this factory or its closure.
		used := map[string]bool{}
		goast.Inspect(body, func(n goast.Node) bool {
			if id, ok := n.(*goast.Ident); ok {
				used[id.Name] = true
			}
			return true
		})
		var kept []paramSpec
		for i, p := range params {
			if used[p.name] {
				entry.captures = append(entry.captures, i)
				kept = append(kept, p)
				goast.Inspect(p.typ, func(n goast.Node) bool {
					if id, ok := n.(*goast.Ident); ok {
						used[id.Name] = true
					}
					return true
				})
			}
		}
		params = kept
		if typeFields != nil {
			var kept []*goast.Field
			for i, field := range typeFields.List {
				if used[field.Names[0].Name] {
					entry.types = append(entry.types, i)
					kept = append(kept, field)
				}
			}
			typeFields.List = kept
			if len(kept) == 0 {
				typeFields = nil
			}
		}
		factory := workerDecl(name, params, body.Type, []goast.Stmt{returnStmt(body)}).(*goast.FuncDecl)
		factory.Type.TypeParams = typeFields
		// Keep callback-record captures behind a real construction boundary.
		// Go 1.26 on darwin/arm64 can corrupt a captured cleanup callback when
		// it inlines this factory into a generic scope worker. Other captures
		// and invocation of the returned closure retain normal Go inlining.
		callables := map[string]bool{}
		for _, callable := range g.callableNames {
			callables[callable] = true
		}
		for _, p := range params {
			if callables[callbackAliasName(p.typ)] {
				factory.Doc = &goast.CommentGroup{List: []*goast.Comment{{Text: "//go:noinline"}}}
				break
			}
		}
		g.sharedCallbackDecls = append(g.sharedCallbackDecls, factory)
	}

	selectArgs := func(values []goast.Expr, indices []int) []goast.Expr {
		var out []goast.Expr
		for _, i := range indices {
			out = append(out, values[i])
		}
		return out
	}
	return callExpr(indexExpr(ident(name), selectArgs(typeArgs, entry.types)), selectArgs(captures, entry.captures)...)
}
