package eval_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/waj/fango/internal/check"
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/debugger"
	"github.com/waj/fango/internal/eval"
	"github.com/waj/fango/internal/nativehost"
)

type inspector func(debugger.Event) debugger.Command

func (f inspector) DebugExchange(_ context.Context, e debugger.Event) debugger.Command { return f(e) }

const debugProgram = `module Main exposing (main)
effect Read
    read : Int -> Int

loop : Int -> Int -> Int
loop n acc =
    if n == 0 then acc else loop (n - 1) (acc + n)

choose : Int -> Maybe Int -> Int
choose offset value =
    case value of
        Just number -> number + offset
        Nothing -> 0

run : Int -> Int
run start =
    offset = start + 1
    callback = { value -> choose offset (Just value) }
    total = loop 3 (callback 10)
    handle read total on
        read amount -> resume (amount + 1)

main = run 4
`

func TestDebugScopesHandlersAndTailIterations(t *testing.T) {
	dir := t.TempDir()
	entry := filepath.Join(dir, "Main.fango")
	if err := os.WriteFile(entry, []byte(debugProgram), 0644); err != nil {
		t.Fatal(err)
	}
	result, diags, err := (&check.Session{Debug: true, DisableObjectCache: true}).Compile(entry)
	if err != nil || len(diags) > 0 {
		t.Fatalf("compile: %v %v", err, diags)
	}
	env := eval.NewEnv()
	env.DefineProg(result.Program)
	for _, worker := range []bool{false, true} {
		t.Run(map[bool]string{false: "local", true: "worker"}[worker], func(t *testing.T) {
			ioctx := eval.NewIOContext(strings.NewReader(""), io.Discard)
			if worker {
				ioctx.Natives, err = nativehost.Bundled()
				if err != nil {
					t.Fatal(err)
				}
			}
			config := &debugger.Config{}
			var hash string
			for _, def := range result.Program.Defs {
				if def.Name == "Main.loop" {
					hash = def.Body.DebugSite().Source.Hash
				}
			}
			if hash == "" {
				t.Fatal("missing loop source")
			}
			for id, line := range []int{7, 12, 18, 21} {
				config.Breakpoints = append(config.Breakpoints, debugger.Breakpoint{ID: id + 1, File: "Main.fango", Hash: hash, Line: line})
			}
			ioctx.Debug = config
			seen := map[string]bool{}
			iterations := map[string]bool{}
			var frame debugger.Frame
			loopID := uint64(0)
			ioctx.DebugHost = inspector(func(e debugger.Event) debugger.Command {
				if e.Kind == "stopped" {
					frame = e.Frames[0]
					if strings.HasSuffix(frame.Name, "loop") {
						if loopID != 0 && loopID != frame.ID {
							t.Errorf("tail iteration changed activation: %d -> %d", loopID, frame.ID)
						}
						loopID = frame.ID
					}
					return debugger.Command{Kind: "locals"}
				}
				for _, v := range e.Variables {
					if v.Value == "<unavailable>" {
						t.Errorf("unavailable %s in %s at %d", v.Name, frame.Name, frame.Site.Line)
					}
					seen[v.Name+"="+v.Value] = true
					if v.Name == "n" && strings.HasSuffix(frame.Name, "loop") {
						iterations[v.Value] = true
					}
				}
				return debugger.Command{Kind: "continue"}
			})
			value, err := eval.EvalIO(context.Background(), &core.VarRef{Name: "Main.main", Ty: result.Checker.B.Int}, env, ioctx)
			if err != nil || value != int64(22) {
				t.Fatalf("result: %v %v", value, err)
			}
			for _, want := range []string{"offset=5", "value=10", "number=10", "amount=21"} {
				if !seen[want] {
					t.Errorf("missing %s: %v", want, seen)
				}
			}
			if len(iterations) != 4 {
				t.Errorf("tail iteration values: %v", iterations)
			}
		})
	}
}

func TestDebugNextFinishAndSelectedCaller(t *testing.T) {
	dir := t.TempDir()
	entry := filepath.Join(dir, "Main.fango")
	if err := os.WriteFile(entry, []byte(debugProgram), 0644); err != nil {
		t.Fatal(err)
	}
	result, diags, err := (&check.Session{Debug: true, DisableObjectCache: true}).Compile(entry)
	if err != nil || len(diags) > 0 {
		t.Fatalf("compile: %v %v", err, diags)
	}
	for _, mode := range []string{"next", "finish", "selected-next"} {
		t.Run(mode, func(t *testing.T) {
			env := eval.NewEnv()
			env.DefineProg(result.Program)
			ioctx := eval.NewIOContext(strings.NewReader(""), io.Discard)
			ioctx.Debug = &debugger.Config{Step: true}
			phase := 0
			var target uint64
			ioctx.DebugHost = inspector(func(e debugger.Event) debugger.Command {
				if e.Kind != "stopped" || len(e.Frames) == 0 {
					t.Fatalf("unexpected event: %+v", e)
				}
				f := e.Frames[0]
				if phase == 0 {
					if mode == "next" && f.Name == "Main.run" {
						phase = 1
						target = f.ID
						return debugger.Command{Kind: "next"}
					}
					if mode != "next" && f.Name == "Main.choose" {
						phase = 1
						target = f.ID
						if mode == "finish" {
							return debugger.Command{Kind: "finish"}
						}
						for i, caller := range e.Frames {
							if caller.Name == "Main.run" {
								target = caller.ID
								return debugger.Command{Kind: "next", Frame: i}
							}
						}
						t.Fatal("caller frame missing")
					}
					return debugger.Command{Kind: "step"}
				}
				phase = 2
				if mode == "finish" {
					for _, frame := range e.Frames {
						if frame.ID == target {
							t.Error("finish stopped before activation returned")
						}
					}
				} else if f.ID != target {
					t.Errorf("next entered a callee: %+v", e.Frames)
				}
				return debugger.Command{Kind: "continue"}
			})
			value, err := eval.EvalIO(context.Background(), &core.VarRef{Name: "Main.main", Ty: result.Checker.B.Int}, env, ioctx)
			if err != nil || value != int64(22) || phase != 2 {
				t.Fatalf("%v %v phase %d", value, err, phase)
			}
		})
	}
}
