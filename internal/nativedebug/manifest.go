// Package nativedebug implements source stepping over Delve's JSON-RPC API.
// It is linked into the compiler frontend only, never into generated programs.
package nativedebug

import (
	"crypto/sha256"
	"debug/dwarf"
	"debug/elf"
	"debug/macho"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
)

const Suffix = ".fango-debug.json"
const version = 1

type Manifest struct {
	Version    int
	BinaryHash string
	Sources    map[string]string
	Lines      map[string][]int
}

func WriteManifest(binary string, data []byte, sources map[string]string) error {
	lines, err := StatementLines(binary, sources)
	if err != nil {
		return err
	}
	m := Manifest{version, fmt.Sprintf("%x", sha256.Sum256(data)), sources, lines}
	encoded, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return os.WriteFile(binary+Suffix, encoded, 0644)
}

func ReadManifest(binary string) (*Manifest, error) {
	data, err := os.ReadFile(binary + Suffix)
	if err != nil {
		return nil, fmt.Errorf("native source metadata missing: rebuild with fango build --debug: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	if m.Version != version {
		return nil, fmt.Errorf("unsupported native debug metadata version %d; rebuild with --debug", m.Version)
	}
	f, err := os.Open(binary)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}
	if m.BinaryHash != fmt.Sprintf("%x", h.Sum(nil)) {
		return nil, fmt.Errorf("native debug metadata does not match binary; rebuild with --debug")
	}
	return &m, nil
}

// StatementLines reads actual linked statement entries, excluding eliminated
// functions and all generated scaffolding. Delve resolves their relocated PCs.
func StatementLines(binary string, sources map[string]string) (map[string][]int, error) {
	var data *dwarf.Data
	if f, err := elf.Open(binary); err == nil {
		defer f.Close()
		data, err = f.DWARF()
		if err != nil {
			return nil, err
		}
	} else if f, err := macho.Open(binary); err == nil {
		defer f.Close()
		data, err = f.DWARF()
		if err != nil {
			return nil, err
		}
	} else {
		return nil, fmt.Errorf("native source debugging requires an ELF or Mach-O binary")
	}
	seen := map[string]map[int]bool{}
	r := data.Reader()
	for {
		entry, err := r.Next()
		if err != nil {
			return nil, err
		}
		if entry == nil {
			break
		}
		if entry.Tag != dwarf.TagCompileUnit {
			continue
		}
		lines, err := data.LineReader(entry)
		if err != nil {
			return nil, err
		}
		if lines == nil {
			continue
		}
		for {
			var line dwarf.LineEntry
			err := lines.Next(&line)
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, err
			}
			if line.File == nil || !line.IsStmt || line.EndSequence || line.Line < 1 {
				continue
			}
			if _, ok := sources[line.File.Name]; !ok {
				continue
			}
			if seen[line.File.Name] == nil {
				seen[line.File.Name] = map[int]bool{}
			}
			seen[line.File.Name][line.Line] = true
		}
	}
	out := map[string][]int{}
	for name, lines := range seen {
		for line := range lines {
			out[name] = append(out[name], line)
		}
		sort.Ints(out[name])
	}
	return out, nil
}
