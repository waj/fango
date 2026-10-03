package core

import (
	"strings"
	"testing"

	"github.com/waj/fango/internal/types"
)

func TestRegexLiteralLintAndRewrite(t *testing.T) {
	supply := &types.Supply{}
	b := types.NewBuiltins(supply)
	opaque := &types.TCon{Unique: supply.NextUnique(), Name: "Runtime.Native.Any"}
	regex := &types.TCon{Unique: supply.NextUnique(), Name: "Regex.Regex"}
	ctor := &types.CtorInfo{Name: "Regex.Regex", Fields: []types.Type{opaque}, Result: regex}
	literal := &RegexLit{Pattern: "abc", Ctor: ctor, Ty: regex}
	program := &Prog{ADTs: []*types.ADTInfo{{Con: regex, Ctors: []*types.CtorInfo{ctor}}}, Defs: []Def{{Name: "main", Type: regex, Body: literal}}}
	if got := lintText(program, b); got != "" {
		t.Fatal(got)
	}
	cloned := Rewrite(literal, func(ty types.Type) types.Type { return ty }, func(expr Expr) Expr { return expr }).(*RegexLit)
	if cloned == literal || cloned.Ctor != ctor || cloned.Pattern != literal.Pattern {
		t.Fatal("rewrite lost literal metadata")
	}
	literal.Pattern = "["
	if got := lintText(program, b); !strings.Contains(got, "invalid RegexLit") {
		t.Fatal(got)
	}
	literal.Pattern = "abc"
	literal.Ctor = nil
	if got := lintText(program, b); !strings.Contains(got, "invalid RegexLit type or constructor") {
		t.Fatal(got)
	}
	literal.Ctor = &types.CtorInfo{Name: "Regex.Regex"}
	if got := lintText(program, b); !strings.Contains(got, "invalid RegexLit type or constructor") {
		t.Fatal(got)
	}
	literal.Ctor = ctor
	ctor.Fields = []types.Type{b.Int}
	if got := lintText(program, b); !strings.Contains(got, "invalid RegexLit type or constructor") {
		t.Fatal(got)
	}
}
