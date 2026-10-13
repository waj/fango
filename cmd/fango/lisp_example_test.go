package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/waj/fango/internal/core"
)

// Running a long example alone cannot distinguish a tail loop from a growing
// Go stack. Pin the evaluator's bounded-frame implementation as well.
func TestLispTailLoop(t *testing.T) {
	var diagnostics bytes.Buffer
	prog, _, _, _, _, ok := compileFileGraph(filepath.Join("..", "..", "examples", "lisp.fango"), &diagnostics)
	if !ok {
		t.Fatal(diagnostics.String())
	}
	for _, def := range prog.Defs {
		if def.Name == "LispEval.drive" {
			if _, ok := core.DetectTailLoop(&def); !ok {
				t.Fatal("Lisp evaluator no longer lowers to a tail loop")
			}
			return
		}
	}
	t.Fatal("Lisp evaluator worker not found")
}

func TestLispExample(t *testing.T) {
	t.Parallel()
	path := filepath.Join("..", "..", "examples", "lisp.fango")
	runDifferentialCase(t, path, cliRunner(path))
}

func TestLispPrograms(t *testing.T) {
	path := filepath.Join("..", "..", "examples", "lisp.fango")
	for _, tc := range []struct {
		name, source, want string
		status             int
	}{
		{"literals", `; Unicode and integer bounds
'(hello #t #f "hé\n\"\\" (1 -2 +3))
-9223372036854775808
9223372036854775807
`, "(hello #t #f \"hé\\n\\\"\\\\\" (1 -2 3))\n-9223372036854775808\n9223372036854775807\n", 0},
		{"arithmetic", `(list (+) (*) (- 4) (- 9 2 3) (quotient -7 3) (remainder -7 3))
(list (= 2 2) (< 1 2) (<= 2 2) (> 3 2) (>= 2 2))
(list (not #f) (not '()) (if 0 7 8) (if #f missing 9))`, "(0 1 -4 4 -2 -1)\n(#t #t #t #t #t)\n(#t #f 7 9)\n", 0},
		{"lists", `(list (cons 1 '(2 3)) (car '(a b)) (cdr '(a b)) (null? '()) (list? 1) (length '(a b)))
(list (equal? '(1 (a)) '(1 (a))) (equal? "a" "a") (equal? + +))`, "((1 2 3) a (b) #t #f 2)\n(#t #t #f)\n", 0},
		{"lexical closures and globals", `(define x 10)
(define (make x) (lambda (y) (+ x y)))
(define add (make 7))
(let ((x 100)) (add 3))
(let ((x 2) (y x)) (+ x y))
(define (global) x)
(define x 20)
(global)
((lambda (x) (+ x 1) (+ x 2)) 3)`, "x\nmake\nadd\n10\n12\nglobal\nx\n20\n5\n", 0},
		{"recursion", `(define (factorial n) (if (= n 0) 1 (* n (factorial (- n 1)))))
(factorial 10)`, "factorial\n3628800\n", 0},
		{"tail calls", `(define (sum n acc) (if (= n 0) acc (let ((next (- n 1))) (begin (+ 1 2) (sum next (+ acc n))))))
(sum 20000 0)
(define (even n) (if (= n 0) #t (odd (- n 1))))
(define (odd n) (if (= n 0) #f (even (- n 1))))
(even 20001)`, "sum\n200010000\neven\nodd\n#f\n", 0},
		{"top level begin", `(begin (define x 2) (begin (define y 3) (+ x y)))
(* x y)`, "5\n6\n", 0},
		{"argument evaluation order", `(+ missing (car '()))`, "program.lisp:1:4: unbound symbol: missing\n", 1},
		{"callee evaluation order", `(missing another)`, "program.lisp:1:2: unbound symbol: missing\n", 1},
		{"division by zero", `(quotient 1 0)`, "program.lisp:1:1: division by zero\n", 1},
		{"wrong builtin arity", `(car 1 2)`, "program.lisp:1:1: wrong number of arguments to car\n", 1},
		{"wrong builtin type", `(+ #t 1)`, "program.lisp:1:1: expected an integer\n", 1},
		{"empty list access", `(car '())`, "program.lisp:1:1: car expects a nonempty list\n", 1},
		{"improper cons", `(cons 1 2)`, "program.lisp:1:1: expected a proper list\n", 1},
		{"procedure arity", `((lambda (x) x))`, "program.lisp:1:1: wrong number of procedure arguments\n", 1},
		{"nonprocedure", `(1 2)`, "program.lisp:1:1: expected a procedure\n", 1},
		{"bad if", `(if #t 1)`, "program.lisp:1:1: if expects condition, then, and else expressions\n", 1},
		{"bad quote", `(quote a b)`, "program.lisp:1:1: quote expects one expression\n", 1},
		{"empty lambda body", `(lambda (x))`, "program.lisp:1:1: expected at least one body expression\n", 1},
		{"duplicate parameter", `(lambda (x x) x)`, "program.lisp:1:9: duplicate parameter: x\n", 1},
		{"duplicate binding", `(let ((x 1) (x 2)) x)`, "program.lisp:1:14: duplicate binding: x\n", 1},
		{"local definition", `((lambda () (define x 1)))`, "program.lisp:1:13: define is only allowed at top level\n", 1},
		{"dotted list", `'(a . b)`, "program.lisp:1:5: dotted lists are not supported\n", 1},
		{"bad escape", `"bad\q"`, "program.lisp:1:6: unsupported string escape\n", 1},
		{"integer overflow", `9223372036854775808`, "program.lisp:1:1: integer literal out of range\n", 1},
		{"missing close", "(list 1\n", "program.lisp:2:1: expected )\n", 1},
		{"unexpected close", ")", "program.lisp:1:1: expected end of input\n", 1},
		{"unicode position", "'é )", "program.lisp:1:4: expected end of input\n", 1},
		{"crlf position", "'é\r\n)", "program.lisp:2:1: expected end of input\n", 1},
		{"empty file", "; comment only", "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "program.lisp"), []byte(tc.source), 0o644); err != nil {
				t.Fatal(err)
			}
			runDifferentialCaseWith(t, path, cliRunner(path), fixtureInputs{
				args: []string{"program.lisp"}, files: dir, status: tc.status,
			}, tc.want)
		})
	}
}

