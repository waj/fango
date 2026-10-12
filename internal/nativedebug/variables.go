package nativedebug

import (
	"fmt"
	"io"
	"sort"

	"github.com/waj/fango/internal/debugger"
	"github.com/waj/fango/internal/debugui"
)

// sourceBindings is deliberately conservative when separate statement scopes
// share a line. The Go line table has no expression-column information. Never
// guess which shadowed binding is current merely because DWARF exposes both.
func (c *Controller) sourceBindings() (map[string]debugger.Binding, map[string]bool, error) {
	t := c.State.CurrentThread
	if t == nil {
		return nil, nil, fmt.Errorf("no current frame")
	}
	var scopes []debugger.NativeScope
	for _, scope := range c.Manifest.Scopes {
		if scope.File == t.File && scope.Line == t.Line {
			scopes = append(scopes, scope)
		}
	}
	if len(scopes) == 0 {
		return nil, nil, fmt.Errorf("no Fango binding metadata at this location; use raw args or raw locals")
	}
	bindings, ambiguous := map[string]debugger.Binding{}, map[string]bool{}
	counts := map[string]int{}
	for _, scope := range scopes {
		for _, b := range scope.Bindings {
			if old, ok := bindings[b.Name]; ok && old != b {
				ambiguous[b.Name] = true
			}
			bindings[b.Name] = b
			counts[b.Name]++
		}
	}
	for name := range bindings {
		if counts[name] != len(scopes) {
			ambiguous[name] = true
		}
	}
	return bindings, ambiguous, nil
}

func (c *Controller) inspect(command, name string, w io.Writer) error {
	if command != "print" && name != "" {
		return fmt.Errorf("%s takes no arguments", command)
	}
	if command == "print" && name == "" {
		return fmt.Errorf("usage: print NAME (use raw print EXPR for Go expressions)")
	}
	bindings, ambiguous, err := c.sourceBindings()
	if err != nil {
		return err
	}
	if command == "print" {
		if _, ok := bindings[name]; !ok {
			return fmt.Errorf("no visible Fango binding %q; use raw print EXPR for Go expressions", name)
		}
	}
	scope := map[string]any{"GoroutineID": c.Goroutine(), "Frame": 0}
	arguments := map[string]bool{}
	if command != "print" {
		// Classify names without loading synthetic dictionaries or adapters.
		var args struct{ Args []variable }
		namesOnly := map[string]any{"FollowPointers": false, "MaxVariableRecurse": 0, "MaxStringLen": 0, "MaxArrayValues": 0, "MaxStructFields": 0}
		if err := c.call("ListFunctionArgs", map[string]any{"Scope": scope, "Cfg": namesOnly}, &args); err != nil {
			return err
		}
		for _, v := range args.Args {
			arguments[v.Name] = true
		}
	}
	names := []string{}
	if command == "print" {
		names = append(names, name)
	} else {
		for name, b := range bindings {
			if command == "args" && arguments[b.Runtime] || command == "locals" && !arguments[b.Runtime] {
				names = append(names, name)
			}
		}
		sort.Strings(names)
	}
	for _, name := range names {
		b := bindings[name]
		if ambiguous[name] {
			debugui.Variable(w, debugger.Variable{Name: name, Type: b.Type, Value: "<unavailable: multiple lexical scopes share this source line>"})
			continue
		}
		debugui.Variable(w, debugger.Variable{Name: name, Type: b.Type, Value: c.preview(b.Runtime)})
	}
	return nil
}
