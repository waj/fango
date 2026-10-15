package fangort

import (
	"runtime"
	"testing"
)

func TestLinkNameRoundTrip(t *testing.T) {
	for _, name := range []string{"Main.divide", "Basics.++", "List.map", "Main.x_plus_y", "Sub.Mod.go_dot", "Basics.<|", "Basics./=", "main"} {
		if got := unlink(LinkName(name)); got != name {
			t.Errorf("unlink(LinkName(%q)) = %q", name, got)
		}
	}
}

func TestDemangle(t *testing.T) {
	cases := []struct {
		goFunction string
		want       string
		ok         bool
	}{
		{"fangobuild/modules/Main.V_Main_dot_divide", "Main.divide", true},
		{"fangobuild/modules/Main.V_Main_dot_divide_exit", "Main.divide", true},
		{"fangobuild/modules/List.V_List_dot_foldl[...]", "List.foldl", true},
		{"fangobuild/modules/List.V_List_dot_map[go.shape.struct { F0 string }, go.shape.int64]", "List.map", true},
		{"fangobuild/modules/Main.V_Main_dot_compute.func1", "Main.compute (lambda)", true},
		{"fangobuild/modules/Main.V_Main_dot_compute.func1.2", "Main.compute (lambda)", true},
		{"fangobuild/modules/Sub/Mod.V_Sub_dot_Mod_dot_run", "Sub.Mod.run", true},
		{"fangobuild/modules/Basics.V_Basics_dot__plus__plus_", "Basics.++", true},
		{"fangobuild/modules/Main.V_Main_dot_x_plus_y", "Main.x_plus_y", true},
		{"main.V_main", "main", true},
		{"main.V_Main_dot_main", "Main.main", true},
		{"main.V_Main_dot_main.func1", "Main.main (lambda)", true},
		{"main.V__lift_Main_dot_outer_1_inner", "Main.outer.inner", true},
		{"main.V__lift7_inner", "inner", true},
		{"fangobuild/modules/Main.callbackBody3", "Main.<lambda>", true},
		{"fangobuild/modules/Main.callbackBodyShared3__V_Main_dot_compute[go.shape.int64]", "Main.compute (lambda)", true},
		{"main.callbackBodyShared4__V__lift_Main_dot_outer_1_inner.func2", "Main.outer.inner (lambda)", true},
		{"fangobuild/modules/Main.init.func1", "Main.<lambda>", true},
		{"main.init.func2.1", "<lambda>", true},
		{"fangobuild/modules/Main.init", "", false},
		{"main.main", "", false},
		{"runtime.gopanic", "", false},
		{"fangobuild/fangort.Capture", "", false},
		{"fangobuild/native/Console.Write", "", false},
	}
	for _, tc := range cases {
		symbol, ok := Demangle(tc.goFunction)
		if ok != tc.ok || (ok && symbol.String() != tc.want) {
			t.Errorf("Demangle(%q) = %q, %v; want %q, %v", tc.goFunction, symbol.String(), ok, tc.want, tc.ok)
		}
	}
}

func TestTransportBridgeIsNotASourceFrame(t *testing.T) {
	frame := runtime.Frame{Function: "main.V_Main_dot_compute", File: TransportBridgeFile, Line: 1}
	if _, ok := fangoFrame(frame); ok {
		t.Fatal("synthetic transport frame was exposed")
	}
	frame.File = "Main.fango"
	if _, ok := fangoFrame(frame); !ok {
		t.Fatal("source frame was hidden")
	}
}

func TestDemangleInstanceMethod(t *testing.T) {
	// The hex is what infer's registerInstance writes.
	link := LinkName("Basics._instance_" + hexOf("Basics.Show") + "_" + hexOf("List($0)") + "_context_" + hexOf("Basics.Show($0)") + "_show")
	symbol, ok := Demangle("fangobuild/modules/Basics.V_" + link)
	if !ok || symbol.String() != "Basics.Show (List a).show" {
		t.Fatalf("instance method demangled to %q, %v", symbol.String(), ok)
	}
	link = LinkName("Main._instance_" + hexOf("Basics.Eq") + "_" + hexOf("Main.Point(Main.Item,$0)") + "_generation_12_7_eq")
	symbol, ok = Demangle("main.V_" + link)
	if !ok || symbol.String() != "Main.Eq (Point Item a).eq" {
		t.Fatalf("generation instance method demangled to %q, %v", symbol.String(), ok)
	}
}

func hexOf(s string) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, 2*len(s))
	for i := 0; i < len(s); i++ {
		out = append(out, digits[s[i]>>4], digits[s[i]&0xf])
	}
	return string(out)
}

func TestFormatTrace(t *testing.T) {
	got := FormatTrace([]Frame{{Function: "Main.divide", File: "Main.fango", Line: 12}, {Function: "Main.main", File: "Main.fango", Line: 30}})
	want := "  at Main.divide (Main.fango:12)\n  at Main.main (Main.fango:30)\n"
	if got != want {
		t.Fatalf("FormatTrace = %q", got)
	}
	if FormatTrace(nil) != "" {
		t.Fatal("empty trace should render as nothing")
	}
}

func TestNativeFile(t *testing.T) {
	if got := nativeFile("/tmp/x/.fango/build/native/Trace/native.go"); got != "native/Trace/native.go" {
		t.Fatalf("nativeFile = %q", got)
	}
}

func TestDemangleExternalEntryModule(t *testing.T) {
	for _, module := range []string{"First", "Second", ""} {
		symbol, ok := DemangleWithEntryModule("main.init.func1", module)
		if !ok || symbol.Module != module || symbol.Function != "<lambda>" {
			t.Fatal(symbol, ok)
		}
	}
	symbol, ok := Demangle("main.init.func1")
	if !ok || symbol.Module != entryModule {
		t.Fatal("external decoding changed runtime entry module", symbol)
	}
}
