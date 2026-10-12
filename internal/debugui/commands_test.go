package debugui

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/waj/fango/internal/debugger"
	"github.com/waj/fango/internal/debugsession"
)

func TestCommonCommandGrammar(t *testing.T) {
	for _, f := range []Features{Interpreter(), Native()} {
		for _, tc := range []struct {
			text string
			want Command
		}{
			{"step", Command{Kind: "step"}},
			{" :n ", Command{Kind: "next"}},
			{"c", Command{Kind: "continue"}},
			{":f", Command{Kind: "finish"}},
			{"h", Command{Kind: "help"}},
			{"exit", Command{Kind: "quit"}},
			{":b path with spaces/Main.fango:12", Command{Kind: "break", Arg: "path with spaces/Main.fango:12", File: "path with spaces/Main.fango", Line: 12}},
			{"delete 3", Command{Kind: "delete", Arg: "3", Index: 3}},
			{"print\tvalue", Command{Kind: "print", Arg: "value"}},
		} {
			got, err := Parse(tc.text, f)
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Errorf("%s %q: %+v, %v", f.Name, tc.text, got, err)
			}
		}
		for _, text := range []string{"next extra", "quit extra", "locals extra", "print", "print x + 1", "break Main.fango:0", "break Main.fango:bad", "delete -1", "delete 1 2", "reload", "x = 2"} {
			if _, err := Parse(text, f); err == nil {
				t.Errorf("%s accepted %q", f.Name, text)
			}
		}
	}
}

func TestCapabilitiesAndHelp(t *testing.T) {
	for _, tc := range []struct {
		f       Features
		yes, no []string
	}{
		{Interpreter(), []string{"frame 0", "expand 1 50", "stop"}, []string{"args", "raw print x"}},
		{Native(), []string{"args", "raw print x + 1", "raw locals", "raw stack", "frame 0", "expand 1"}, []string{"stop"}},
	} {
		var out bytes.Buffer
		Help(&out, tc.f)
		for _, text := range tc.yes {
			cmd, err := Parse(text, tc.f)
			if err != nil || !strings.Contains(out.String(), cmd.Kind) {
				t.Fatalf("%q: %v\n%s", text, err, &out)
			}
		}
		for _, text := range tc.no {
			if _, err := Parse(text, tc.f); err == nil || !strings.Contains(err.Error(), "not supported") {
				t.Fatalf("%q: %v", text, err)
			}
			if strings.Contains(out.String(), "  "+strings.Fields(text)[0]+" ") {
				t.Fatalf("unsupported help: %s", &out)
			}
		}
	}
	for _, text := range []string{"raw locals extra", "raw stack extra", "raw print", "raw next", "frame -1", "expand 0", "expand 1 -1", "expand 1 2 extra"} {
		f := Interpreter()
		if strings.HasPrefix(text, "raw") {
			f = Native()
		}
		if _, err := Parse(text, f); err == nil {
			t.Fatal(text)
		}
	}
	cmd, err := Parse("raw\tprint\tx + 1", Native())
	if err != nil || cmd.RawCommand != "print" || cmd.Arg != "x + 1" {
		t.Fatalf("%+v %v", cmd, err)
	}
}

func TestInputRepeatsOnlyValidCommands(t *testing.T) {
	var input Input
	if cmd, err := input.Parse("", Native()); err != nil || cmd.Kind != "" {
		t.Fatal(cmd, err)
	}
	input.Parse(":next", Native())
	if _, err := input.Parse("next extra", Native()); err == nil {
		t.Fatal("invalid command accepted")
	}
	if cmd, err := input.Parse("", Native()); err != nil || cmd.Kind != "next" {
		t.Fatal(cmd, err)
	}
}

func TestPresentation(t *testing.T) {
	var out bytes.Buffer
	Variable(&out, debugger.Variable{
		Name: "items", Type: "List Int", Handle: 7,
		Value: debugger.Value{Kind: debugger.ListValue, Fields: []debugger.ValueField{
			{Value: debugger.Value{Kind: debugger.NumberValue, Text: "1"}},
			{Value: debugger.Value{Kind: debugger.NumberValue, Text: "2"}},
		}},
	})
	Breakpoint(&out, 3, "Main.fango", 4, "verified")
	Frame(&out, 0, "main", Location{File: "Main.fango", Line: 4, Column: 2})
	want := "items : List Int = [1, 2] [handle 7]\nbreakpoint 3: Main.fango:4 (verified)\n#0 main at Main.fango:4:2\n"
	if out.String() != want {
		t.Fatalf("%s", &out)
	}
}

func TestRawStackPresentation(t *testing.T) {
	var out bytes.Buffer
	(&Terminal{Out: &out}).Present(debugsession.Event{Kind: "raw-frames", Frames: []debugsession.Frame{{Name: "main.V_main", Location: Location{File: "Main.fango", Line: 3}}}})
	if want := "native #0 main.V_main at Main.fango:3\n"; out.String() != want {
		t.Fatalf("got %q, want %q", out.String(), want)
	}
}
