package check

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"

	"github.com/waj/fango/internal/modules"
	"github.com/waj/fango/internal/source"
)

const checkedCandidateSchema = 1

type ObjectCache interface {
	LoadCandidates(baseKey string) [][]byte
	StoreCandidate(baseKey string, data []byte)
	LoadObject(objectKey string) ([]byte, bool)
	StoreObject(objectKey string, data []byte)
}

type stageInput struct {
	Module      string `json:"module"`
	Fingerprint string `json:"fingerprint"`
}

type candidatePayload struct {
	BaseKey           string       `json:"base_key"`
	Module            string       `json:"module"`
	ObjectKey         string       `json:"object_key"`
	Semantic          string       `json:"semantic"`
	ABI               string       `json:"abi"`
	StageDependencies []stageInput `json:"stage_dependencies"`
}

type candidateEnvelope struct {
	Schema        int              `json:"schema"`
	Kind          string           `json:"kind"`
	PayloadSHA256 string           `json:"payload_sha256"`
	Payload       candidatePayload `json:"payload"`
}

type moduleSummary struct {
	Semantic string
	ABI      string
	Stage    string
}

func moduleBaseKey(module modules.ResolvedModule, fixityHash string, summaries map[string]moduleSummary) (string, bool) {
	parts := []string{"checked-module-base", module.Name, module.SourceHash, module.NativeHash, fixityHash, fmt.Sprint(module.Role), module.Entry}
	for _, dep := range module.Dependencies {
		summary, ok := summaries[dep]
		if !ok {
			return "", false
		}
		parts = append(parts, dep, summary.Semantic)
	}
	return digest(parts...), true
}

func dependencyFingerprints(names []string, summaries map[string]moduleSummary, field func(moduleSummary) string) ([]string, bool) {
	out := make([]string, 0, len(names)*2)
	for _, name := range names {
		summary, ok := summaries[name]
		if !ok {
			return nil, false
		}
		out = append(out, name, field(summary))
	}
	return out, true
}

func finalObjectKey(base string, inputs []stageInput) string {
	parts := []string{"checked-module-object", base}
	for _, input := range inputs {
		parts = append(parts, input.Module, input.Fingerprint)
	}
	return digest(parts...)
}

func makeCandidate(base string, object *ModuleObject, summaries map[string]moduleSummary) (candidatePayload, bool) {
	names := append([]string(nil), object.CheckStageDependencies...)
	sort.Strings(names)
	inputs := make([]stageInput, 0, len(names))
	for _, name := range names {
		summary, ok := summaries[name]
		if !ok {
			return candidatePayload{}, false
		}
		inputs = append(inputs, stageInput{Module: name, Fingerprint: summary.Stage})
	}
	return candidatePayload{BaseKey: base, Module: object.State.Name, ObjectKey: finalObjectKey(base, inputs), Semantic: object.Semantic, ABI: object.ABI, StageDependencies: inputs}, true
}

func encodeCandidate(payload candidatePayload) ([]byte, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	h := sha256.Sum256(b)
	return json.Marshal(candidateEnvelope{Schema: checkedCandidateSchema, Kind: "checked-module-candidate", PayloadSHA256: hex.EncodeToString(h[:]), Payload: payload})
}

func decodeCandidate(data []byte, base, module string, summaries map[string]moduleSummary) (candidatePayload, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var envelope candidateEnvelope
	if err := dec.Decode(&envelope); err != nil {
		return candidatePayload{}, err
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return candidatePayload{}, fmt.Errorf("trailing candidate data")
	}
	payload, err := json.Marshal(envelope.Payload)
	if err != nil {
		return candidatePayload{}, err
	}
	h := sha256.Sum256(payload)
	if envelope.Schema != checkedCandidateSchema || envelope.Kind != "checked-module-candidate" || envelope.PayloadSHA256 != hex.EncodeToString(h[:]) || envelope.Payload.BaseKey != base || envelope.Payload.Module != module {
		return candidatePayload{}, fmt.Errorf("invalid checked-module candidate")
	}
	last := ""
	for _, input := range envelope.Payload.StageDependencies {
		if input.Module <= last || summaries[input.Module].Stage == "" || summaries[input.Module].Stage != input.Fingerprint {
			return candidatePayload{}, fmt.Errorf("stale stage dependency %q", input.Module)
		}
		last = input.Module
	}
	if envelope.Payload.ObjectKey != finalObjectKey(base, envelope.Payload.StageDependencies) {
		return candidatePayload{}, fmt.Errorf("invalid checked-module object key")
	}
	return envelope.Payload, nil
}

func loadCachedObject(cache ObjectCache, base string, module modules.ResolvedModule, summaries map[string]moduleSummary, sources map[string]*source.File) (*ModuleObject, bool) {
	for _, data := range cache.LoadCandidates(base) {
		candidate, err := decodeCandidate(data, base, module.Name, summaries)
		if err != nil {
			continue
		}
		data, ok := cache.LoadObject(candidate.ObjectKey)
		if !ok {
			continue
		}
		object, err := DecodeObject(data, sources)
		if err != nil || object.State == nil || object.State.Name != module.Name {
			continue
		}
		if len(object.CheckStageDependencies) != len(candidate.StageDependencies) {
			continue
		}
		valid := true
		for i, input := range candidate.StageDependencies {
			valid = valid && object.CheckStageDependencies[i] == input.Module
		}
		ownSemantic, ownABI, ownStage := ownFingerprints(object)
		semanticDeps, semanticOK := dependencyFingerprints(module.Dependencies, summaries, func(s moduleSummary) string { return s.Semantic })
		abiDeps, abiOK := dependencyFingerprints(module.Dependencies, summaries, func(s moduleSummary) string { return s.ABI })
		stageDeps, stageOK := dependencyFingerprints(object.StageDependencies, summaries, func(s moduleSummary) string { return s.Stage })
		semantic := combinedFingerprint("semantic", ownSemantic, semanticDeps)
		abi := combinedFingerprint("abi", ownABI, abiDeps)
		if valid && semanticOK && abiOK && stageOK && object.Semantic == candidate.Semantic && object.Semantic == semantic && object.ABI == candidate.ABI && object.ABI == abi && object.StageImplementation == ownStage {
			object.StageFingerprint = combinedFingerprint("stage", ownStage, stageDeps)
			return object, true
		}
	}
	return nil, false
}

func publishCachedObject(cache ObjectCache, base string, object *ModuleObject, summaries map[string]moduleSummary) {
	candidate, ok := makeCandidate(base, object, summaries)
	if !ok {
		return
	}
	objectData, err := EncodeObject(object)
	if err != nil {
		return
	}
	candidateData, err := encodeCandidate(candidate)
	if err != nil {
		return
	}
	cache.StoreObject(candidate.ObjectKey, objectData)
	cache.StoreCandidate(base, candidateData)
}
