// Package compilecache persists successful compiler results. Cache failures
// are deliberately indistinguishable from misses: compilation must never
// depend on this optimization being writable or intact.
package compilecache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/waj/fango/internal/build"
	"github.com/waj/fango/internal/codegen"
	"github.com/waj/fango/internal/modules"
)

const schema = 1

type artifact struct {
	Schema int                     `json:"schema"`
	Entry  string                  `json:"entry"`
	Mode   string                  `json:"mode"`
	Inputs []modules.ManifestEntry `json:"inputs"`
	Files  []codegen.File          `json:"files,omitempty"`
}

var fingerprint struct {
	sync.Once
	value string
}

func compilerFingerprint() (string, error) {
	var err error
	fingerprint.Do(func() {
		path, e := os.Executable()
		if e != nil {
			err = e
			return
		}
		data, e := os.ReadFile(path)
		if e != nil {
			err = e
			return
		}
		h := sha256.Sum256(data)
		fingerprint.value = hex.EncodeToString(h[:])
	})
	return fingerprint.value, err
}

func path(entry, mode string) (string, error) {
	abs, err := filepath.Abs(entry)
	if err != nil {
		return "", err
	}
	root, err := build.CacheDir(abs)
	if err != nil {
		return "", err
	}
	fp, err := compilerFingerprint()
	if err != nil {
		return "", err
	}
	h := sha256.Sum256([]byte(abs + "\x00" + mode))
	return filepath.Join(root, "v1", fp, hex.EncodeToString(h[:])+".json"), nil
}

// Load returns a validated successful artifact. Bundled inputs are covered by
// the executable fingerprint; project inputs are re-hashed from the source
// root without invoking the lexer or parser.
func Load(entry, mode string) ([]codegen.File, []modules.ManifestEntry, bool) {
	p, err := path(entry, mode)
	if err != nil {
		return nil, nil, false
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, nil, false
	}
	var a artifact
	if json.Unmarshal(data, &a) != nil || a.Schema != schema || a.Mode != mode {
		return nil, nil, false
	}
	abs, err := filepath.Abs(entry)
	if err != nil || a.Entry != abs {
		return nil, nil, false
	}
	root := filepath.Dir(abs)
	known := make(map[string]bool, len(a.Inputs))
	for _, in := range a.Inputs {
		if strings.HasPrefix(in.Path, "<stdlib>/") {
			continue
		}
		rel := filepath.Clean(filepath.FromSlash(in.Path))
		if filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, nil, false
		}
		known[filepath.ToSlash(rel)] = true
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			return nil, nil, false
		}
		h := sha256.Sum256(b)
		if hex.EncodeToString(h[:]) != in.SHA256 {
			return nil, nil, false
		}
	}
	// A newly added sidecar is an input change too. It cannot appear in the
	// old manifest, so derive each possible sidecar path from its source.
	for rel := range known {
		if !strings.HasSuffix(rel, ".fango") {
			continue
		}
		native := strings.TrimSuffix(rel, ".fango") + ".native.go"
		_, err := os.Stat(filepath.Join(root, filepath.FromSlash(native)))
		if (err == nil) != known[native] {
			return nil, nil, false
		}
	}
	return a.Files, a.Inputs, true
}

// Store atomically publishes a successful artifact. Errors are intentionally
// ignored by callers; a read-only or concurrently modified cache is a miss.
func Store(entry, mode string, inputs []modules.ManifestEntry, files []codegen.File) {
	p, err := path(entry, mode)
	if err != nil {
		return
	}
	abs, err := filepath.Abs(entry)
	if err != nil {
		return
	}
	a := artifact{Schema: schema, Entry: abs, Mode: mode, Inputs: inputs, Files: files}
	data, err := json.Marshal(a)
	if err != nil || os.MkdirAll(filepath.Dir(p), 0o755) != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".artifact-*")
	if err != nil {
		return
	}
	name := tmp.Name()
	ok := false
	defer func() {
		tmp.Close()
		if !ok {
			os.Remove(name)
		}
	}()
	if tmp.Chmod(0o644) != nil {
		return
	}
	if _, err = tmp.Write(data); err != nil {
		return
	}
	if tmp.Sync() != nil || tmp.Close() != nil {
		return
	}
	if os.Rename(name, p) != nil {
		return
	}
	ok = true
}
