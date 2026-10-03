package infer_test

import "testing"

const putEffect = `effect Put a
    put : a -> ()

both : () ->{Put Bool, Put String} ()
both() =
    put True
    put "answer"
`

func TestAmbiguousEffectApplicationsAreRejected(t *testing.T) {
	for name, src := range map[string]string{
		"nested handlers": putEffect + `
main() = handle (handle both() of
    put text -> resume ()) of
    put flag -> resume ()
`,
		"polymorphic handler": putEffect + `
drop : (() ->{Put x | e} a) ->{e} a
drop action = handle action() of
    put _ -> resume ()

main() = drop { drop { both() } }
`,
	} {
		t.Run(name, func(t *testing.T) {
			_, _, errs := check(t, src)
			for _, err := range errs {
				if err.Error() == "AMBIGUOUS EFFECT" {
					return
				}
			}
			t.Fatalf("missing AMBIGUOUS EFFECT: %v", errs)
		})
	}
}

func TestEffectApplicationsChosenByType(t *testing.T) {
	for name, src := range map[string]string{
		"typed helpers": putEffect + `
dropStrings : (() ->{Put String | e} a) ->{e} a
dropStrings action = handle action() of
    put _ -> resume ()

dropBools : (() ->{Put Bool | e} a) ->{e} a
dropBools action = handle action() of
    put _ -> resume ()

main() = dropBools { dropStrings { both() } }
`,
		"payload pattern": putEffect + `
main() = handle (handle both() of
    put True -> resume ()
    put False -> resume ()) of
    put _ -> resume ()
`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, errs := check(t, src); len(errs) != 0 {
				t.Fatal(errs)
			}
		})
	}
}
