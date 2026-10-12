package nativedebug

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
)

// RPC is implemented by net/rpc.Client and by the deterministic protocol tests.
type RPC interface{ Call(string, any, any) error }

type Function struct {
	Name string `json:"name"`
}
type Location struct {
	PC       uint64    `json:"pc"`
	PCs      []uint64  `json:"pcs"`
	File     string    `json:"file"`
	Line     int       `json:"line"`
	Function *Function `json:"function"`
}
type Frame struct {
	Location
	FrameOffset int64
	Err         string
}
type Breakpoint struct {
	ID    int      `json:"id"`
	Addr  uint64   `json:"addr"`
	Addrs []uint64 `json:"addrs"`
	File  string   `json:"file"`
	Line  int      `json:"line"`
}
type Thread struct {
	Location
	GoroutineID int64       `json:"goroutineID"`
	Breakpoint  *Breakpoint `json:"breakPoint"`
}
type State struct {
	CurrentThread     *Thread `json:"currentThread"`
	SelectedGoroutine *struct {
		ID int64 `json:"id"`
	} `json:"currentGoroutine"`
	NextInProgress bool
	Threads        []*Thread
	Exited         bool   `json:"exited"`
	ExitStatus     int    `json:"exitStatus"`
	StopReason     string `json:"stopReason"`
}

type Controller struct {
	RPC         RPC
	Manifest    *Manifest
	State       State
	sites       []uint64
	users       map[int]Breakpoint
	broken      error
	interrupted atomic.Bool
}

func (c *Controller) call(method string, in, out any) error {
	return c.RPC.Call("RPCServer."+method, in, out)
}
func (c *Controller) command(name string) error {
	var out struct{ State State }
	if err := c.call("Command", map[string]any{"name": name}, &out); err != nil {
		// Some Delve backends return an exit error instead of the final state.
		if e := c.call("State", map[string]any{"NonBlocking": true}, &out); e == nil && out.State.Exited {
			c.State = out.State
			return nil
		}
		return err
	}
	c.State = out.State
	return nil
}
func (c *Controller) Goroutine() int64 {
	if c.State.SelectedGoroutine != nil {
		return c.State.SelectedGoroutine.ID
	}
	if c.State.CurrentThread != nil && c.State.CurrentThread.GoroutineID != 0 {
		return c.State.CurrentThread.GoroutineID
	}
	return -1
}
func (c *Controller) Frames() ([]Frame, error) {
	var out struct{ Locations []Frame }
	if err := c.call("Stacktrace", map[string]any{"Id": c.Goroutine(), "Depth": 1024}, &out); err != nil {
		return nil, err
	}
	for _, f := range out.Locations {
		if f.Err != "" {
			return nil, fmt.Errorf("native stack: %s", f.Err)
		}
	}
	if len(out.Locations) == 1024 {
		return nil, fmt.Errorf("native stack exceeds 1024 frames")
	}
	return out.Locations, nil
}
func (c *Controller) locations(file string, line int) ([]uint64, error) {
	var out struct{ Locations []Location }
	err := c.call("FindLocation", map[string]any{"Scope": map[string]any{"GoroutineID": -1}, "Loc": fmt.Sprintf("%s:%d", file, line)}, &out)
	if err != nil {
		return nil, err
	}
	var pcs []uint64
	for _, l := range out.Locations {
		if len(l.PCs) > 0 {
			pcs = append(pcs, l.PCs...)
		} else if l.PC != 0 {
			pcs = append(pcs, l.PC)
		}
	}
	if len(pcs) == 0 {
		return nil, fmt.Errorf("no executable Fango site at %s:%d", file, line)
	}
	return pcs, nil
}
func (c *Controller) resolveSites() error {
	if c.sites != nil {
		return nil
	}
	seen := map[uint64]bool{}
	var names []string
	for name := range c.Manifest.Lines {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		for _, line := range c.Manifest.Lines[name] {
			pcs, err := c.locations(name, line)
			if err != nil {
				return fmt.Errorf("resolve Fango site: %w", err)
			}
			for _, pc := range pcs {
				seen[pc] = true
			}
		}
	}
	for pc := range seen {
		c.sites = append(c.sites, pc)
	}
	sort.Slice(c.sites, func(i, j int) bool { return c.sites[i] < c.sites[j] })
	if len(c.sites) == 0 {
		return fmt.Errorf("binary has no executable Fango sites")
	}
	return nil
}
func (c *Controller) create(in map[string]any) (Breakpoint, error) {
	var out struct{ Breakpoint Breakpoint }
	err := c.call("CreateBreakpoint", map[string]any{"Breakpoint": in}, &out)
	return out.Breakpoint, err
}
func (c *Controller) clear(id int) error {
	return c.call("ClearBreakpoint", map[string]any{"Id": id}, &struct{}{})
}

