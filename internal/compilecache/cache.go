// Package compilecache locates the compiler's persistent artifacts — checked
// module objects and emitted Go — beneath a source-local root with a
// user-cache fallback. Cache failures are deliberately
// indistinguishable from misses: compilation must never depend on this
// optimization being writable or intact.
package compilecache

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"sync"

	"github.com/waj/fango/internal/build"
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
	readFile func(string) ([]byte, error)
}

// cacheFiles reads the running executable for the compiler fingerprint.
var cacheFiles = fileOperations{os.ReadFile}

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
