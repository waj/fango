package backend

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"

	"github.com/waj/fango/internal/artifactframe"
	"github.com/waj/fango/internal/codegen"
)

const (
	emissionSchema = 2
	emissionKind   = "emitted-unit"
)

type linkedModule struct {
	Module   string `json:"module"`
	Semantic string `json:"semantic"`
	ABI      string `json:"abi"`
}

// unitRecord is what an emitted unit says it was built from: its own runtime
// Core and contracts, the contracts of the modules it links against, its role
// and generated path, and the entry-only inputs. A lookup compares this
// against the graph it has, and a record that disagrees is an ordinary miss.
type unitRecord struct {
	Schema         int            `json:"schema"`
	Unit           string         `json:"unit"`
	Path           string         `json:"path"`
	Entry          bool           `json:"entry"`
	PrintMain      bool           `json:"print_main"`
	Implementation string         `json:"implementation"`
	Semantic       string         `json:"semantic"`
	ABI            string         `json:"abi"`
	Linked         []linkedModule `json:"linked"`
	EntrySymbol    string         `json:"entry_symbol"`
	NativeLinks    []string       `json:"native_links"`
	Intrinsics     []string       `json:"intrinsics"`
}

// An emitted unit is the record of what it was built from on one line,
// followed by the generated Go source verbatim. The artifact frame carries the
// digest, so the payload needs no container of its own and the source needs no
// re-encoding.
func encodeUnit(record unitRecord, file codegen.File) []byte {
	line, err := json.Marshal(record)
	if err != nil || bytes.ContainsRune(line, '\n') {
		return nil
	}
	payload := make([]byte, 0, len(line)+1+len(file.Data))
	payload = append(payload, line...)
	payload = append(payload, '\n')
	payload = append(payload, file.Data...)
	return artifactframe.Wrap(emissionKind, emissionSchema, payload)
}

func decodeUnit(data []byte, want unitRecord) ([]byte, bool) {
	payload, ok := artifactframe.Unwrap(emissionKind, emissionSchema, data)
	if !ok {
		return nil, false
	}
	line, source, ok := bytes.Cut(payload, []byte{'\n'})
	if !ok || len(source) == 0 {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	var have unitRecord
	if err := dec.Decode(&have); err != nil {
		return nil, false
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return nil, false
	}
	if !reflect.DeepEqual(have, want) {
		return nil, false
	}
	return source, true
}

// loadUnit also reports the artifact bytes it read, which a superseded or
// structurally invalid artifact still costs even though it is a miss.
func loadUnit(cache Cache, slot string, want unitRecord) ([]byte, int, bool) {
	data, ok := cache.Load(slot)
	if !ok {
		return nil, 0, false
	}
	source, ok := decodeUnit(data, want)
	return source, len(data), ok
}

// storeUnit reports the bytes it wrote, or zero when the unit could not be
// encoded.
func storeUnit(cache Cache, slot string, record unitRecord, file codegen.File) int {
	data := encodeUnit(record, file)
	if data == nil {
		return 0
	}
	cache.Store(slot, data)
	return len(data)
}
