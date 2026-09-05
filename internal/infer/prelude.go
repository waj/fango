package infer

import (
	"io/fs"

	fango "github.com/waj/fango"
	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/lexer"
	"github.com/waj/fango/internal/parser"
	"github.com/waj/fango/internal/source"
)

// InstallPrelude parses and checks the embedded declarations used by prompt
// sessions and focused compiler tests. Batch compilation reaches the same
// declarations through the module graph loader.
func (ck *Checker) InstallPrelude() []diag.Error {
	var merged ast.Module
	operators := map[string]string{}
	for _, name := range []string{"Basics", "IO"} {
		data, err := fs.ReadFile(fango.StdlibFS, "stdlib/"+name+".fango")
		if err != nil {
			return []diag.Error{{Title: "INVALID EMBEDDED PRELUDE", Body: err.Error()}}
		}
		file := source.NewFile("<stdlib>/"+name+".fango", data)
		toks, errs := lexer.Lex(file)
		if len(errs) > 0 {
			return errs
		}
		mod, errs := parser.Parse(toks, file)
		if len(errs) > 0 {
			return errs
		}
		for _, d := range mod.Decls {
			switch d := d.(type) {
			case *ast.ValueDecl:
				d.Name = name + "." + d.Name
			case *ast.InfixDecl:
				d.Target = name + "." + d.Target
				operators[d.Op] = d.Target
			case *ast.EffectDecl:
				d.Name = name + "." + d.Name
				for i := range d.Ops {
					d.Ops[i].Name = name + "." + d.Ops[i].Name
				}
			}
		}
		merged.Decls = append(merged.Decls, mod.Decls...)
	}

	ck.Operators = operators
	_, errs := ck.Module(&merged)
	if len(errs) > 0 {
		return errs
	}
	for _, name := range []string{"print", "readLine"} {
		canonical := "IO." + name
		if scheme, ok := ck.Env.Lookup(canonical); ok {
			ck.Env.Bind(name, scheme)
			ck.Operations[name] = ck.Operations[canonical]
		}
	}
	if ck.IO != nil {
		ck.Effects["IO"] = ck.IO
	}
	return nil
}
