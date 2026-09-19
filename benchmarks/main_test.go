package benchmarks

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/waj/fango/internal/libroot"
)

// TestMain names the library this repository owns. Both gates build the
// compiler into a temporary directory and run it there, so neither the
// executable-relative install layout nor the checkout walk would reach it.
func TestMain(m *testing.M) {
	root, err := filepath.Abs("..")
	if err != nil {
		fmt.Fprintf(os.Stderr, "locating the Fango library: %v\n", err)
		os.Exit(1)
	}
	os.Setenv(libroot.EnvRoot, root)
	os.Exit(m.Run())
}
