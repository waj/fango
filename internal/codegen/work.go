package codegen

import (
	"github.com/waj/fango/internal/core"
	goast "go/ast"
)

func (g *gen) workExpr(e *core.Work) goast.Expr {
	g.usesFangort = true
	args := make([]goast.Expr, len(e.Args))
	for i, arg := range e.Args {
		args[i] = g.expr(arg, 0)
	}
	if e.Kind == "registration" {
		return args[0]
	}
	if e.Kind == "registry-owner" {
		return callExpr(selector("fangort", "CoroutineWorkOwner"), args[0])
	}
	if e.Kind == "create" || e.Kind == "register" {
		cursor := g.coroutineStart(e.Args[1], callExpr(selector("fangort", "NewYieldOwner")), callExpr(selector("fangort", "NewCursorEvidence"), callExpr(selector("fangort", "CoroutineScopeEvidence"), args[0])))
		registered := callExpr(selector("fangort", "RegisterCoroutine"), args[0], cursor)
		if e.Kind == "register" {
			return callExpr(selector("fangort", "PackWork"), callExpr(selector("fangort", "CoroutineWorkOwner"), args[0]), registered)
		}
		return registered
	}
	if e.Kind == "facet" {
		return args[0]
	}
	name := map[string]string{"begin": "NewWorkOwner", "end": "CloseWorkOwner", "pack": "PackWork", "open": "OpenWork"}[e.Kind]
	if name == "" {
		panic("codegen: invalid work operation")
	}
	return callExpr(selector("fangort", name), args...)
}
