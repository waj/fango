// Package compilecache locates the compiler's persistent artifacts — checked
// module objects and emitted Go — beneath a source-local root with a
// user-cache fallback. Cache failures are deliberately
// indistinguishable from misses: compilation must never depend on this
// optimization being writable or intact.
//
// Each module keeps one artifact per kind, at a slot named after the module
// rather than after a hash of its inputs, and a rebuild overwrites it. The
// artifact itself records what it was built from; the layers above compare
// that record against the graph they have. Storage is therefore bounded by the
// modules a project has, not by its edit history.
package compilecache

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/waj/fango/internal/artifactstore"
	"github.com/waj/fango/internal/build"
)

const (
	checkedKind = "checked"
	emittedKind = "emitted"

	// moduleGroup and entryGroup keep a headerless String.fango entry from
	// sharing a slot with the stdlib String it imports: the two are different
	// modules that answer to the same name.
	moduleGroup = "module"
	entryGroup  = "entry"
)

type fingerprintResult struct {
	sync.Once
	value string
	err   error
}

var fingerprint = &fingerprintResult{}

var executablePath = os.Executable
var cacheRoots = build.CacheDirs

type fileOperations struct {
	readHead func(string) ([]byte, error)
	readFile func(string) ([]byte, error)
}

// cacheFiles reads the running executable for the compiler fingerprint.
var cacheFiles = fileOperations{readHead, os.ReadFile}

// buildIDSpan is how far into an executable the Go linker's build ID is
// sought. It sits at the start of the text segment, a page into the file.
const buildIDSpan = 64 << 10

func readHead(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	head := make([]byte, buildIDSpan)
	n, err := io.ReadFull(f, head)
	if err == io.ErrUnexpectedEOF || err == io.EOF {
		err = nil
	}
	return head[:n], err
}

// goBuildID returns the build ID the Go toolchain embeds in an executable.
// Its last component is a digest of the linked binary's own content, so it
// identifies a compiler build as well as hashing the file does, without
// reading the whole file. An executable without a well-formed one — linked
// with -buildid= — has no such digest.
func goBuildID(head []byte) (string, bool) {
	const prefix, suffix = "\xff Go build ID: \"", "\"\n \xff"
	i := bytes.Index(head, []byte(prefix))
	if i < 0 {
		return "", false
	}
	rest := head[i+len(prefix):]
	j := bytes.Index(rest, []byte(suffix))
	if j < 0 {
		return "", false
	}
	id := string(rest[:j])
	parts := strings.Split(id, "/")
	if len(parts) != 2 && len(parts) != 4 {
		return "", false
	}
	for _, part := range parts {
		if part == "" {
			return "", false
		}
	}
	return id, true
}

// compilerFingerprint selects a namespace per compiler build. Both its value
// and any failure to compute it are stable for the process, so a compiler that
// cannot read itself simply does not use the cache.
func compilerFingerprint() (string, error) {
	fingerprint.Do(func() {
		path, err := executablePath()
		if err != nil {
			fingerprint.err = err
			return
		}
		head, err := cacheFiles.readHead(path)
		if err != nil {
			fingerprint.err = err
			return
		}
		if id, ok := goBuildID(head); ok {
			h := sha256.Sum256([]byte("go build ID " + id))
			fingerprint.value = hex.EncodeToString(h[:])
			return
		}
		data, err := cacheFiles.readFile(path)
		if err != nil {
			fingerprint.err = err
			return
		}
		h := sha256.Sum256(data)
		fingerprint.value = hex.EncodeToString(h[:])
	})
	return fingerprint.value, fingerprint.err
}

// Slot names the one artifact a module keeps for a kind. It is the module's
// own identity, never a digest of its inputs, so a rebuild replaces the
// artifact instead of adding one beside it.
func Slot(entry bool, name string) string {
	if entry {
		return entryGroup + "/" + name
	}
	return moduleGroup + "/" + name
}

// slotName is what a slot component may be spelled with. A name that cannot
// be spelled as a path component declines to cache rather than being escaped
// into one.
var slotName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*$`)

func slotPath(kind, slot string) (string, bool) {
	group, name, ok := strings.Cut(slot, "/")
	if !ok || (group != moduleGroup && group != entryGroup) || !slotName.MatchString(name) {
		return "", false
	}
	return kind + "/" + group + "/" + name + ".json", true
}

// slotStore is the shared root selection and slot addressing behind both seams.
type slotStore struct {
	bytes *artifactstore.Store
}

func newStore(entry string) *slotStore {
	abs, err := filepath.Abs(entry)
	if err != nil {
		return &slotStore{}
	}
	local, fallback, err := cacheRoots(abs)
	if err != nil {
		return &slotStore{}
	}
	fp, err := compilerFingerprint()
	if err != nil {
		return &slotStore{}
	}
	roots := []string{filepath.Join(local, "v1", fp)}
	if fallback != "" {
		roots = append(roots, filepath.Join(fallback, "v1", fp))
	}
	return &slotStore{bytes: artifactstore.New(roots...)}
}

func (s *slotStore) load(kind, slot string) ([]byte, bool) {
	path, ok := slotPath(kind, slot)
	if !ok {
		return nil, false
	}
	return s.bytes.Load(path)
}

func (s *slotStore) store(kind, slot string, data []byte) {
	path, ok := slotPath(kind, slot)
	if !ok || len(data) == 0 {
		return
	}
	s.bytes.Store(path, data)
}
