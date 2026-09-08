package nativehost

import (
	"bytes"
	"fmt"
	goast "go/ast"
	"go/format"
	goparser "go/parser"
	gotoken "go/token"
	"strconv"

	"github.com/waj/fango/internal/codegen"
)

func (e *Executor) workerSource() ([]byte, error) {
	imports := []goast.Spec{
		importSpec("fangort", "fangobuild/fangort"),
		importSpec("nativeworker", "fangobuild/nativeworker"),
	}
	functions := &goast.CompositeLit{Type: &goast.MapType{Key: goast.NewIdent("string"), Value: goast.NewIdent("any")}}
	var installs []goast.Stmt
	for i, source := range e.sources {
		alias := fmt.Sprintf("native%d", i)
		imports = append(imports, importSpec(alias, "fangobuild/native/"+codegen.NativeLinkName(source.Module)))
		installs = append(installs, &goast.AssignStmt{
			Lhs: []goast.Expr{&goast.SelectorExpr{X: goast.NewIdent(alias), Sel: goast.NewIdent("FangoHost")}},
			Tok: gotoken.ASSIGN,
			Rhs: []goast.Expr{goast.NewIdent("host")},
		})
		file, err := goparser.ParseFile(gotoken.NewFileSet(), source.Module+".native.go", source.Content, 0)
		if err != nil {
			return nil, err
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*goast.FuncDecl)
			if !ok || fn.Recv != nil || !goast.IsExported(fn.Name.Name) {
				continue
			}
			name := lowerFirst(fn.Name.Name)
			if source.Module != "" {
				name = source.Module + "." + name
			}
			functions.Elts = append(functions.Elts, &goast.KeyValueExpr{
				Key:   &goast.BasicLit{Kind: gotoken.STRING, Value: strconv.Quote(name)},
				Value: &goast.SelectorExpr{X: goast.NewIdent(alias), Sel: goast.NewIdent(fn.Name.Name)},
			})
		}
	}

	file := &goast.File{
		Name: goast.NewIdent("main"),
		Decls: []goast.Decl{
			&goast.GenDecl{Tok: gotoken.IMPORT, Specs: imports},
			&goast.GenDecl{Tok: gotoken.VAR, Specs: []goast.Spec{&goast.ValueSpec{
				Names:  []*goast.Ident{goast.NewIdent("functions")},
				Values: []goast.Expr{functions},
			}}},
			&goast.FuncDecl{
				Name: goast.NewIdent("main"),
				Type: &goast.FuncType{Params: &goast.FieldList{}},
				Body: &goast.BlockStmt{List: []goast.Stmt{&goast.ExprStmt{X: &goast.CallExpr{
					Fun: &goast.SelectorExpr{X: goast.NewIdent("nativeworker"), Sel: goast.NewIdent("Run")},
					Args: []goast.Expr{
						goast.NewIdent("functions"),
						&goast.FuncLit{
							Type: &goast.FuncType{Params: &goast.FieldList{List: []*goast.Field{{
								Names: []*goast.Ident{goast.NewIdent("host")},
								Type:  &goast.SelectorExpr{X: goast.NewIdent("fangort"), Sel: goast.NewIdent("NativeHost")},
							}}}},
							Body: &goast.BlockStmt{List: installs},
						},
					},
				}}}},
			},
		},
	}
	var source bytes.Buffer
	if err := format.Node(&source, gotoken.NewFileSet(), file); err != nil {
		return nil, fmt.Errorf("format native worker: %w", err)
	}
	return source.Bytes(), nil
}

func importSpec(alias, path string) *goast.ImportSpec {
	return &goast.ImportSpec{Name: goast.NewIdent(alias), Path: &goast.BasicLit{Kind: gotoken.STRING, Value: strconv.Quote(path)}}
}
