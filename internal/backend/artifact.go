package backend

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"reflect"

	"github.com/waj/fango/internal/artifactframe"
	"github.com/waj/fango/internal/codegen"
)

const (
	emissionSchema = 12
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

// unitArtifact is an emitted unit's whole slot: what the unit was built from,
// and the digest of what came out. The generated Go itself is not here. It is
// already on disk at the record's path, in the tree the Go toolchain compiles,
// and storing it again would mean keeping the same bytes twice and reading
// them twice. The digest cannot live inside the record, which is assembled
// before the bytes exist; it is what a lookup holds the file to.
type unitArtifact struct {
	Built  unitRecord `json:"built"`
	Source string     `json:"source"`
}

func sourceDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func encodeUnit(record unitRecord, file codegen.File) []byte {
	payload, err := json.Marshal(unitArtifact{Built: record, Source: sourceDigest(file.Data)})
	if err != nil {
		return nil
	}
	return artifactframe.Wrap(emissionKind, emissionSchema, payload)
}

// decodeUnit returns the digest the artifact expects its generated file to
// have, if the unit it describes is the one being built now.
func decodeUnit(data []byte, want unitRecord) (string, bool) {
	payload, ok := artifactframe.Unwrap(emissionKind, emissionSchema, data)
	if !ok {
		return "", false
	}
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.DisallowUnknownFields()
	var have unitArtifact
	if err := dec.Decode(&have); err != nil {
		return "", false
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return "", false
	}
	if have.Source == "" || !reflect.DeepEqual(have.Built, want) {
		return "", false
	}
	return have.Source, true
}

// loadUnit also reports the artifact bytes it read, which a superseded or
// structurally invalid artifact still costs even though it is a miss.
func loadUnit(cache Cache, slot string, want unitRecord) (string, int, bool) {
	data, ok := cache.Load(slot)
	if !ok {
		return "", 0, false
	}
	digest, ok := decodeUnit(data, want)
	return digest, len(data), ok
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
