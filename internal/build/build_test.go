package build

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/waj/fango/internal/codegen"
)

func TestSyncGeneratedPrunesOnlyManagedSources(t *testing.T) {
	dir := t.TempDir()
	userFile := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(userFile, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	first := []codegen.File{
		{Path: "entries/Main/main.go", Data: []byte("package main\n")},
		{Path: "modules/A/module.go", Data: []byte("package fangomod\n")},
	}
	if changed, err := SyncGenerated(dir, "Main", first); err != nil || !changed {
		t.Fatalf("first sync changed=%v err=%v", changed, err)
	}
	if changed, err := SyncGenerated(dir, "Main", first); err != nil || changed {
		t.Fatalf("unchanged sync changed=%v err=%v", changed, err)
	}
	if changed, err := SyncGenerated(dir, "Main", first[:1]); err != nil || !changed {
		t.Fatalf("pruning sync changed=%v err=%v", changed, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "modules", "A", "module.go")); !os.IsNotExist(err) {
		t.Fatalf("stale module remains: %v", err)
	}
	if data, err := os.ReadFile(userFile); err != nil || string(data) != "keep" {
		t.Fatalf("user file changed: %q, %v", data, err)
	}
}

// A runtime source the library root stops shipping must leave the build
// directory: unlike a stale modules/ package, which `go build .` never reaches,
// a leftover fangort file stays in the compiled package.
func TestSyncGeneratedPrunesDroppedRuntimeSources(t *testing.T) {
	dir := t.TempDir()
	first := []codegen.File{
		{Path: "entries/Main/main.go", Data: []byte("package main\n")},
		{Path: "go.mod", Data: []byte(goModContent)},
		{Path: "fangort/kept.go", Data: []byte("package fangort\n")},
		{Path: "fangort/dropped.go", Data: []byte("package fangort\n")},
	}
	if changed, err := SyncGenerated(dir, "Main", first); err != nil || !changed {
		t.Fatalf("first sync changed=%v err=%v", changed, err)
	}
	if changed, err := SyncGenerated(dir, "Main", first[:3]); err != nil || !changed {
		t.Fatalf("pruning sync changed=%v err=%v", changed, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "fangort", "dropped.go")); !os.IsNotExist(err) {
		t.Fatalf("stale runtime source remains: %v", err)
	}
	for _, rel := range []string{"go.mod", filepath.Join("fangort", "kept.go")} {
		if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
			t.Fatalf("%s missing: %v", rel, err)
		}
	}
}

// The runtime is not refcounted the way a program's own packages are. Every
// program compiles it and every build regenerates it in full, so one program's
// build must drop a runtime source even while a sibling's stale list still
// names it — otherwise a dropped fangort file survives in the package every
// generated module imports, and the sibling's own build is what would have to
// remove it.
func TestSyncGeneratedPrunesRuntimeASiblingStillLists(t *testing.T) {
	dir := t.TempDir()
	runtime := []codegen.File{
		{Path: "go.mod", Data: []byte(goModContent)},
		{Path: "fangort/kept.go", Data: []byte("package fangort\n")},
		{Path: "fangort/dropped.go", Data: []byte("package fangort\n")},
	}
	first := append([]codegen.File{{Path: "entries/first/main.go", Data: []byte("package main\n")}}, runtime...)
	second := append([]codegen.File{{Path: "entries/second/main.go", Data: []byte("package main\n")}}, runtime...)
	for _, files := range [][]codegen.File{first, second} {
		if _, err := SyncGenerated(dir, filepath.Base(filepath.Dir(files[0].Path)), files); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := SyncGenerated(dir, "first", first[:3]); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "fangort", "dropped.go")); !os.IsNotExist(err) {
		t.Fatalf("dropped runtime source survived behind a sibling's list: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "entries", "second", "main.go")); err != nil {
		t.Fatalf("sibling's entry package was pruned: %v", err)
	}
}

// The build directory belongs to a source directory, not to one program, so a
// program's sync must leave alone the modules a sibling still imports and the
// sibling's own entry package. Pruning what only it used to generate is still
// its job.
func TestSyncGeneratedKeepsWhatASiblingProgramStillNeeds(t *testing.T) {
	dir := t.TempDir()
	shared := codegen.File{Path: "modules/Shared/module.go", Data: []byte("package fangomod\n")}
	first := []codegen.File{
		{Path: "entries/first/main.go", Data: []byte("package main // first\n")},
		shared,
		{Path: "modules/OnlyFirst/module.go", Data: []byte("package fangomod\n")},
	}
	second := []codegen.File{
		{Path: "entries/second/main.go", Data: []byte("package main // second\n")},
		shared,
	}
	if _, err := SyncGenerated(dir, "first", first); err != nil {
		t.Fatal(err)
	}
	if _, err := SyncGenerated(dir, "second", second); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"entries/first/main.go", "entries/second/main.go",
		"modules/Shared/module.go", "modules/OnlyFirst/module.go"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("%s did not survive the sibling's build: %v", rel, err)
		}
	}
	// Neither program's files moved, so neither sync writes anything.
	if changed, err := SyncGenerated(dir, "first", first); err != nil || changed {
		t.Fatalf("rebuilding the first program changed=%v err=%v", changed, err)
	}
	// Dropping a module the first program alone reached removes it.
	if changed, err := SyncGenerated(dir, "first", first[:2]); err != nil || !changed {
		t.Fatalf("pruning sync changed=%v err=%v", changed, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "modules", "OnlyFirst", "module.go")); !os.IsNotExist(err) {
		t.Fatalf("stale module remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "modules", "Shared", "module.go")); err != nil {
		t.Fatalf("shared module removed: %v", err)
	}
}

