package runtimefiles

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"runtime"
	"sync"
)

// Tree is where adapted bytes already are, if an earlier build left them there.
type Tree interface {
	Source(path string) ([]byte, bool)
}

// Store keeps an adapter's records between builds. Losing them costs only the
// adaptation they would have saved.
type Store interface {
	Load() ([]byte, bool)
	Store(data []byte)
}

// Adapter produces the Go a generated module holds from the Go sources behind
// it, without parsing and formatting those sources again when nothing changed.
// Formatting cannot be skipped by inspection: it is what the output is defined
// by, and a source need not already be formatted. So an adaptation is memoized
// as what it is, a function from the bytes of a source to the bytes of a file,
// under the compiler that computed it.
//
// As with emission, the bytes themselves are not in the record. A hit is both
// halves — a record of the source as it is now, and a file in the tree that
// still hashes to what that record produced — so a hand-edited or missing file
// is an ordinary miss and is adapted again. A nil Adapter, or one with no tree
// or store, adapts everything.
type Adapter struct {
	Tree  Tree
	Store Store

	load    sync.Once
	mu      sync.Mutex
	records map[string]adaptation
	changed bool
}

// adaptation is the record for one generated path.
type adaptation struct {
	Source string `json:"source"`
	Output string `json:"output"`
}

// request is one file to adapt. Reading its source is part of the request so
// that the files of a build are read concurrently.
type request struct {
	dest, name string
	source     func() ([]byte, error)
	adapt      func(name string, source []byte) ([]byte, error)
}

// run answers every request, concurrently, in order. It reports the first
// failure in request order, which is the one adapting them in turn would.
func (a *Adapter) run(requests []request) ([]File, error) {
	if a == nil {
		a = &Adapter{}
	}
	a.loadRecords()
	files := make([]File, len(requests))
	errs := make([]error, len(requests))
	next := make(chan int, len(requests))
	for i := range requests {
		next <- i
	}
	close(next)
	var wg sync.WaitGroup
	for range min(runtime.GOMAXPROCS(0), len(requests)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				data, err := a.answer(requests[i])
				files[i], errs[i] = File{Path: requests[i].dest, Data: data}, err
			}
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return files, nil
}

func (a *Adapter) answer(r request) ([]byte, error) {
	source, err := r.source()
	if err != nil {
		return nil, err
	}
	digest := contentDigest(source)
	a.mu.Lock()
	record, recorded := a.records[r.dest]
	a.mu.Unlock()
	if recorded && record.Source == digest && a.Tree != nil {
		if data, ok := a.Tree.Source(r.dest); ok && contentDigest(data) == record.Output {
			return data, nil
		}
	}
	data, err := r.adapt(r.name, source)
	if err != nil {
		return nil, err
	}
	fresh := adaptation{Source: digest, Output: contentDigest(data)}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.records[r.dest] != fresh {
		a.records[r.dest] = fresh
		a.changed = true
	}
	return data, nil
}

func (a *Adapter) loadRecords() {
	a.load.Do(func() {
		a.records = map[string]adaptation{}
		if a.Store == nil {
			return
		}
		if data, ok := a.Store.Load(); ok {
			var stored map[string]adaptation
			if json.Unmarshal(data, &stored) == nil {
				maps.Copy(a.records, stored)
			}
		}
	})
}

// Save stores the records if this build changed any. Records for paths this
// build did not ask about stay, because they are a sibling program's; storage
// is bounded by the paths a project has generated. Two builds racing here lose
// one's records, which only costs it the adaptation next time.
func (a *Adapter) Save() {
	if a == nil || a.Store == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.changed {
		return
	}
	if data, err := json.Marshal(a.records); err == nil {
		a.Store.Store(data)
		a.changed = false
	}
}

func contentDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
