package check

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"

	"github.com/waj/fango/internal/compilecache"
	"github.com/waj/fango/internal/modules"
	"github.com/waj/fango/internal/source"
)

// ObjectCache is the byte-storage seam for checked module objects. A slot
// holds one module's current artifact and a store replaces it; deciding
// whether that artifact is still the right one belongs here, not to the store.
// An installer stores objects in the background while it checks later
// modules, so a cache must accept calls from several goroutines at once.
type ObjectCache interface {
	LoadObject(slot string) ([]byte, bool)
	StoreObject(slot string, data []byte)
}

type stageInput struct {
	Module      string `json:"module"`
	Fingerprint string `json:"fingerprint"`
}

// validityRecord is what a stored object says it was built from. Its base key
// names the module's own source and its dependencies' contracts; its stage
// dependencies name the modules the prior check reached at compile time, which
// only the artifact itself can report.
type validityRecord struct {
	BaseKey           string       `json:"base_key"`
	Module            string       `json:"module"`
	StageDependencies []stageInput `json:"stage_dependencies"`
}

// objectSlot names the one artifact this module keeps. The entry answers to
// its sidecar name, which is its file stem, because a headerless entry has no
// module name of its own and would otherwise collide with every other one.
func objectSlot(module modules.ResolvedModule) (string, bool) {
	if module.Role == modules.EntryRole {
		return compilecache.Slot(true, module.NativeModule), module.NativeModule != ""
	}
	return compilecache.Slot(false, module.Name), module.Name != ""
}

func moduleBaseKey(module modules.ResolvedModule, fixityHash string, summaries map[string]moduleSummary) (string, bool) {
	parts := []string{"checked-module-base", module.Name, module.SourceHash, module.NativeHash, fixityHash, fmt.Sprint(module.Role), module.Entry}
	for _, dep := range module.Dependencies {
		summary, ok := summaries[dep]
		if !ok {
			return "", false
		}
		// Inlining copies a dependency's candidate bodies into this module's
		// Core, so a change to them must miss even when its interface holds.
		parts = append(parts, dep, summary.Semantic, summary.Unfolding)
	}
	return digest(parts...), true
}

