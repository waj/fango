package main

import (
	"strings"
	"testing"
)

func TestHiddenValues(t *testing.T) {
	dir := t.TempDir()
	path := writeModuleFile(t, dir, "Main.fango", `
import Console

type Box a = Box state (state -> a)
read (Box value observe) = observe value
repack (Box value observe) = Box value observe
choose flag = if flag then Box () { _ -> 42 } else Box False { b -> if b then 0 else 17 }

type Record a = { label : String, state : hidden, observe : hidden -> a }
readRecord (Record { state = value, observe = f }) = f value
rename : Record a -> Record a
rename record = { record | label = "new" }

main() =
    Console.print (read (repack (choose True)))
    Console.print (read (choose False))
    original = Record { label = "old", state = "state", observe = { text -> text ++ "!" } }
    renamed = rename original
    Console.print original.label
    Console.print renamed.label
    Console.print (readRecord renamed)
`)
	runDifferentialCaseWith(t, path, cliRunner(path), fixtureInputs{}, "42\n17\nold\nnew\nstate!\n")
}

func TestHiddenEffectsAndRecursivePackages(t *testing.T) {
	path := writeModuleFile(t, t.TempDir(), "Main.fango", `
import Console

type Stream a e = { initial : state, step : state ->{e} Maybe (a, state) }
unfold : state -> (state ->{e} Maybe (a, state)) -> Stream a e
unfold initial step = { initial = initial, step = step }
foldStep : (a -> b ->{e} b) -> (state ->{e} Maybe (a, state)) -> b -> state ->{e} b
foldStep combine step accumulator state = case step state of
    Nothing -> accumulator
    Just (value, next) -> foldStep combine step (combine value accumulator) next
fold : (a -> b ->{e} b) -> b -> Stream a e ->{e} b
fold combine accumulator (Stream { initial = state, step = step }) = foldStep combine step accumulator state

effect Tick
    tick : Int -> Int

make : Stream Int Tick
make = unfold 0 { n -> if n < 3 then Just (tick n, n + 1) else Nothing }

type Box a = Box state (state -> a)
readBox (Box state action) = action state
nest : Int -> Box Int
nest depth = if depth == 0 then Box 7 { n -> n } else Box (nest (depth - 1)) readBox

main() =
    result = handle fold (+) 0 make on
        tick n -> resume (n + 1)
    Console.print result
    Console.print (readBox (nest 20))
    partial = Box False
    Console.print (readBox (partial { b -> if b then 0 else 9 }))
`)
	runDifferentialCaseWith(t, path, cliRunner(path), fixtureInputs{}, "6\n7\n9\n")
}

func TestHiddenTypesSurviveModuleCache(t *testing.T) {
	dir := t.TempDir()
	writeModuleFile(t, dir, "Package.fango", `module Package exposing (Box(..), make, read, repack)
type Box a = { state : hidden, observe : hidden -> a }
make : a -> Box a
make value = Box { state = value, observe = { x -> x } }
read : Box a -> a
read (Box { state = value, observe = f }) = f value
repack : Box a -> Box a
repack (Box { state = value, observe = f }) = Box { state = value, observe = f }
`)
	path := writeModuleFile(t, dir, "Main.fango", `import Package
main = Package.read (Package.repack (Package.make 13))
`)
	buildVerbose(t, path, "-v")
	_, warm := buildVerbose(t, path, "-v")
	if !strings.Contains(warm, "Checking  Package (from cache)") {
		t.Fatalf("module was not cached: %s", warm)
	}
	runDifferentialCaseWith(t, path, cliRunner(path), fixtureInputs{}, "13\n")
	writeModuleFile(t, dir, "Main.fango", `import Package
main = case Package.make "cached" of
    Package.Box { state = value, observe = f } -> f value
`)
	runDifferentialCaseWith(t, path, cliRunner(path), fixtureInputs{}, "cached\n")
}

