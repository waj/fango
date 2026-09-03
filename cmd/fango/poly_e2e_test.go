package main

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/waj/fango/internal/build"
	"github.com/waj/fango/internal/codegen"
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/elaborate"
	"github.com/waj/fango/internal/eval"
	"github.com/waj/fango/internal/infer"
	"github.com/waj/fango/internal/lexer"
	"github.com/waj/fango/internal/parser"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/types"
)

// TestPolyDifferential drives polymorphic programs through both backends
// with the S5 staging flag on — the differential harness for generics until
// the flag is deleted and these become ordinary testdata/run programs.
func TestPolyDifferential(t *testing.T) {
	cases := []struct {
		name, src, expected string
	}{
		{"map_filter_foldr", `type List a = Nil | Cons a (List a)

map : (a -> b) -> List a -> List b
map f xs =
    case xs of
        Nil -> Nil
        Cons x rest -> Cons (f x) (map f rest)

filter : (a -> Bool) -> List a -> List a
filter p xs =
    case xs of
        Nil -> Nil
        Cons x rest -> if p x then Cons x (filter p rest) else filter p rest

foldr : (a -> b -> b) -> b -> List a -> b
foldr f z xs =
    case xs of
        Nil -> z
        Cons x rest -> f x (foldr f z rest)

upto : Int -> List Int
upto n = if n < 1 then Nil else Cons n (upto (n - 1))

main =
    xs = upto 10
    small = filter (\x -> x < 6) xs
    print (foldr (\x acc -> x + acc) 0 (map (\x -> x * x) small))
`, "55\n"},
		{"generic_show", `type Maybe a = Nothing | Just a
main = print (Just (Just 1))
`, "Just (Just 1)\n"},
		{"generic_show_nested", `type Maybe a = Nothing | Just a
type Pair a b = MkPair a b
main = print (MkPair (Just (0 - 1)) "hi")
`, "MkPair (Just -1) \"hi\"\n"},
		{"generic_eq", `type List a = Nil | Cons a (List a)
main = print (Cons 1 (Cons 2 Nil) == Cons 1 (Cons 2 Nil))
`, "True\n"},
		{"generic_eq_nested", `type Maybe a = Nothing | Just a
type List a = Nil | Cons a (List a)
main = print (Cons (Just 1) Nil /= Cons Nothing Nil)
`, "True\n"},
		{"number_generic", `double x = x + x
main = print (double 2.25)
`, "4.5\n"},
		{"number_generic_int", `double x = x + x
inc n = n + 1
main = print (double (inc 20))
`, "42\n"},
		{"nullary_value", `type Maybe a = Nothing | Just a
none = Nothing
pick m d =
    case m of
        Nothing -> d
        Just x -> x
main = print (pick none 7)
`, "7\n"},
		{"lifted_local", `main =
    id2 y = y
    a = id2 21
    b = id2 "sky"
    print b
`, "sky\n"},
		{"poly_partial", `type Maybe a = Nothing | Just a
wrap : a -> Maybe a
wrap x = Just x
apply f x = f x
main = print (apply wrap 3)
`, "Just 3\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := source.NewFile(c.name+".fango", []byte(c.src))
			toks, lexErrs := lexer.Lex(f)
			if len(lexErrs) > 0 {
				t.Fatalf("lex: %v", lexErrs)
			}
			m, parseErrs := parser.Parse(toks, f)
			if len(parseErrs) > 0 {
				t.Fatalf("parse: %v", parseErrs)
			}
			sup := &types.Supply{}
			b := types.NewBuiltins(sup)
			ck := infer.NewChecker(sup, b, infer.NewEnv())
			ck.AllowPoly = true
			infos, inferErrs := ck.Module(m)
			if len(inferErrs) > 0 {
				t.Fatalf("infer: %v", inferErrs)
			}
			prog, elabErrs := elaborate.Module(infos, ck)
			if len(elabErrs) > 0 {
				t.Fatalf("elaborate: %v", elabErrs)
			}
			if lintErrs := core.Lint(prog, b); len(lintErrs) > 0 {
				t.Fatalf("lint: %v\n%s", lintErrs, core.Dump(prog))
			}

			// Backend 1: the interpreter.
			env := eval.NewEnv()
			env.DefineProg(prog)
			var printed bytes.Buffer
			v, err := eval.Force(context.Background(), "main", env, &printed)
			if err != nil {
				t.Fatalf("eval: %v", err)
			}
			mainTy := mainType(prog)
			var evalOut string
			if con, isCon := mainTy.(*types.TCon); isCon && con.Unique == b.Unit.Unique {
				evalOut = printed.String()
			} else {
				evalOut = eval.ShowForPrint(v, mainTy, b) + "\n"
			}
			if evalOut != c.expected {
				t.Errorf("interpreter output %q, want %q", evalOut, c.expected)
			}

			if testing.Short() {
				t.Skip("compiled leg skipped in -short mode")
			}

			// Backend 2: emitted Go, built and run.
			gosrc, err := codegen.Emit(prog, b, true)
			if err != nil {
				t.Fatalf("emit: %v", err)
			}
			dir := t.TempDir()
			if _, err := build.Materialize(dir); err != nil {
				t.Fatal(err)
			}
			if _, err := build.WriteIfChanged(filepath.Join(dir, "main.go"), gosrc); err != nil {
				t.Fatal(err)
			}
			if err := build.GoBuild(dir); err != nil {
				t.Fatalf("go build failed — generated Go is a compiler bug:\n%v\n\n%s", err, gosrc)
			}
			out, err := exec.Command(build.BinaryPath(dir)).CombinedOutput()
			if err != nil {
				t.Fatalf("running compiled binary: %v\n%s", err, out)
			}
			if string(out) != c.expected {
				t.Errorf("compiled output %q, want %q\ngenerated:\n%s", out, c.expected, gosrc)
			}
			if string(out) != evalOut {
				t.Errorf("backends disagree: compiled %q vs interpreted %q", out, evalOut)
			}
		})
	}
}

func mainType(p *core.Prog) types.Type {
	for i := range p.Defs {
		if p.Defs[i].Name == "main" {
			return p.Defs[i].Type
		}
	}
	return p.Defs[len(p.Defs)-1].Type
}
