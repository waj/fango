package main

import (
	"fmt"
	"strings"
)

type shape struct{ Handlers, Members int }
type scenario struct {
	ID                        string
	Shape                     shape
	Kind, Position, Operation string
}

func shapes() []shape {
	var out []shape
	for _, n := range []int{1, 2, 3, 8, 16, 32, 64} {
		out = append(out, shape{n, 1})
	}
	for _, n := range []int{2, 8, 16, 32, 64} {
		out = append(out, shape{1, n})
	}
	return append(out, shape{16, 16}, shape{16, 64})
}

func scenarios(s shape) []scenario {
	var out []scenario
	add := func(kind, position, operation string) {
		out = append(out, scenario{fmt.Sprintf("h%d-m%d/%s/%s/%s", s.Handlers, s.Members, kind, position, operation), s, kind, position, operation})
	}
	for _, position := range []string{"inner", "outer"} {
		add("state", position, "first")
		add("open", position, "first")
		if s.Members > 1 {
			add("state", position, "last")
		}
	}
	// Rows are ordered by effect name and lookup starts at the newest binding,
	// so an open callback's cost depends on where its effect sorts. AHot takes
	// the deepest slot, ZHot the newest. Escaping callbacks keep invocation-time
	// lookup, which a callback the worker only invokes and forwards no longer needs.
	add("openlast", "inner", "first")
	add("escaping", "inner", "first")
	add("escapinglast", "inner", "first")
	add("stateless", "inner", "first")
	add("setup", "inner", "first")
	if s == (shape{1, 1}) {
		for _, kind := range []string{"pure", "go"} {
			add(kind, "none", "none")
		}
	}
	for _, kind := range []string{"lookup", "extend"} {
		add(kind, "inner", "first")
		if kind == "lookup" {
			add(kind, "outer", "first")
		}
	}
	return out
}

// checksummed kinds advance a state cell, producing a triangular result.
func checksummed(kind string) bool {
	switch kind {
	case "state", "open", "openlast", "escaping", "escapinglast":
		return true
	}
	return false
}

func functionName(c scenario) string { return "probe" + c.Kind + c.Position + c.Operation }