type moduleSummary struct {
	Semantic  string
	ABI       string
	Stage     string
	Unfolding string
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

func makeRecord(base string, object *ModuleObject, summaries map[string]moduleSummary) (validityRecord, bool) {
	names := append([]string(nil), object.CheckStageDependencies...)
	sort.Strings(names)
	inputs := make([]stageInput, 0, len(names))
	for _, name := range names {
		summary, ok := summaries[name]
		if !ok {
			return validityRecord{}, false
		}
		inputs = append(inputs, stageInput{Module: name, Fingerprint: summary.Stage})
	}
	return validityRecord{BaseKey: base, Module: object.State.Name, StageDependencies: inputs}, true
}

func decodeRecord(data []byte, base, module string, summaries map[string]moduleSummary) (validityRecord, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var record validityRecord
	if err := dec.Decode(&record); err != nil {
		return validityRecord{}, err
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return validityRecord{}, fmt.Errorf("trailing validity record data")
	}
	if record.BaseKey != base || record.Module != module {
		return validityRecord{}, fmt.Errorf("superseded checked module")
	}
	last := ""
	for _, input := range record.StageDependencies {
		if input.Module <= last || summaries[input.Module].Stage == "" || summaries[input.Module].Stage != input.Fingerprint {
			return validityRecord{}, fmt.Errorf("stale stage dependency %q", input.Module)
		}
		last = input.Module
	}
	return record, nil
}

// cachedObject is a slot's artifact read and decoded, not yet accepted: its
// record still has to match the graph, which needs the dependencies' summaries.
// read is the artifact's size whether or not it proves usable, so a miss can
// be told apart from a cache that was never consulted.
type cachedObject struct {
	read   int
	record []byte
	object *ModuleObject
}

// readCachedObject reads and decodes a slot. It depends on nothing the graph
// computes, so an installer reads every slot it will consult concurrently,
// before checking the first module.
func readCachedObject(cache ObjectCache, slot string, sources map[string]*source.File) cachedObject {
	data, ok := cache.LoadObject(slot)
	if !ok {
		return cachedObject{}
	}
	read := len(data)
	recordData, sections, err := SplitObject(data)
	if err != nil {
		return cachedObject{read: read}
	}
	object, err := DecodeObjectSections(sections, sources)
	if err != nil {
		return cachedObject{read: read}
	}
	return cachedObject{read: read, record: recordData, object: object}
}

// acceptCachedObject decides whether a decoded artifact is this module as the
// graph now stands.
func acceptCachedObject(cached cachedObject, base string, module modules.ResolvedModule, summaries map[string]moduleSummary) (*ModuleObject, bool) {
	object := cached.object
	if object == nil {
		return nil, false
	}
	record, err := decodeRecord(cached.record, base, module.Name, summaries)
	if err != nil {
		return nil, false
	}
	if object.State == nil || object.State.Name != module.Name {
		return nil, false
	}
	if len(object.CheckStageDependencies) != len(record.StageDependencies) {
		return nil, false
	}
	for i, input := range record.StageDependencies {
		if object.CheckStageDependencies[i] != input.Module {
			return nil, false
		}
	}
	// The frame verifies the object bytes, including the own fingerprints
	// computed when it was published. Recomputing them over the decoded Core
	// on every hit costs more than decoding the object itself.
	ownSemantic, ownABI := object.OwnSemantic, object.OwnABI
	// The stage fingerprint is carried by the frame like the others; stage
	// Core is a deferred section that a hit does not read.
	ownStage := object.StageImplementation
	semanticDeps, semanticOK := dependencyFingerprints(module.Dependencies, summaries, func(s moduleSummary) string { return s.Semantic })
	abiDeps, abiOK := dependencyFingerprints(module.Dependencies, summaries, func(s moduleSummary) string { return s.ABI })
	stageDeps, stageOK := dependencyFingerprints(object.StageDependencies, summaries, func(s moduleSummary) string { return s.Stage })
	unfoldingDeps, unfoldingOK := dependencyFingerprints(module.Dependencies, summaries, func(s moduleSummary) string { return s.Unfolding })
	if !semanticOK || !abiOK || !stageOK || !unfoldingOK || ownSemantic == "" || ownABI == "" || ownStage == "" || object.OwnUnfolding == "" || object.Implementation == "" {
		return nil, false
	}
	object.Unfolding = combinedFingerprint("unfolding", object.OwnUnfolding, unfoldingDeps)
	if object.Semantic != combinedFingerprint("semantic", ownSemantic, semanticDeps) ||
		object.ABI != combinedFingerprint("abi", ownABI, abiDeps) {
		return nil, false
	}
	object.StageFingerprint = combinedFingerprint("stage", ownStage, stageDeps)
	return object, true
}

// encodeRecord is the part of publishing that reads the graph's summaries,
// which later modules extend; the rest touches only the object.
func encodeRecord(base string, object *ModuleObject, summaries map[string]moduleSummary) ([]byte, bool) {
	record, ok := makeRecord(base, object, summaries)
	if !ok {
		return nil, false
	}
	recordData, err := json.Marshal(record)
	if err != nil {
		return nil, false
	}
	return recordData, true
}

// storeCachedObject reports the bytes it wrote, or zero when the object could
// not be published. A failure to write stays silent: it is an optimization
// declining, not a diagnostic.
func storeCachedObject(cache ObjectCache, slot string, object *ModuleObject, recordData []byte) int {
	data, err := EncodeObject(object, recordData)
	if err != nil {
		return 0
	}
	cache.StoreObject(slot, data)
	return len(data)
}
