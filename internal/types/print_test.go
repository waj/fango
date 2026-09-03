package types

import "testing"

func TestEffectRowPrinting(t *testing.T) {
	console := EffLabel{Unique: 10, Name: "Console"}
	e := &TVar{ID: 1, Kind: RowVar}
	cases := []struct {
		ty   Type
		want string
	}{
		{&TFun{Arg: &TCon{Name: "Int"}, Eff: Row{Tail: e}, Ret: &TCon{Name: "Int"}}, "Int -> Int"},
		{&TFun{Arg: &TCon{Name: "Int"}, Eff: Row{Labels: []EffLabel{console}, Tail: e}, Ret: &TCon{Name: "Int"}}, "Int ->{Console} Int"},
		{&TFun{Arg: &TCon{Name: "Int"}, Eff: Row{Labels: []EffLabel{console}, Tail: e}, Ret: &TFun{Arg: &TCon{Name: "Int"}, Eff: Row{Tail: e}, Ret: &TCon{Name: "Int"}}}, "Int ->{Console | e} Int -> Int"},
	}
	for _, c := range cases {
		if got := Show(c.ty); got != c.want {
			t.Errorf("Show = %q, want %q", got, c.want)
		}
	}
}
