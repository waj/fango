package infer_test

import (
	"strings"
	"testing"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/elaborate"
)

func TestAnnotatedNestedHandlersRetainOuterEffects(t *testing.T) {
	for _, src := range []string{
		`effect Database
    lookup : Int -> Int

effect Http
    request : Int -> Int

interpret : (() ->{Database, Http | e} a) ->{e} a
interpret action =
    handle (handle action() of
        lookup value -> resume (request value)) of
        request value -> resume (value + 1)
`,
		`effect Ask
    ask : () -> Int

interpret : (() ->{Ask | e} a) ->{e} a
interpret action =
    handle (handle action() of
        ask () -> resume (ask() + 1)) of
        ask () -> resume 10
`,
	} {
		ck, infos, errs := check(t, src)
		if len(errs) != 0 {
			t.Fatal(errs)
		}
		p, elabErrs := elaborate.Module(infos, ck)
		if len(elabErrs) != 0 {
			t.Fatal(elabErrs)
		}
		if errs := core.Lint(p, ck.B); len(errs) != 0 {
			t.Fatal(errs)
		}
	}
}

func TestAnnotatedHandlersDoNotGainAmbientIOOrSelfHandling(t *testing.T) {
	for _, clause := range []string{
		"ask () ->\n            print 1\n            resume 1",
		"ask () -> resume (ask())",
	} {
		_, _, errs := check(t, `effect Ask
    ask : () -> Int

interpret : (() ->{Ask | e} a) ->{e} a
interpret action =
    handle action() of
        `+clause+"\n")
		var found bool
		for _, err := range errs {
			found = found || err.Error() == "EFFECT MISMATCH"
		}
		if !found {
			t.Fatalf("clause %q: %v, want EFFECT MISMATCH", clause, errs)
		}
	}
}

// An ordinary type parameter belongs to the caller. Adding a phantom scope
// index to a library API does not make that API a generative scope runner.
// This positive test is a counterexample to that proposed encoding, not a
// claim that Fango already has scoped local references.
func TestOrdinaryPhantomParameterDoesNotCreateFreshScope(t *testing.T) {
	ck, infos, errs := check(t, `type Token r = Token

withToken : (Token r -> a) -> a
withToken use = use Token

chosenByCaller : Token Int
chosenByCaller = withToken { token -> token }

savedCallback : () -> Token Int
savedCallback = withToken { token -> { _ -> token } }

type Box a = Box a
wrapped : Box (Token Int)
wrapped = withToken { token -> Box token }
`)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	p, elabErrs := elaborate.Module(infos, ck)
	if len(elabErrs) != 0 {
		t.Fatal(elabErrs)
	}
	if errs := core.Lint(p, ck.B); len(errs) != 0 {
		t.Fatal(errs)
	}
}

func TestScopedAPIUnimplementedTypeBoundaries(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"two instances of one effect", `effect Local s
    read : () -> s

both : () ->{Local Int, Local Bool} ()
both() = ()
`, "EFFECT MISMATCH"},
		{"effect row parameter", `type Job e = { run : () ->{e} () }

effect Scheduling e
    submit : Job e -> ()

bad : Job e ->{Scheduling e | e} ()
bad input = submit input
`, "KIND MISMATCH"},
		{"invalid effect argument in row-indexed type", `effect Local s
    read : () -> s

type Reader e = { read : () ->{e} Int }
bad : Reader (Local {IO}) -> ()
bad _ = ()
`, "KIND MISMATCH"},
		{"erased reader effect", `effect Reading
    read : () -> Int

type Reader e = { read : () ->{e} Int }

bad : (Reader e ->{e} a) -> a
bad use = handle use { read = read } of
    read () -> resume 42
`, "EFFECT MISMATCH"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, errs := check(t, tc.src)
			var messages []string
			for _, err := range errs {
				messages = append(messages, err.Error())
			}
			if got := strings.Join(messages, "\n"); !strings.Contains(got, tc.want) {
				t.Fatalf("%s: want %s", got, tc.want)
			}
		})
	}
}

func TestScopedDeclarationErrors(t *testing.T) {
	for _, src := range []string{
		"{-# scoped missing #-}\nrun : (Int ->{s} a) ->{e} a\nrun use = use 0\n",
		"{-# scoped s #-}\nrun : (Int ->{s} a) ->{s} a\nrun use = use 0\n",
		"{-# scoped s #-}\nrun : (Int ->{s} a) ->{e} a\nrun use = run use\n",
		"{-# scoped s #-}\nrun : (Int ->{s} a) ->{e} a\nrun use = other use\nother use = run use\n",
		"{-# scoped s #-}\nrun : Show a => (Int ->{s} a) ->{e} a\nrun use = use 0\n",
		"{-# scoped s #-}\nrun : (Int ->{s} a) ->{e} a\nrun _ = 0\n",
		"{-# scoped s #-}\nrun : (Int ->{s} a) ->{e} a\nrun = native\n",
		"{-# scoped s #-}\nrun : (Int ->{s} a) ->{e} a\nrun use = use 0\nalias = run\n",
	} {
		t.Run(src, func(t *testing.T) {
			_, _, errs := check(t, src)
			for _, err := range errs {
				if err.Error() == "SCOPED CALLBACK" {
					return
				}
			}
			t.Fatalf("missing scoped declaration diagnostic: %v", errs)
		})
	}
}
