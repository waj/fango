package check

import (
	"path/filepath"
	"testing"

	"github.com/waj/fango/internal/types"
)

func TestDynamicCoroutineContractsAcrossModuleObjects(t *testing.T) {
	for _, fixture := range []string{"coroutine_registration_latent", "coroutine_registration_failure"} {
		t.Run(fixture, func(t *testing.T) {
			cache := newMemoryObjectCache()
			path := filepath.Join("..", "..", "testdata", "run", fixture+".fango")
			compileEvents(t, path, cache)
			_, events := compileEvents(t, path, cache)
			if events["checked-cache-hit"]["Coroutine"] != 1 || events["checked-cache-hit"]["Work"] != 1 {
				t.Fatalf("missing cached contracts: %v", events)
			}
		})
	}
	result, _ := compileEvents(t, filepath.Join("..", "..", "testdata", "modules", "dynamic_coroutines", "Helpers.fango"), newMemoryObjectCache())
	scheme, ok := result.Checker.Env.Lookup("Helpers.register")
	if !ok {
		t.Fatal("missing exported registration helper")
	}
	arrow := scheme.Body.(*types.TFun)
	if len(arrow.Eff.Labels) != 0 {
		t.Fatalf("producer-local control escaped registration: %s", types.Show(scheme.Body))
	}
}
