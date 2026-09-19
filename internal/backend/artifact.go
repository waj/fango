package backend

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"strconv"

	"github.com/waj/fango/internal/codegen"
)

const emissionSchema = 1

type emissionPayload struct {
	Key  string `json:"key"`
	Path string `json:"path"`
	Data []byte `json:"data"`
}

type emissionEnvelope struct {
	Schema        int             `json:"schema"`
	Kind          string          `json:"kind"`
	PayloadSHA256 string          `json:"payload_sha256"`
	Payload       emissionPayload `json:"payload"`
}

func digest(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		_, _ = h.Write([]byte(strconv.Itoa(len(part))))
		_, _ = h.Write([]byte{':'})
		_, _ = h.Write([]byte(part))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func loadUnit(cache Cache, key, path string) ([]byte, bool) {
	data, ok := cache.Load(key)
	if !ok {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var envelope emissionEnvelope
	if err := dec.Decode(&envelope); err != nil {
		return nil, false
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return nil, false
	}
	payload, err := json.Marshal(envelope.Payload)
	if err != nil {
		return nil, false
	}
	h := sha256.Sum256(payload)
	if envelope.Schema != emissionSchema || envelope.Kind != "emitted-unit" ||
		envelope.PayloadSHA256 != hex.EncodeToString(h[:]) ||
		envelope.Payload.Key != key || envelope.Payload.Path != path || len(envelope.Payload.Data) == 0 {
		return nil, false
	}
	return envelope.Payload.Data, true
}

func storeUnit(cache Cache, key string, file codegen.File) {
	payload := emissionPayload{Key: key, Path: file.Path, Data: file.Data}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return
	}
	h := sha256.Sum256(encoded)
	data, err := json.Marshal(emissionEnvelope{Schema: emissionSchema, Kind: "emitted-unit", PayloadSHA256: hex.EncodeToString(h[:]), Payload: payload})
	if err != nil {
		return
	}
	cache.Store(key, data)
}
