package check

import (
	"testing"

	"github.com/waj/fango/internal/core"
)

func TestCallbackContractParticipatesInModuleABI(t *testing.T) {
	object := &ModuleObject{Runtime: []core.Def{{Name: "M.use", ABI: core.ABISummary{Valid: true, Callbacks: []core.CallbackABI{{Arity: 2, DirectUses: 1, ExitUses: 2}}}}}}
	semantic, abi, implementation := ownFingerprints(object)
	object.Runtime[0].ABI.Callbacks[0] = core.CallbackABI{}
	nextSemantic, nextABI, nextImplementation := ownFingerprints(object)
	if semantic != nextSemantic {
		t.Fatal("a private calling convention changed the source interface")
	}
	if abi == nextABI || implementation == nextImplementation {
		t.Fatal("calling convention change did not invalidate consumer/owner backend keys")
	}
}