// The single-program tree wrote main.go and sources.json at the root and
// recorded them in a flat list. Nothing claims those paths now, so the first
// build after the change removes them.
func TestSyncGeneratedPrunesASingleProgramTree(t *testing.T) {
	dir := t.TempDir()
	legacy := `{"files":["go.mod","main.go","modules/A/module.go","sources.json"]}` + "\n"
	for _, rel := range []string{"main.go", "sources.json"} {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte("stale"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, generatedManifestName), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	files := []codegen.File{
		{Path: "go.mod", Data: []byte(goModContent)},
		{Path: "entries/Main/main.go", Data: []byte("package main\n")},
		{Path: "modules/A/module.go", Data: []byte("package fangomod\n")},
	}
	if _, err := SyncGenerated(dir, "Main", files); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"main.go", "sources.json"} {
		if _, err := os.Stat(filepath.Join(dir, rel)); !os.IsNotExist(err) {
			t.Fatalf("single-program %s remains: %v", rel, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "modules", "A", "module.go")); err != nil {
		t.Fatalf("module still in use was pruned: %v", err)
	}
}

// A program's link cannot be decided from whether its own sync wrote
// anything: a sibling sharing a module can leave its binary stale while it
// writes nothing itself.
func TestLinkedFollowsTheCompiledInputsNotTheWrites(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := []codegen.File{
		{Path: "go.mod", Data: []byte(goModContent)},
		{Path: "entries/Main/main.go", Data: []byte("package main\n")},
		{Path: "modules/Shared/module.go", Data: []byte("package fangomod\n")},
		{Path: "entries/Main/sources.json", Data: []byte(`[{"module":"Main"}]`)},
	}
	if Linked(dir, "Main", files) {
		t.Fatal("reported linked with no binary")
	}
	if err := os.WriteFile(BinaryPath(dir, "Main"), []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if Linked(dir, "Main", files) {
		t.Fatal("reported linked with no stamp")
	}
	RecordLink(dir, "Main", files)
	if !Linked(dir, "Main", files) {
		t.Fatal("reported unlinked immediately after linking")
	}

	// What another program's build can do: replace a shared module in place.
	shared := append([]codegen.File(nil), files...)
	shared[2].Data = []byte("package fangomod // rebuilt by a sibling\n")
	if Linked(dir, "Main", shared) {
		t.Fatal("a shared module changed under the binary and it still reported linked")
	}

	// What recording the build must not do: force a link because the record of
	// what it was made from moved. sources.json is not compiled.
	recorded := append([]codegen.File(nil), files...)
	recorded[3].Data = []byte(`[{"module":"Main","sha256":"different"}]`)
	if !Linked(dir, "Main", recorded) {
		t.Fatal("a change to sources.json alone forced a link")
	}
}

func TestSyncGeneratedRejectsUnsafePaths(t *testing.T) {
	if _, err := SyncGenerated(t.TempDir(), "Main", []codegen.File{{Path: "../outside.go", Data: []byte("x")}}); err == nil {
		t.Fatal("unsafe generated path accepted")
	}
}

func TestValidateExportDirRejectsUnmanagedContent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "keep"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ValidateExportDir(dir); err == nil {
		t.Fatal("non-empty unmanaged directory accepted")
	}
}

func TestCacheFallbackIsSourceRootScopedAndCleaned(t *testing.T) {
	old := userCacheDir
	cacheBase := t.TempDir()
	userCacheDir = func() (string, error) { return cacheBase, nil }
	t.Cleanup(func() { userCacheDir = old })

	root := t.TempDir()
	first := filepath.Join(root, "Main.fango")
	second := filepath.Join(root, "Tool.fango")
	local, fallback, err := CacheDirs(first)
	if err != nil {
		t.Fatal(err)
	}
	_, secondFallback, err := CacheDirs(second)
	if err != nil {
		t.Fatal(err)
	}
	if fallback != secondFallback {
		t.Fatalf("same source root got distinct fallbacks: %q and %q", fallback, secondFallback)
	}
	legacy, err := LegacyCacheDir(first)
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{filepath.Dir(local), fallback, filepath.Dir(legacy)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "artifact"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := Clean(first); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{filepath.Dir(local), fallback, filepath.Dir(legacy)} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("clean left %q: %v", dir, err)
		}
	}
}

func TestBuildDirOverrideDoesNotRedirectCompilationCache(t *testing.T) {
	old := userCacheDir
	userCacheDir = func() (string, error) { return t.TempDir(), nil }
	t.Cleanup(func() { userCacheDir = old })
	root := t.TempDir()
	entry := filepath.Join(root, "Main.fango")
	t.Setenv("FANGO_BUILD_DIR", filepath.Join(t.TempDir(), "generated"))
	local, _, err := CacheDirs(entry)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, ".fango", "cache")
	if local != want {
		t.Fatalf("cache dir = %q, want %q", local, want)
	}
}
