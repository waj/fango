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

const (
	schema       = 2
	artifactKind = "project"
)

type payload struct {
	Entry  string                  `json:"entry"`
	Mode   string                  `json:"mode"`
	Inputs []modules.ManifestEntry `json:"inputs"`
	Files  []codegen.File          `json:"files"`
}

type artifact struct {
	Schema        int     `json:"schema"`
	Kind          string  `json:"kind"`
	PayloadSHA256 string  `json:"payload_sha256"`
	Payload       payload `json:"payload"`
}

type fingerprintResult struct {
	sync.Once
	value string
	err   error
}

var fingerprint = &fingerprintResult{}

var executablePath = os.Executable
var cacheRoots = build.CacheDirs

type fileOperations struct {
	readFile   func(string) ([]byte, error)
	mkdirAll   func(string, os.FileMode) error
	createTemp func(string, string) (*os.File, error)
	rename     func(string, string) error
	remove     func(string) error
}

var cacheFiles = fileOperations{os.ReadFile, os.MkdirAll, os.CreateTemp, os.Rename, os.Remove}

func compilerFingerprint() (string, error) {
	fingerprint.Do(func() {
		path, err := executablePath()
		if err != nil {
			fingerprint.err = err
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

func paths(entry, mode string) ([]string, error) {
	abs, err := filepath.Abs(entry)
	if err != nil {
		return nil, err
	}
	local, fallback, err := cacheRoots(abs)
	if err != nil {
		return nil, err
	}
	fp, err := compilerFingerprint()
	if err != nil {
		return nil, err
	}
	h := sha256.Sum256([]byte(abs + "\x00" + mode))
	name := hex.EncodeToString(h[:]) + ".json"
	paths := []string{filepath.Join(local, "v1", fp, name)}
	if fallback != "" {
		paths = append(paths, filepath.Join(fallback, "v1", fp, name))
	}
	return paths, nil
}

func payloadHash(p payload) (string, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

// Load returns a validated successful artifact. Bundled inputs are covered by
// the executable fingerprint; project discovery is revalidated without
// invoking the lexer or parser.
func Load(entry, mode string) ([]codegen.File, []modules.ManifestEntry, bool) {
	ps, err := paths(entry, mode)
	if err != nil {
		return nil, nil, false
	}
	abs, err := filepath.Abs(entry)
	if err != nil {
		return nil, nil, false
	}
	for _, path := range ps {
		data, readErr := cacheFiles.readFile(path)
		if readErr != nil {
			continue
		}
		var a artifact
		if json.Unmarshal(data, &a) != nil || a.Schema != schema || a.Kind != artifactKind ||
			a.Payload.Entry != abs || a.Payload.Mode != mode || a.PayloadSHA256 == "" ||
			a.Payload.Inputs == nil || a.Payload.Files == nil {
			continue
		}
		digest, hashErr := payloadHash(a.Payload)
		if hashErr != nil || !strings.EqualFold(digest, a.PayloadSHA256) {
			continue
		}
		if !validFiles(mode, a.Payload.Files) || !modules.ValidateManifest(abs, a.Payload.Inputs) {
			continue
		}
		return a.Payload.Files, a.Payload.Inputs, true
	}
	return nil, nil, false
}

func validFiles(mode string, files []codegen.File) bool {
	switch {
	case mode == "check":
		return len(files) == 0
	case strings.HasPrefix(mode, "emit:"):
		return build.ValidateGeneratedFiles(files)
	case len(files) == 0:
		return true
	default:
		return build.ValidateGeneratedFiles(files)
	}
}

// Store atomically publishes a successful artifact, preferring source-local
// storage and falling back to the source-root namespace. Errors are ignored by
// callers; a read-only or concurrently modified cache remains an optimization.
func Store(entry, mode string, inputs []modules.ManifestEntry, files []codegen.File) {
	ps, err := paths(entry, mode)
	if err != nil {
		return
	}
	abs, err := filepath.Abs(entry)
	if files == nil {
		files = []codegen.File{}
	}
	if err != nil || !validFiles(mode, files) || !modules.ValidateManifest(abs, inputs) {
		return
	}
	p := payload{Entry: abs, Mode: mode, Inputs: inputs, Files: files}
	digest, err := payloadHash(p)
	if err != nil {
		return
	}
	data, err := json.Marshal(artifact{Schema: schema, Kind: artifactKind, PayloadSHA256: digest, Payload: p})
	if err != nil {
		return
	}
	for _, path := range ps {
		if writeAtomic(path, data) == nil {
			return
		}
	}
}

func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := cacheFiles.mkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := cacheFiles.createTemp(dir, ".artifact-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	closed := false
	defer func() {
		if !closed {
			_ = tmp.Close()
		}
		_ = cacheFiles.remove(name)
	}()
	if err := tmp.Chmod(0o644); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	closed = true
	return cacheFiles.rename(name, path)
}
