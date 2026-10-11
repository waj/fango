package nativedebug

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

type stop struct {
	frames      []Frame
	user        bool
	interrupted bool
	exited      bool
}
type fakeRPC struct {
	nextID       int
	active       map[int]Breakpoint
	stops        []stop
	frames       []Frame
	failCreateAt int
	calls        int
	onCreate     func()
	failClear    bool
	continued    int
}

func frame(name string, offset int64, line int) Frame {
	return Frame{Location: Location{PC: uint64(line), File: "Main.fango", Line: line, Function: &Function{Name: name}}, FrameOffset: offset}
}
func reply(out, v any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	return json.Unmarshal(b, out)
}
func (f *fakeRPC) Call(method string, in, out any) error {
	switch strings.TrimPrefix(method, "RPCServer.") {
	case "Stacktrace":
		return reply(out, map[string]any{"Locations": f.frames})
	case "ListBreakpoints":
		var bps []Breakpoint
		for _, bp := range f.active {
			bps = append(bps, bp)
		}
		return reply(out, map[string]any{"Breakpoints": bps})
	case "CreateBreakpoint":
		f.calls++
		if f.onCreate != nil {
			f.onCreate()
		}
		if f.calls == f.failCreateAt {
			return fmt.Errorf("injected creation failure")
		}
		f.nextID++
		bp := Breakpoint{ID: f.nextID, Addr: uint64(f.nextID)}
		f.active[bp.ID] = bp
		return reply(out, map[string]any{"Breakpoint": bp})
	case "ClearBreakpoint":
		if f.failClear {
			return fmt.Errorf("injected cleanup failure")
		}
		delete(f.active, in.(map[string]any)["Id"].(int))
		return nil
	case "Command":
		if in.(map[string]any)["name"] != "continue" {
			return nil
		}
		f.continued++
		if len(f.stops) == 0 {
			return fmt.Errorf("unexpected continue")
		}
		s := f.stops[0]
		f.stops = f.stops[1:]
		f.frames = s.frames
		state := State{Exited: s.exited}
		if !s.exited {
			bp := Breakpoint{ID: f.nextID}
			if s.user {
				bp.ID = 99
			}
			state.CurrentThread = &Thread{Location: s.frames[0].Location, Breakpoint: &bp, GoroutineID: 1}
			if s.interrupted {
				state.CurrentThread.Breakpoint = nil
			}
		}
		return reply(out, map[string]any{"State": state})
	}
	return fmt.Errorf("unexpected RPC %s", method)
}
func controller(stops ...stop) (*Controller, *fakeRPC) {
	start := frame("main.V_run", 10, 1)
	f := &fakeRPC{active: map[int]Breakpoint{}, frames: []Frame{start}, stops: stops}
	c := &Controller{RPC: f, Manifest: &Manifest{}, sites: []uint64{10, 20}, State: State{CurrentThread: &Thread{Location: start.Location, GoroutineID: 1}}}
	return c, f
}
func TestSourceStepModes(t *testing.T) {
	parent := frame("main.V_run", 10, 2)
	child := frame("main.V_leaf", 20, 3)
	caller := frame("main.V_main", 1, 4)
	for _, tc := range []struct {
		name      string
		stops     []stop
		remaining int
	}{
		{"step", []stop{{frames: []Frame{child, parent}}}, 0},
		{"next", []stop{{frames: []Frame{child, parent}}, {frames: []Frame{parent}}}, 0},
		{"finish", []stop{{frames: []Frame{child, parent}}, {frames: []Frame{parent}}, {frames: []Frame{caller}}}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, f := controller(tc.stops...)
			if err := c.Move(tc.name); err != nil {
				t.Fatal(err)
			}
			if len(f.stops) != tc.remaining || len(f.active) != 0 {
				t.Fatalf("remaining stops %d, leaked breakpoints %v", len(f.stops), f.active)
			}
		})
	}
}
func TestNextStopsOnBreakpointOrInterruptInCallee(t *testing.T) {
	for _, user := range []bool{false, true} {
		c, f := controller(stop{frames: []Frame{frame("leaf", 20, 3), frame("main.V_run", 10, 1)}, user: user, interrupted: !user})
		f.active[99] = Breakpoint{ID: 99, Addr: 99}
		if err := c.Move("next"); err != nil {
			t.Fatal(err)
		}
		if len(f.active) != 1 || f.active[99].ID != 99 {
			t.Fatalf("user breakpoint altered: %v", f.active)
		}
	}
}
func TestTemporaryBreakpointsCleanedOnFailure(t *testing.T) {
	c, f := controller()
	f.failCreateAt = 2
	if err := c.Move("step"); err == nil {
		t.Fatal("expected failure")
	}
	if len(f.active) != 0 {
		t.Fatalf("leaked breakpoints: %v", f.active)
	}
	c, f = controller() // command failure after successful installation
	if err := c.Move("step"); err == nil {
		t.Fatal("expected failure")
	}
	if len(f.active) != 0 {
		t.Fatalf("leaked breakpoints: %v", f.active)
	}
}
func TestTailIterationKeepsActivationAndRecursionDoesNot(t *testing.T) {
	start := frame("loop", 10, 7)
	if !shouldStop("next", start, []Frame{frame("loop", 10, 7)}) {
		t.Fatal("missed tail iteration")
	}
	if shouldStop("next", start, []Frame{frame("loop", 20, 7), start}) {
		t.Fatal("entered recursive activation on next")
	}
	if shouldStop("finish", start, []Frame{start}) {
		t.Fatal("finished a tail loop prematurely")
	}
	if !shouldStop("finish", start, []Frame{frame("caller", 1, 8)}) {
		t.Fatal("did not finish")
	}
}
func TestProcessExitCleansSteppingBreakpoints(t *testing.T) {
	c, f := controller(stop{exited: true})
	if err := c.Move("finish"); err != nil {
		t.Fatal(err)
	}
	if !c.State.Exited || len(f.active) != 0 {
		t.Fatalf("state %#v; breakpoints %v", c.State, f.active)
	}
}

func TestInterruptDuringBreakpointInstallationDoesNotResume(t *testing.T) {
	c, f := controller()
	f.onCreate = func() {
		if err := c.Interrupt(); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Move("step"); err != nil {
		t.Fatal(err)
	}
	if f.continued != 0 || len(f.active) != 0 {
		t.Fatalf("continued %d, remaining breakpoints %v", f.continued, f.active)
	}
}

func TestCleanupFailurePreventsFurtherExecution(t *testing.T) {
	c, f := controller(stop{frames: []Frame{frame("main.V_run", 10, 2)}})
	f.failClear = true
	if err := c.Move("step"); err == nil {
		t.Fatal("expected cleanup failure")
	}
	n := f.continued
	if err := c.Move("continue"); err == nil {
		t.Fatal("resumed with stale stepping breakpoints")
	}
	if f.continued != n {
		t.Fatal("continued after cleanup failure")
	}
}
