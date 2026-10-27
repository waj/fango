package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Entry assembly failures are compile-time failures at the offending
// attribute or at `main`.
func TestEntryAssemblyFailures(t *testing.T) {
	const runner = `
run : (() ->{Greeter | e} a) ->{e} a
run action =
    handle action() on
        greet name -> resume name
`
	for _, tc := range []struct{ name, source, title, message, site string }{
		{"no default", "import Runtime.Entry\neffect Greeter\n    greet : String -> String\nmain() = print (greet \"x\")\n",
			"COMPILE-TIME FAILURE", "declares no default handler", "main"},
		{"no defaults in the program", "{-# no-prelude #-}\neffect Greeter\n    greet : String -> ()\nmain() = greet \"x\"\n",
			"UNHANDLED EFFECT", "declares no default handler", "main"},
		{"two defaults", "import Runtime.Entry\n#[Runtime.Entry.DefaultHandler `run`]\n#[Runtime.Entry.DefaultHandler `run`]\neffect Greeter\n    greet : String -> String\n" + runner + "main() = print (greet \"x\")\n",
			"COMPILE-TIME FAILURE", "more than one default handler", "Runtime.Entry.DefaultHandler `run`"},
		{"wrongly typed runner", "import Runtime.Entry\n#[Runtime.Entry.DefaultHandler `wrong`]\neffect Greeter\n    greet : String -> String\nwrong : Int -> Int\nwrong n = n\nmain() = print (greet \"x\")\n",
			"COMPILE-TIME FAILURE", "must have the shape", "Runtime.Entry.DefaultHandler `wrong`"},
		{"not a reference", "import Runtime.Entry\n#[Runtime.Entry.DefaultHandler `{ x -> x }`]\neffect Greeter\n    greet : String -> String\nmain() = print (greet \"x\")\n",
			"COMPILE-TIME FAILURE", "quotes a runner by name", "Runtime.Entry.DefaultHandler `{ x -> x }`"},
		{"cycle", `import Runtime.Entry
#[Runtime.Entry.DefaultHandler ` + "`runA`" + `]
effect A
    a : () -> Int
#[Runtime.Entry.DefaultHandler ` + "`runB`" + `]
effect B
    b : () -> Int
runA : (() ->{A | e} x) ->{B | e} x
runA action =
    handle action() on
        a () -> resume (b ())
runB : (() ->{B | e} x) ->{A | e} x
runB action =
    handle action() on
        b () -> resume (a ())
main() = print (a ())
`, "COMPILE-TIME FAILURE", "depends on itself", "Runtime.Entry.DefaultHandler `runA`"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "Main.fango")
			if err := os.WriteFile(path, []byte(tc.source), 0o644); err != nil {
				t.Fatal(err)
			}
			var diagnostics bytes.Buffer
			if _, _, ok := compileFile(path, &diagnostics); ok {
				t.Fatal("compiled")
			}
			out := diagnostics.String()
			if !strings.Contains(out, "-- "+tc.title) || !strings.Contains(out, tc.message) || !strings.Contains(out, tc.site) {
				t.Fatalf("want %s %q at %q:\n%s", tc.title, tc.message, tc.site, out)
			}
		})
	}
}

// An annotated and an unannotated `main` with the same row select the same
// defaults, and a runner installed inside `main` wins over the default for
// the code it wraps.
func TestEntryAssemblyAnnotationsAndOverrides(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, main, want string }{
		{"inferred", "main() = print (Process.args())\n", "[\"a\"]\n"},
		{"annotated", "main : () ->{Console, Process.Environment} ()\nmain() = print (Process.args())\n", "[\"a\"]\n"},
		{"override", "import Dict\n\nmain() =\n    print (Process.runWith [\"fixed\"] Dict.empty Process.args)\n    print (Process.args())\n", "[\"fixed\"]\n[\"a\"]\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "Main.fango")
			if err := os.WriteFile(path, []byte("import Process\n"+tc.main), 0o644); err != nil {
				t.Fatal(err)
			}
			runDifferentialCaseWith(t, path, cliRunner(path), fixtureInputs{args: []string{"a"}}, tc.want)
		})
	}
}
