package check

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeStorageTypeAndLifetimeContracts(t *testing.T) {
	const cell = `module StorageFixture exposing (Box, box, read, write)
import Native
{-# resource #-}
type Box a = Box Native.Any
box : a ->{IO} Box a
box = native
read : Box a ->{IO} a
read = native
write : Box a -> a ->{IO} ()
write = native
`
	const sidecar = `package native
type cell struct { value any }
func Box(value any) any { return &cell{value} }
func Read(handle any) any { return handle.(*cell).value }
func Write(handle, value any) { handle.(*cell).value = value }
`
	for _, tc := range []struct{ name, body, want string }{
		{"different indices", `main() =
    x : StorageFixture.Box Int
    x = StorageFixture.box 1
    y = StorageFixture.box "text"
    print (StorageFixture.read x)
    print (StorageFixture.read y)
`, ""},
		{"wrong retrieval", `bad : StorageFixture.Box Int ->{IO} String
bad value = StorageFixture.read value
main() = ()
`, "TYPE MISMATCH"},
		{"wrong write", `main() =
    x : StorageFixture.Box Int
    x = StorageFixture.box 1
    StorageFixture.write x "wrong"
`, "TYPE MISMATCH"},
		{"borrowed initial payload", `freeze : (() -> Int) -> (() -> Int)
freeze action = action
bad() = handle StorageFixture.box (freeze (\_ -> State.get())) with current = 0 of
    State.get () -> resume current with current
    State.put next -> resume () with next
main() = ()
`, "ESCAPE"},
		{"borrowed stored payload", `save cell value = StorageFixture.write cell value
main() =
    target : StorageFixture.Box (() -> Int)
    target = StorageFixture.box (\_ -> 0)
    handle save target (\_ -> State.get()) with current = 0 of
        State.get () -> resume current with current
        State.put next -> resume () with next
    ()
`, "ESCAPE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, source := range map[string]string{"StorageFixture.fango": cell, "StorageFixture.native.go": sidecar, "Main.fango": "import StorageFixture\nimport State\n" + tc.body} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(source), 0600); err != nil {
					t.Fatal(err)
				}
			}
			cache := newMemoryObjectCache()
			for range 2 {
				_, diagnostics, err := (&Session{Cache: cache}).Compile(filepath.Join(dir, "Main.fango"))
				if err != nil {
					t.Fatal(err)
				}
				var messages []string
				for _, d := range diagnostics {
					messages = append(messages, d.Title+" "+d.Body)
				}
				got := strings.Join(messages, "\n")
				if tc.want == "" && got != "" || tc.want != "" && !strings.Contains(got, tc.want) {
					t.Fatalf("got %s, want %q", got, tc.want)
				}
			}
		})
	}
}

func TestCompletionCellContracts(t *testing.T) {
	for _, tc := range []struct{ name, source, want string }{
		{"one index", `main() = Coroutine.scope (\scope ->
    cell : Cell.Publisher Int
    cell = Cell.create scope
    first = Cell.publish cell 1
    second = Cell.publish cell "wrong"
    ())
`, "TYPE MISMATCH"},
		{"owner escape", `bad() = Coroutine.scope (\scope -> Cell.create scope)
main() = ()
`, "ESCAPE"},
		{"reader escape", `bad() = Coroutine.scope (\scope -> Cell.reader (Cell.create scope))
main() = ()
`, "ESCAPE"},
		{"private publisher", `main() = Cell.cellNew()
`, "PRIVATE OR UNKNOWN NAME"},
		{"borrowed payload", `main() = Coroutine.scope (\scope ->
    cell : Cell.Publisher (() -> Int)
    cell = Cell.create scope
    stored = handle Cell.publish cell (\_ -> State.get()) with current = 0 of
        State.get () -> resume current with current
        State.put next -> resume () with next
    ())
`, "ESCAPE"},
		{"row index", `write : Cell.Publisher (Completion.Completion Int e) -> Completion.Completion Int f ->{IO} Bool
write cell value = Cell.publish cell value
main() = ()
`, "EFFECT MISMATCH"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "Main.fango")
			if err := os.WriteFile(path, []byte("import Cell\nimport Coroutine\nimport State\nimport Completion\n"+tc.source), 0600); err != nil {
				t.Fatal(err)
			}
			_, diagnostics, err := (&Session{}).Compile(path)
			if err != nil {
				t.Fatal(err)
			}
			var messages []string
			for _, d := range diagnostics {
				messages = append(messages, d.Title+" "+d.Body)
			}
			got := strings.Join(messages, "\n")
			if !strings.Contains(got, tc.want) {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func TestIndexedNativeDeclarations(t *testing.T) {
	for _, tc := range []struct{ name, declaration, native, want string }{
		{"rebuild index", "cast : Box Int -> Box String\ncast (Box raw) = Box raw\n", "", "NATIVE HANDLE REPRESENTATION"},
		{"native cast", "cast : Box a -> Box b\ncast = native\n", "func Cast(value any) any { return value }\n", "NATIVE STORAGE"},
		{"untyped lookup", "find : Int -> Box a\nfind = native\n", "func Find(key int64) any { return nil }\n", "NATIVE STORAGE"},
		{"foreign index", "{-# resource #-}\ntype Other a = Other Native.Any\ncast : Box a -> Other b\ncast = native\n", "func Cast(value any) any { return value }\n", "NATIVE STORAGE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "Main.fango")
			source := "import Native\n{-# resource #-}\ntype Box a = Box Native.Any\nnew : a -> Box a\nnew = native\n" + tc.declaration + "main() = ()\n"
			if err := os.WriteFile(path, []byte(source), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "Main.native.go"), []byte("package native\nfunc New(value any) any { return value }\n"+tc.native), 0600); err != nil {
				t.Fatal(err)
			}
			_, ds, err := (&Session{}).Compile(path)
			if err != nil {
				t.Fatal(err)
			}
			var messages []string
			for _, d := range ds {
				messages = append(messages, d.Title+" "+d.Body)
			}
			if got := strings.Join(messages, "\n"); !strings.Contains(got, tc.want) {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}