func TestHiddenSchemaIsOpaque(t *testing.T) {
	path := writeModuleFile(t, t.TempDir(), "Main.fango", `import Meta
type Box a = Box state (state -> a)
main = $(Meta.lift (case Meta.info (typeOf (Box Int)) of
     Meta.Opaque -> True
     Meta.Visible _ -> False))
`)
	runDifferentialCaseWith(t, path, cliRunner(path), fixtureInputs{}, "True\n")
}

func TestHiddenConsumerUsesInvocationHandler(t *testing.T) {
	dir := t.TempDir()
	writeModuleFile(t, dir, "Flow.fango", `module Flow exposing (Box, make, map, read)
type Box a e = Box state (state ->{e} a)
make : state -> (state ->{e} a) -> Box a e
make state action = Box state action
map : (a ->{e} b) -> Box a e -> Box b e
map f (Box state action) = Box state { value -> f (action value) }
read : Box a e ->{e} a
read (Box state action) = action state
`)
	path := writeModuleFile(t, dir, "Main.fango", `import Console
import Fail
import Flow
type Box a e = Box state (state ->{e} a)
make : state -> (state ->{e} a) -> Box a e
make state action = Box state action
read : Box a e ->{e} a
read (Box state action) = action state
invoke : state -> (state ->{e} a) ->{e} a
invoke state action = read (make state action)
effect Tick
    tick : Int -> Int
main() =
    saved = make 7 tick
    first = handle read saved on
        tick n -> resume (n + 1)
    second = handle read saved on
        tick n -> resume (n + 2)
    Console.print (first, second)
    aborted = Fail.attempt { invoke 3 { n -> if n == 3 then Fail.fail "stopped" else n } }
    Console.print aborted
    nested = handle invoke 5 { n -> tick n } on
        tick n -> resume (n * 2)
    Console.print nested
    mapped = Flow.map { n -> tick (n + 1) } (Flow.make 4 tick)
    crossed = handle Flow.read mapped on
        tick n -> resume (n + 10)
    Console.print crossed
`)
	runDifferentialCaseWith(t, path, cliRunner(path), fixtureInputs{}, "(8, 9)\nErr \"stopped\"\n10\n25\n")
}

func TestHiddenStorageAcrossTypeOnlyModuleAndCache(t *testing.T) {
	dir := t.TempDir()
	writeModuleFile(t, dir, "PackageTypes.fango", `module PackageTypes exposing (Box(..))
type Box a = Box state (state -> a)
`)
	helper := `module PackageUse exposing (make, consume)
import PackageTypes exposing (Box(..))
make : Int -> Box Int
make n = Box n { x -> x }
consume : Int -> Box Int -> Int
consume n source =
    if n <= 0 then 0
    else case source of
        Box state observe -> observe state + consume (n - 1) source
`
	writeModuleFile(t, dir, "PackageUse.fango", helper)
	path := writeModuleFile(t, dir, "Main.fango", `import PackageUse
main = PackageUse.consume 1000 (PackageUse.make 7)
`)
	runDifferentialCaseWith(t, path, cliRunner(path), fixtureInputs{}, "7000\n")
	_, warm := buildVerbose(t, path, "-v")
	if !strings.Contains(warm, "Checking  PackageTypes (from cache)") {
		t.Fatal(warm)
	}
	files := emittedProject(t, path)
	found := false
	for _, file := range files {
		if strings.Contains(file.Path, "PackageUse") && strings.Contains(string(file.Data), "V_PackageUse_dot__storage_consume") {
			found = true
		}
	}
	if !found {
		t.Fatal("type-only module did not export its typed storage representation")
	}
	// The source signature stays the same, but the constructor witness and
	// generated factory ABI change. Cached callers must pick up that change.
	writeModuleFile(t, dir, "PackageUse.fango", strings.Replace(helper, "Box n { x -> x }", "Box (n, 1) { (x, increment) -> x + increment }", 1))
	runDifferentialCaseWith(t, path, cliRunner(path), fixtureInputs{}, "8000\n")
}
