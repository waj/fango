package natives

import (
	"strings"
	"testing"
)

// Host fallbacks must never become executable during staging when a new
// adapter is added. Worker-only modules need no placeholder registry entries.
func TestRegistryExecutionPolicy(t *testing.T) {
	host := map[string]Spec{}
	installHost(host)
	installPrompt(host)
	for name := range host {
		if spec, ok := Lookup(name); !ok || spec.CompileTimeSafe {
			t.Errorf("host adapter %s: registered=%t, stage-safe=%t", name, ok, spec.CompileTimeSafe)
		}
	}
	for name := range table {
		if strings.HasPrefix(name, "File.") || strings.HasPrefix(name, "Net.") {
			t.Errorf("worker-only native %s has an unnecessary in-process adapter", name)
		}
	}
}