// Start skips the Go runtime startup, then stops at the first Fango statement.
func (c *Controller) Start() (err error) {
	bp, err := c.create(map[string]any{"functionName": "main.main", "line": -1})
	if err != nil {
		return err
	}
	err = c.command("continue")
	err = errors.Join(err, c.clear(bp.ID))
	if err != nil {
		return err
	}
	if c.State.Exited || c.interrupted.Load() {
		return nil
	}
	return c.Move("step")
}

func sameFrame(a, b Frame) bool {
	return a.Function != nil && b.Function != nil && a.Function.Name == b.Function.Name && a.FrameOffset == b.FrameOffset
}
func shouldStop(mode string, start Frame, frames []Frame) bool {
	if mode == "step" || len(frames) == 0 {
		return true
	}
	for i, f := range frames {
		if sameFrame(start, f) {
			return mode == "next" && i == 0
		}
	}
	return true // the starting activation returned (or unwound)
}

// Move enters calls using source breakpoints, but steps over/out using Delve
// native operations so skipped callees run without per-statement traps.
func (c *Controller) Move(mode string) (err error) {
	c.interrupted.Store(false)
	if c.broken != nil {
		return c.broken
	}
	if c.State.Exited {
		return fmt.Errorf("process has exited")
	}
	if mode == "continue" {
		return c.command("continue")
	}
	if mode != "step" && mode != "next" && mode != "finish" {
		return fmt.Errorf("unknown stepping mode %q", mode)
	}
	if mode != "step" {
		return c.moveOver(mode)
	}
	return c.stepSource()
}

// stepSource seeks a single Fango stop, including callbacks across native code.
// It must not reset the interrupt flag when called as part of another move.
func (c *Controller) stepSource() (err error) {
	if err := c.resolveSites(); err != nil {
		return err
	}
	frames, err := c.Frames()
	if err != nil {
		return err
	}
	if len(frames) == 0 {
		return fmt.Errorf("no stack for the selected goroutine")
	}
	gid := c.Goroutine()
	var existing struct{ Breakpoints []Breakpoint }
	if err := c.call("ListBreakpoints", map[string]any{}, &existing); err != nil {
		return err
	}
	occupied := map[uint64]bool{}
	for _, bp := range existing.Breakpoints {
		occupied[bp.Addr] = true
		for _, pc := range bp.Addrs {
			occupied[pc] = true
		}
	}
	temporary := map[int]bool{}
	defer func() {
		for id := range temporary {
			if e := c.clear(id); e != nil && !c.State.Exited {
				c.broken = fmt.Errorf("cannot remove stepping breakpoint %d: %w", id, e)
				err = errors.Join(err, c.broken)
			}
		}
	}()
	for _, pc := range c.sites {
		if occupied[pc] {
			continue
		}
		args := map[string]any{"addr": pc}
		if gid > 0 {
			args["cond"] = fmt.Sprintf("runtime.curg.goid == %d", gid)
		}
		bp, e := c.create(args)
		if e != nil {
			return e
		}
		temporary[bp.ID] = true
	}
	if c.interrupted.Load() {
		return nil
	}
	// Every resulting stop is observable: either the next source site, an
	// explicit breakpoint, an interrupt, or process exit.
	return c.command("continue")
}