// All handlers are exercised before the steady-state window. Open callbacks
// additionally reference every effect on a runtime-dependent, untaken branch:
// their row really contains the surrounding handlers without adding hot calls.
func workload(s shape, literal int) string {
	var b strings.Builder
	fmt.Fprintln(&b, "begin : () ->{IO} ()\nbegin = native\nfinish : Int ->{IO} Int\nfinish = native\nmetric : Int ->{IO} Int\nmetric = native\nkeep : Int ->{IO} ()\nkeep = native")
	fmt.Fprintln(&b, "readMetric : Int ->{IO} Int\nreadMetric index = metric index")
	fmt.Fprintln(&b, "effect ZHot\n    zop : Int -> Int\neffect AHot")
	for i := 0; i < s.Members; i++ {
		fmt.Fprintf(&b, "    op%d : Int -> Int\n", i)
	}
	for i := 1; i < s.Handlers; i++ {
		fmt.Fprintf(&b, "effect Cold%d\n    cold%d : Int -> Int\n", i, i)
	}
	fmt.Fprintln(&b, "\nloop : Int -> Int ->{AHot} Int\nloop n x = if n <= 0 then x else loop (n - 1) (op0 x)")
	if s.Members > 1 {
		fmt.Fprintf(&b, "lastLoop : Int -> Int ->{AHot} Int\nlastLoop n x = if n <= 0 then x else lastLoop (n - 1) (op%d x)\n", s.Members-1)
	}
	fmt.Fprintln(&b, "openLoop : (Int ->{e} Int) -> Int -> Int ->{e} Int\nopenLoop action n x = if n <= 0 then x else openLoop action (n - 1) (action x)")
	fmt.Fprintln(&b, "openTimed : (Int ->{e} Int) -> Int ->{IO, e} Int\nopenTimed action n = begin(); value = openLoop action n 0; finish value")
	// Returning the callback gives it the general value ABI. The worker cannot
	// promise which row reaches it, so the body looks its evidence up.
	fmt.Fprintln(&b, "escape : (Int ->{e} Int) -> Int ->{e} Int\nescape action = { x -> action x }")
	fmt.Fprintln(&b, "pureLoop : Int -> Int -> Int\npureLoop n x = if n <= 0 then x else pureLoop (n - 1) (x + 1)")
	for _, c := range scenarios(s) {
		if c.Kind == "go" || c.Kind == "lookup" || c.Kind == "extend" {
			continue
		}
		name := functionName(c)
		if c.Kind == "setup" {
			arrow := "->"
			if s.Handlers > 1 {
				arrow = "->{IO}"
			}
			fmt.Fprintf(&b, "setupLoop : Int -> Int %s Int\nsetupLoop n total = if n <= 0 then total else value = installed n; setupLoop (n - 1) (total + value)\n", arrow)
		}
		hot := "op0"
		if c.Kind == "openlast" || c.Kind == "escapinglast" {
			hot = "zop"
		}
		fmt.Fprintf(&b, "%s : Int ->{IO} Int\n", name)
		if c.Kind == "pure" {
			fmt.Fprintf(&b, "%s n = begin(); value = pureLoop n 0; finish value\n", name)
			continue
		}
		if c.Kind == "setup" {
			fmt.Fprintf(&b, "%s n = begin(); value = setupLoop n 0; finish value\ninstalled n =\n", name)
		} else {
			fmt.Fprintf(&b, "%s n =\n", name)
		}
		order := []int{0}
		for i := 1; i < s.Handlers; i++ {
			order = append(order, i)
		}
		if c.Position == "inner" {
			order = append(order[1:], 0)
		}
		indent := "    "
		for range order {
			fmt.Fprintf(&b, "%shandle\n", indent)
			indent += "    "
		}
		for i := 1; i < s.Handlers; i++ {
			fmt.Fprintf(&b, "%skeep (cold%d 0)\n", indent, i)
		}
		if c.Kind == "setup" {
			fmt.Fprintf(&b, "%sop0 0\n", indent)
		} else {
			switch c.Kind {
			case "open", "openlast", "escaping", "escapinglast":
				fmt.Fprintf(&b, "%sopenTimed ", indent)
				closing := "}"
				if c.Kind == "escaping" || c.Kind == "escapinglast" {
					fmt.Fprint(&b, "(escape ")
					closing = "})"
				}
				fmt.Fprintln(&b, "{ x ->")
				fmt.Fprintf(&b, "%s    if x < 0 then\n", indent)
				for i := 1; i < s.Handlers; i++ {
					fmt.Fprintf(&b, "%s        keep (cold%d x)\n", indent, i)
				}
				fmt.Fprintf(&b, "%s        %s x\n%s    else %s x\n%s%s n\n", indent, hot, indent, hot, indent, closing)
			default:
				fmt.Fprintf(&b, "%sbegin()\n", indent)
				loop := "loop"
				if c.Operation == "last" {
					loop = "lastLoop"
				}
				fmt.Fprintf(&b, "%svalue = %s n 0\n", indent, loop)
				fmt.Fprintf(&b, "%sfinish value\n", indent)
			}
		}
		for i := len(order) - 1; i >= 0; i-- {
			indent = indent[:len(indent)-4]
			if order[i] == 0 {
				if c.Kind == "stateless" {
					fmt.Fprintf(&b, "%son\n", indent)
				} else {
					fmt.Fprintf(&b, "%swith current = 0 on\n", indent)
				}
				if hot == "zop" {
					fmt.Fprintf(&b, "%s    zop x -> resume (x + current + 1) with (current + 1)\n", indent)
				}
				for op := 0; op < s.Members && hot != "zop"; op++ {
					if c.Kind == "stateless" {
						fmt.Fprintf(&b, "%s    op%d x -> resume (x + 1)\n", indent, op)
					} else {
						fmt.Fprintf(&b, "%s    op%d x -> resume (x + current + 1) with (current + 1)\n", indent, op)
					}
				}
			} else {
				fmt.Fprintf(&b, "%son\n%s    cold%d x -> resume (x + n)\n", indent, indent, order[i])
			}
		}
	}
	fmt.Fprintf(&b, "main() = print (probeState_PLACEHOLDER %d)\n", literal)
	// Stable exported spelling is shared by the Go harness and build literal edit.
	return strings.ReplaceAll(b.String(), "probeState_PLACEHOLDER", "probestateinnerfirst")
}

const sidecar = `package native
import (
 "runtime"
 "time"
)
var started time.Time
var before runtime.MemStats
var elapsed, allocated, allocations int64
var sink int64
func Begin() { runtime.ReadMemStats(&before); started = time.Now() }
func Finish(value int64) int64 {
 elapsed = time.Since(started).Nanoseconds()
 var after runtime.MemStats; runtime.ReadMemStats(&after)
 allocated = int64(after.TotalAlloc-before.TotalAlloc); allocations = int64(after.Mallocs-before.Mallocs)
 sink = value; return value
}
func Metric(index int64) int64 { switch index { case 0: return elapsed; case 1: return allocated; default: return allocations } }
func Keep(value int64) { sink = value }
`