func TestLispReplFailures(t *testing.T) {
	path := filepath.Join("..", "..", "examples", "lisp.fango")
	for _, tc := range []struct {
		name, input, want string
		status            int
	}{
		{"unfinished list", "(\n", "lisp> ...> <stdin>:2:1: expected )\n", 1},
		{"unfinished quote", "'", "lisp> ...> <stdin>:1:2: expected expression\n", 1},
		{"unfinished string", "\"hello", "lisp> ...> <stdin>:1:7: expected closing quote\n", 1},
		{"multiline string", "\"a\nb\"\n:quit\n", "lisp> ...> \"a\\nb\"\nlisp> ", 0},
		{"failed begin keeps definitions", "(begin (define x 2) missing (define y 3))\nx\ny\n:quit\n", "lisp> <stdin>:1:21: unbound symbol: missing\nlisp> 2\nlisp> <stdin>:1:1: unbound symbol: y\nlisp> ", 0},
		{"failed define keeps old value", "(define x 2)\n(define x missing)\nx\n:quit\n", "lisp> x\nlisp> <stdin>:1:11: unbound symbol: missing\nlisp> 2\nlisp> ", 0},
		{"syntax error evaluates nothing", "(define x 2) )\nx\n:quit\n", "lisp> <stdin>:1:14: expected end of input\nlisp> <stdin>:1:1: unbound symbol: x\nlisp> ", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runDifferentialCaseWith(t, path, cliRunner(path), fixtureInputs{stdin: tc.input, status: tc.status}, tc.want)
		})
	}
}

func TestLispCommandFailures(t *testing.T) {
	path := filepath.Join("..", "..", "examples", "lisp.fango")
	for _, tc := range []struct {
		name   string
		args   []string
		want   string
		status int
	}{
		{"usage", []string{"one", "two"}, "usage: lisp [file]\n", 2},
		{"missing file", []string{"missing.lisp"}, "missing.lisp: no such file or directory\n", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runDifferentialCaseWith(t, path, cliRunner(path), fixtureInputs{args: tc.args, files: t.TempDir(), status: tc.status}, tc.want)
		})
	}
}
