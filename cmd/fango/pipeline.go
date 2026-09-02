package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/waj/fango/internal/codegen"
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/elaborate"
	"github.com/waj/fango/internal/infer"
	"github.com/waj/fango/internal/lexer"
	"github.com/waj/fango/internal/parser"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/types"
)

// compileFile runs source → tokens → AST → typed AST → Core. Diagnostics go
// to stderr; ok is false if any stage failed.
func compileFile(entry string, stderr io.Writer) (*core.Prog, *infer.Checker, bool) {
	content, err := os.ReadFile(entry)
	if err != nil {
		fmt.Fprintf(stderr, "fango: %v\n", err)
		return nil, nil, false
	}
	f := source.NewFile(filepath.Base(entry), content)

	toks, lexErrs := lexer.Lex(f)
	if report(stderr, lexErrs) {
		return nil, nil, false
	}
	m, parseErrs := parser.Parse(toks, f)
	if report(stderr, parseErrs) {
		return nil, nil, false
	}

	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	ck := infer.NewChecker(sup, b, infer.NewEnv())
	infos, inferErrs := ck.Module(m)
	if report(stderr, inferErrs) {
		return nil, nil, false
	}

	prog, elabErrs := elaborate.Module(infos, ck)
	if report(stderr, elabErrs) {
		return nil, nil, false
	}
	if lintErrs := core.Lint(prog); len(lintErrs) > 0 {
		fmt.Fprintf(stderr, "fango: internal compiler error: Core invariants violated:\n")
		for _, e := range lintErrs {
			fmt.Fprintf(stderr, "  %v\n", e)
		}
		return nil, nil, false
	}
	return prog, ck, true
}

func report(stderr io.Writer, errs []diag.Error) bool {
	if len(errs) == 0 {
		return false
	}
	diag.Render(stderr, errs)
	return true
}

func hasMain(p *core.Prog) bool {
	for _, d := range p.Defs {
		if d.Name == "main" {
			return true
		}
	}
	return false
}

// emitGo runs the front half of the pipeline and returns generated Go
// source. FANGO_INTERNAL_PRINT_MAIN=1 switches codegen into the
// differential harness's print-main mode.
func emitGo(entry string, stderr io.Writer) ([]byte, bool) {
	prog, ck, ok := compileFile(entry, stderr)
	if !ok {
		return nil, false
	}
	if !hasMain(prog) {
		fmt.Fprintf(stderr, "fango: %s has no `main` — a program needs `main = ...`\n", entry)
		return nil, false
	}
	printMain := os.Getenv("FANGO_INTERNAL_PRINT_MAIN") == "1"
	src, err := codegen.Emit(prog, ck.B, printMain)
	if err != nil {
		fmt.Fprintf(stderr, "fango: internal compiler error: %v\n", err)
		return nil, false
	}
	return src, true
}

// cmdCheck parses and typechecks only: quiet on success (exit 0),
// diagnostics on stderr (exit 1). The test harness's workhorse.
func cmdCheck(args []string, stderr io.Writer) int {
	if len(args) != 1 {
		usage(stderr)
		return 2
	}
	if _, _, ok := compileFile(args[0], stderr); !ok {
		return 1
	}
	return 0
}
