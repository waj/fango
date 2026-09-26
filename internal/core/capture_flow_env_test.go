package core

import (
	"fmt"
	"testing"
)

// A chained value environment must read exactly as the copies it replaces:
// an extension never sees a later write below it, and a write to an extension
// never reaches the layer it extends.
func TestFlowScopeExtensionBehavesAsCopy(t *testing.T) {
	v := func(ref int) flowValue { return flowValue{refs: []int{ref}} }
	root := newFlowScope()
	root.set("a", v(1))
	child := root.extend()
	child.set("b", v(2))
	root.set("a", v(3))
	root.set("c", v(4))
	if got := child.get("a"); !equalFlow(got, v(1)) {
		t.Fatalf("extension saw a later write below it: %v", got)
	}
	if _, ok := child.lookup("c"); ok {
		t.Fatal("extension saw a binding added below it")
	}
	if _, ok := root.lookup("b"); ok {
		t.Fatal("a write to an extension reached its parent")
	}
	child.set("a", v(5))
	if got := root.get("a"); !equalFlow(got, v(3)) {
		t.Fatalf("shadowing in an extension changed its parent: %v", got)
	}

	// Past the depth bound and the slice bound, the chain flattens and the
	// layer becomes a map; neither changes what it reads.
	deep := child
	want := map[string]flowValue{"a": v(5), "b": v(2)}
	for i := range 3 * maxFlowScopeDepth {
		deep = deep.extend()
		name := fmt.Sprint("x", i%(2*maxFlowScopeBinds))
		deep.set(name, v(10+i))
		want[name] = v(10 + i)
	}
	got := map[string]flowValue{}
	for name, value := range deep.all {
		if _, dup := got[name]; dup {
			t.Fatalf("%s yielded twice", name)
		}
		got[name] = value
	}
	if len(got) != len(want) {
		t.Fatalf("visible names %v, want %d of them", deep.names(), len(want))
	}
	for name, value := range want {
		if !equalFlow(got[name], value) || !equalFlow(deep.flatten().get(name), value) {
			t.Fatalf("%s = %v, want %v", name, got[name], value)
		}
	}
}
