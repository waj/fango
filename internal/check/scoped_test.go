package check

import (
	"github.com/waj/fango/internal/types"
	"testing"
)

func TestScopedFingerprintPreservesIdentity(t *testing.T) {
	digest := func(ids ...int) string {
		var labels []types.EffLabel
		for _, id := range ids {
			labels = append(labels, types.EffLabel{Unique: id, Name: "local scope", Scoped: true})
		}
		return canonicalDigest(labels, &ModuleObject{})
	}
	if digest(10, 20, 10) != digest(100, 200, 100) {
		t.Fatal("fresh numbering changed fingerprint")
	}
	if digest(10, 20, 10) == digest(10, 20, 20) {
		t.Fatal("distinct scope sharing has identical fingerprint")
	}
}
