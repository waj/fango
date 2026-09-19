package artifactstore

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestStoreRoundTripAndPathValidation(t *testing.T) {
	root := t.TempDir()
	s := New(root)
	s.Store("checked/abc.json", []byte("payload"))
	if got, ok := s.Load("checked/abc.json"); !ok || string(got) != "payload" {
		t.Fatalf("load = %q, %v", got, ok)
	}
	s.Store("../outside", []byte("bad"))
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "outside")); !os.IsNotExist(err) {
		t.Fatalf("unsafe path escaped store: %v", err)
	}
}

func TestConcurrentAtomicWriters(t *testing.T) {
	s := New(t.TempDir())
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Store("checked/object.json", []byte("complete"))
		}()
	}
	wg.Wait()
	if got, ok := s.Load("checked/object.json"); !ok || string(got) != "complete" {
		t.Fatalf("load after concurrent stores = %q, %v", got, ok)
	}
}
