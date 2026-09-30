package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstrumentDecodeBoundary(t *testing.T) {
	for _, body := range []string{
		"var v_total int = fold(); _ = v_total",
		"switch 0 { case 0: var v_total int = fold(); _ = v_total }",
	} {
		t.Run(body, func(t *testing.T) {
			project := t.TempDir()
			dir := filepath.Join(project, "entries", "main")
			if err := os.MkdirAll(dir, 0755); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "main.go")
			if err := os.WriteFile(path, []byte("package main; func main(){"+body+"}"), 0644); err != nil {
				t.Fatal(err)
			}
			if err := instrument(project); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			s := string(data)
			if !strings.Contains(s, "func benchmarkMain()") || strings.Count(s, "snapshotDecodedOutput()") != 1 || strings.Index(s, "snapshotDecodedOutput()") > strings.Index(s, "fold()") {
				t.Fatalf("incorrect decode boundary:\n%s", s)
			}
		})
	}
}