// moveOver leaves called functions uninstrumented. After returning into
// generated scaffolding it seeks the next Fango site, preserving the original
// activation until it actually returns (including recursive and tail calls).
func (c *Controller) moveOver(mode string) (err error) {
	frames, err := c.Frames()
	if err != nil {
		return err
	}
	if len(frames) == 0 {
		return fmt.Errorf("no stack for the selected goroutine")
	}
	start := frames[0]
	// A breakpoint, interrupt, or failed RPC can leave Delve's native stepping
	// breakpoints pending. Never let a later continue inherit that operation.
	defer func() {
		if !c.State.Exited {
			if e := c.call("CancelNext", map[string]any{}, &struct{}{}); e != nil {
				c.broken = fmt.Errorf("cannot cancel native stepping: %w", e)
				err = errors.Join(err, c.broken)
			} else {
				c.State.NextInProgress = false
			}
		}
	}()
	// Delve's line-based next skips a loop whose body occupies one source
	// line. Keep just the current PC armed for the current activation so a
	// Fango tail iteration remains observable without trapping in callees.
	if mode == "next" && start.PC != 0 && c.Goroutine() > 0 {
		if _, source := c.Manifest.Sources[start.File]; source {
			var existing struct{ Breakpoints []Breakpoint }
			if err := c.call("ListBreakpoints", map[string]any{}, &existing); err != nil {
				return err
			}
			occupied := false
			for _, bp := range existing.Breakpoints {
				if bp.Addr == start.PC {
					occupied = true
				}
				for _, pc := range bp.Addrs {
					if pc == start.PC {
						occupied = true
					}
				}
			}
			if !occupied {
				bp, createErr := c.create(map[string]any{"addr": start.PC, "cond": fmt.Sprintf("runtime.curg.goid == %d && runtime.frameoff == %d", c.Goroutine(), start.FrameOffset)})
				if createErr != nil {
					return createErr
				}
				defer func() {
					if e := c.clear(bp.ID); e != nil && !c.State.Exited {
						c.broken = fmt.Errorf("cannot remove loop breakpoint %d: %w", bp.ID, e)
						err = errors.Join(err, c.broken)
					}
				}()
			}
		}
	}
	command := "next"
	if mode == "finish" {
		command = "stepOut"
	}
	for {
		if c.interrupted.Load() {
			return nil
		}
		if err := c.command(command); err != nil {
			return err
		}
		if c.State.Exited || c.interrupted.Load() || c.State.NextInProgress {
			return nil
		}
		// User breakpoints (including runtime panic breakpoints) take priority.
		for _, thread := range append([]*Thread{c.State.CurrentThread}, c.State.Threads...) {
			if thread != nil && thread.Breakpoint != nil {
				return nil
			}
		}
		if reason := c.State.StopReason; reason != "" && reason != "next finished" {
			return nil
		}
		frames, err = c.Frames()
		if err != nil {
			return err
		}
		t := c.State.CurrentThread
		if t != nil {
			if _, ok := c.Manifest.Sources[t.File]; ok && shouldStop(mode, start, frames) {
				return nil
			}
		}
		if shouldStop("finish", start, frames) {
			return c.stepSource()
		}
		command = "stepOut"
		if mode == "next" && len(frames) > 0 && sameFrame(start, frames[0]) {
			command = "next"
		}
	}
}

func (c *Controller) Break(spec string) (Breakpoint, error) {
	i := strings.LastIndex(spec, ":")
	if i < 1 {
		return Breakpoint{}, fmt.Errorf("usage: break FILE:LINE")
	}
	line, err := strconv.Atoi(spec[i+1:])
	if err != nil || line < 1 {
		return Breakpoint{}, fmt.Errorf("invalid line number")
	}
	file := strings.ReplaceAll(spec[:i], "\\", "/")
	if _, ok := c.Manifest.Sources[file]; !ok {
		var matches []string
		for name := range c.Manifest.Sources {
			if strings.HasSuffix(name, "/"+file) || strings.HasSuffix(file, "/"+name) {
				matches = append(matches, name)
			}
		}
		if len(matches) != 1 {
			return Breakpoint{}, fmt.Errorf("unknown or ambiguous Fango source %q", file)
		}
		file = matches[0]
	}
	pcs, err := c.locations(file, line)
	if err != nil {
		return Breakpoint{}, err
	}
	bp, err := c.create(map[string]any{"addrs": pcs, "file": file, "line": line})
	if err == nil {
		if c.users == nil {
			c.users = map[int]Breakpoint{}
		}
		c.users[bp.ID] = bp
	}
	return bp, err
}
func (c *Controller) Delete(id int) error {
	if _, ok := c.users[id]; !ok {
		return fmt.Errorf("unknown breakpoint %d", id)
	}
	if err := c.clear(id); err != nil {
		return err
	}
	delete(c.users, id)
	return nil
}

// Interrupt halts a pending Continue and also prevents a stop requested during
// breakpoint installation from being lost before execution resumes.
func (c *Controller) Interrupt() error {
	c.interrupted.Store(true)
	return c.call("Command", map[string]any{"name": "halt"}, &struct{}{})
}
