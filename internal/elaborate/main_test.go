package elaborate

import (
	"os"
	"testing"
)

// Dumps and goldens here describe elaboration; inline_test.go enables the
// inliner for its own cases.
func TestMain(m *testing.M) {
	Inlining = false
	os.Exit(m.Run())
}
