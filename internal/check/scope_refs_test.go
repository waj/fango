package check

import (
	"github.com/waj/fango/internal/types"
	"testing"
)

func TestImportedScopeRetainedByOwnedSummary(t *testing.T) {
	imported := types.CaptureSummary{Captures: types.ScopeCapture(10)}
	own := types.CaptureSummary{Captures: types.UnionCaptures(types.ScopeCapture(10), types.ScopeCapture(20))}
	names := foreignScopeNames(map[string]types.CaptureSummary{"Fail.attempt": imported, "Async.capture": own}, map[string]types.CaptureSummary{"Async.capture": own}, own)
	if names[10] != "Fail.attempt#0" || names[20] != "" {
		t.Fatalf("scope identities: %v", names)
	}
}
