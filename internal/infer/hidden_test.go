package infer_test

import (
	"strings"
	"testing"
)

func TestHiddenConstructors(t *testing.T) {
	for _, program := range []string{
		"type Box a = Box state (state -> a)\nread (Box value observe) = observe value\nx = read (Box () { _ -> True })",
		"type Box a = Box state (state -> a)\nrepack (Box value observe) = Box value observe",
		"type Box a = Box state (state -> a)\nchoose flag = if flag then Box () { _ -> True } else Box False { b -> b }",
		"type Box a = { initial : state, read : state -> a }\nreadBox (Box { initial = value, read = observe }) = observe value",
		"type Box a = { initial : state, read : state -> a }\nreadBox : Box a -> a\nreadBox { initial = value, read = observe } = observe value",
		"type Box a = { initial : state, read : state -> a }\nmake : Box Bool\nmake = { initial = (), read = { _ -> True } }",
		"type Box a = { label : String, initial : state, read : state -> a }\nlabel : Box a -> String\nlabel box = box.label",
		"type Box a = { initial : state, read : state -> a }\nreadBox : Box a -> a\nreadBox box =\n    { initial = value, read = observe } = box\n    observe value",
	} {
		t.Run(program, func(t *testing.T) {
			_, _, errs := checkPoly(t, program)
			if len(errs) > 0 {
				t.Fatalf("unexpected errors: %v", errs)
			}
		})
	}
}

func TestHiddenTypesCannotEscapeOrMix(t *testing.T) {
	for _, tc := range []struct{ program, diagnostic string }{
		{"type Box = Box state\nleak (Box value) = value", "HIDDEN TYPE ESCAPES"},
		{"type Box = Box state\nleak box = case box of\n    Box value -> { _ -> value }", "HIDDEN TYPE ESCAPES"},
		{"type Box = { value : state }\nleak : Box -> a\nleak { value = value } = value", "TYPE MISMATCH"},
		{"type Box a = Box state (state -> a)\nmix (Box left observe) (Box right _) = observe right", "TYPE MISMATCH"},
		{"type Box a = Box state (state -> a)\nbad = Box True { x -> x ++ \"no\" }", "TYPE MISMATCH"},
		{"type Box a = { initial : state, read : state -> a }\nleak : Box a -> b\nleak box = box.initial", "HIDDEN RECORD FIELD"},
		{"type Box a = { initial : state, read : state -> a }\nbad : Box a -> Box a\nbad box = { box | initial = () }", "HIDDEN RECORD FIELD"},
		{"type Box = Box state\n(Box value) = Box ()", "HIDDEN TYPE ESCAPES"},
		{"type Box = { label : Bool, value : state }\nBox { label = flag, value = value } = Box { label = True, value = () }", "HIDDEN TYPE ESCAPES"},
		{"type Box = Box (() ->{e} ())", "HIDDEN EFFECT ROW"},
		{"type Box = Box (Later hidden)\ntype Later e = Later (() ->{e} ())", "HIDDEN EFFECT ROW"},
	} {
		t.Run(tc.program, func(t *testing.T) {
			_, _, errs := checkPoly(t, tc.program)
			for _, err := range errs {
				if strings.Contains(err.Error(), tc.diagnostic) {
					return
				}
			}
			t.Fatalf("wanted %s, got %v", tc.diagnostic, errs)
		})
	}
}

func TestHiddenVariablesAreConstructorLocal(t *testing.T) {
	ck, _, errs := checkPoly(t, "type Choice a = Left state (state -> a) | Right state (state -> a)")
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	left, right := ck.Ctors["Left"], ck.Ctors["Right"]
	if len(left.Hidden) != 1 || len(right.Hidden) != 1 || left.Hidden[0].ID == right.Hidden[0].ID {
		t.Fatal("constructor-local binders were missing or shared")
	}
}

func TestHiddenTypesDoNotAssumeClassInstances(t *testing.T) {
	for _, source := range []string{
		"type Box = Box state\nsame (Box value) = value == value",
		"type Box = Box state deriving (Eq)",
		"type Box = Box state\nshowBox (Box value) = show value",
	} {
		_, _, errs := checkPoly(t, source)
		if len(errs) == 0 {
			t.Errorf("accepted unconstrained hidden instance: %s", source)
		}
	}
}
