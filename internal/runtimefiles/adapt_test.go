package runtimefiles

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
)

type memoryTree map[string][]byte

func (t memoryTree) Source(path string) ([]byte, bool) {
	data, ok := t[path]
	return data, ok
}

type memoryStore struct {
	data   []byte
	stored int
}

func (s *memoryStore) Load() ([]byte, bool) { return s.data, s.data != nil }
func (s *memoryStore) Store(data []byte)    { s.data = data; s.stored++ }

// counted is a request whose adaptation is observable, so a test can tell what
// a hit saved from what it returned.
func counted(dest string, source []byte, calls *int) request {
	return request{
		dest:   dest,
		source: func() ([]byte, error) { return source, nil },
		adapt: func(_ string, source []byte) ([]byte, error) {
			*calls++
			return append([]byte("adapted "), source...), nil
		},
	}
}

func answer(t *testing.T, a *Adapter, r request) []byte {
	t.Helper()
	files, err := a.run([]request{r})
	if err != nil {
		t.Fatal(err)
	}
	return files[0].Data
}

// A hit is both halves: a record of the source as it is now, and a file that
// still hashes to what the record produced. Losing either adapts again.
func TestAdaptationHitNeedsTheSourceAndTheFile(t *testing.T) {
	const dest = "fangort/a.go"
	want := []byte("adapted one")
	tree, store := memoryTree{}, &memoryStore{}
	calls := 0
	build := func(source string) []byte {
		t.Helper()
		a := &Adapter{Tree: tree, Store: store}
		got := answer(t, a, counted(dest, []byte(source), &calls))
		a.Save()
		tree[dest] = got
		return got
	}
	expect := func(step string, wantCalls int, got, wantData []byte) {
		t.Helper()
		if calls != wantCalls || !bytes.Equal(got, wantData) {
			t.Fatalf("%s: %d adaptations, %q", step, calls, got)
		}
	}

	expect("cold", 1, build("one"), want)
	expect("unchanged", 1, build("one"), want)

	tree[dest] = []byte("edited by hand")
	expect("hand-edited file", 2, build("one"), want)

	delete(tree, dest)
	expect("missing file", 3, build("one"), want)

	expect("edited source", 4, build("two"), []byte("adapted two"))
	expect("settled", 4, build("two"), []byte("adapted two"))

	store.data = []byte("not json")
	expect("damaged record", 5, build("two"), []byte("adapted two"))
}

func TestAdapterWithoutCacheAdaptsEverything(t *testing.T) {
	calls := 0
	source := []byte("one")
	tree := memoryTree{"fangort/a.go": []byte("adapted one")}
	store := &memoryStore{}
	first := &Adapter{Store: store}
	answer(t, first, counted("fangort/a.go", source, &calls))
	first.Save()
	if answer(t, &Adapter{Tree: tree}, counted("fangort/a.go", source, &calls)); calls != 2 {
		t.Fatalf("no store: %d adaptations", calls)
	}
	var none *Adapter
	if answer(t, none, counted("fangort/a.go", source, &calls)); calls != 3 {
		t.Fatalf("nil adapter: %d adaptations", calls)
	}
	none.Save()
}

// A build keeps the records of the paths it did not ask about, which are a
// sibling program's, and writes nothing when it learned nothing.
func TestSaveKeepsSiblingRecordsAndSkipsNoChange(t *testing.T) {
	sibling := adaptation{Source: "s", Output: "o"}
	stored, err := json.Marshal(map[string]adaptation{"native/Other/native.go": sibling})
	if err != nil {
		t.Fatal(err)
	}
	store := &memoryStore{data: stored}
	tree := memoryTree{}
	calls := 0
	a := &Adapter{Tree: tree, Store: store}
	tree["fangort/a.go"] = answer(t, a, counted("fangort/a.go", []byte("one"), &calls))
	a.Save()
	if store.stored != 1 {
		t.Fatalf("stored %d times", store.stored)
	}
	var got map[string]adaptation
	if err := json.Unmarshal(store.data, &got); err != nil {
		t.Fatal(err)
	}
	if got["native/Other/native.go"] != sibling || got["fangort/a.go"].Source == "" {
		t.Fatalf("records: %#v", got)
	}
	again := &Adapter{Tree: tree, Store: store}
	answer(t, again, counted("fangort/a.go", []byte("one"), &calls))
	again.Save()
	if store.stored != 1 || calls != 1 {
		t.Fatalf("no-change build stored %d times, adapted %d", store.stored, calls)
	}
}

func TestRunReportsTheFirstFailureInOrder(t *testing.T) {
	first, second := errors.New("first"), errors.New("second")
	failing := func(err error) request {
		return request{source: func() ([]byte, error) { return nil, err }}
	}
	if _, err := (*Adapter)(nil).run([]request{failing(first), failing(second)}); !errors.Is(err, first) {
		t.Fatalf("got %v", err)
	}
}

// A source need not be formatted, and adapting one sorts its rewritten imports,
// which is why formatting cannot be skipped by looking at the source.
func TestAdaptingFormatsAndSorts(t *testing.T) {
	source := []byte("package native\n\nimport (\n\t\"fmt\"\n\t\"github.com/waj/fango/runtime/fangort\"\n)\n\nvar A =   fmt.Sprint(fangort.UnitValue)\n")
	got, err := adaptSidecar("A.native.go", source)
	if err != nil {
		t.Fatal(err)
	}
	want := "package native\n\nimport (\n\t\"fangobuild/fangort\"\n\t\"fmt\"\n)\n\nvar A = fmt.Sprint(fangort.UnitValue)\n"
	if string(got) != want {
		t.Fatalf("got:\n%s", got)
	}
}
