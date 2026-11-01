package runtimefiles

import (
	"bytes"
	"strings"
	"testing"
)

func TestGeneratedRuntimeSources(t *testing.T) {
	var adapter *Adapter
	files, err := adapter.Packages("fangort", "nativewire", "nativeworker")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no runtime sources")
	}
	for _, file := range files {
		if strings.HasSuffix(file.Path, "_test.go") {
			t.Errorf("test source materialized: %s", file.Path)
		}
		if bytes.Contains(file.Data, []byte(repositoryRuntime)) {
			t.Errorf("repository import survived in %s", file.Path)
		}
	}
	sidecars, err := adapter.Sidecars([]Sidecar{{Dir: "native/Hash/", Path: "Hash.native.go", Source: []byte("package native\n")}})
	if err != nil {
		t.Fatal(err)
	}
	host := sidecars[1].Data
	if !bytes.Contains(host, []byte(`"fangobuild/fangort"`)) || bytes.Contains(host, []byte(repositoryRuntime)) {
		t.Fatalf("host imports were not adapted:\n%s", host)
	}
}
