// Package llvmbuild owns the experimental LLVM artifact tree and toolchain.
package llvmbuild

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/waj/fango/internal/build"
	"github.com/waj/fango/internal/check"
	"github.com/waj/fango/internal/libroot"
	"github.com/waj/fango/internal/llvmgen"
)

func command(dir, name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	data, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("LLVM toolchain: %s failed: %w\n%s", name, err, data)
	}
	return data, nil
}
func toolchain() (flags []string, identity []byte, err error) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		return nil, nil, fmt.Errorf("UNSUPPORTED LLVM TARGET: this experiment currently supports macOS ARM64")
	}
	for _, tool := range []string{"clang", "clang++", "llvm-link", "llvm-nm", "opt", "pkg-config"} {
		if _, e := exec.LookPath(tool); e != nil {
			return nil, nil, fmt.Errorf("LLVM toolchain: missing %s; enter the Nix development shell", tool)
		}
	}
	output, e := command("", "pkg-config", "--cflags", "--libs", "bdw-gc", "zlib")
	if e != nil {
		return nil, nil, e
	}
	flags = strings.Fields(string(output))
	version, e := command("", "clang", "--version")
	if e != nil {
		return nil, nil, e
	}
	identity = append(version, output...)
	for _, tool := range []string{"clang++", "llvm-link", "opt", "llvm-nm"} {
		version, e := command("", tool, "--version")
		if e != nil {
			return nil, nil, e
		}
		identity = append(identity, version...)
	}
	return flags, identity, nil
}

// Build returns a cached native binary. All LLVM output is beneath its own
// subtree, including when FANGO_BUILD_DIR redirects the surrounding build root.
func Build(entry string, result *check.Result, printMain bool) (string, error) {
	return BuildWithOptions(entry, result, printMain, false)
}

func BuildWithOptions(entry string, result *check.Result, printMain, noCache bool) (string, error) {
	flags, identity, err := toolchain()
	if err != nil {
		return "", err
	}
	source, natives, err := llvmgen.Emit(result.Program, result.Checker.B, printMain)
	if err != nil {
		return "", err
	}
	base, err := build.Dir(entry)
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(entry)
	if err != nil {
		return "", err
	}
	id := sha256.Sum256([]byte(abs))
	dir := filepath.Join(base, "llvm", "entries", hex.EncodeToString(id[:8]))
	files := []llvmgen.File{{Path: "program.cpp", Data: source}}
	files = append(files, llvmgen.NativeHeaders(result.Program, natives)...)
	root, err := libroot.Root()
	if err != nil {
		return "", err
	}
	for _, name := range []string{"fango.h", "fango.hpp", "fango.cpp"} {
		data, e := os.ReadFile(filepath.Join(root, "runtime", "llvm", name))
		if e != nil {
			return "", e
		}
		files = append(files, llvmgen.File{Path: name, Data: data})
	}
	var cSources []string
	for owner := range natives {
		var path string
		for _, native := range result.Graph.Natives {
			if native.Module == owner {
				path = native.Path
				break
			}
		}
		if path == "" {
			return "", fmt.Errorf("MISSING C SIDECAR: native module %s has no sidecar identity", owner)
		}
		if strings.HasSuffix(path, ".native.go") {
			path = strings.TrimSuffix(path, ".native.go") + ".native.c"
		}
		var full string
		if strings.HasPrefix(path, "<stdlib>/") {
			full = filepath.Join(root, "stdlib", filepath.FromSlash(strings.TrimPrefix(path, "<stdlib>/")))
		} else {
			full = filepath.Join(filepath.Dir(abs), filepath.FromSlash(path))
		}
		data, e := os.ReadFile(full)
		if e != nil {
			return "", fmt.Errorf("MISSING C SIDECAR: LLVM native module %s requires %s: %w", owner, full, e)
		}
		headerDir := filepath.Dir(llvmgen.NativeHeaderPath(owner))
		path = filepath.ToSlash(filepath.Join(headerDir, "native.c"))
		files = append(files, llvmgen.File{Path: path, Data: data})
		cSources = append(cSources, path)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	sort.Strings(cSources)
	h := sha256.New()
	h.Write(identity)
	h.Write([]byte("fango-llvm-v1 C++20 LLVM-O3\n"))
	for _, f := range files {
		fmt.Fprintf(h, "%s %d\n", f.Path, len(f.Data))
		h.Write(f.Data)
	}
	stamp := []byte(hex.EncodeToString(h.Sum(nil)))
	binary := filepath.Join(dir, "program")
	old, _ := os.ReadFile(filepath.Join(dir, "linked"))
	if !noCache && string(old) == string(stamp) {
		intact := true
		for _, f := range files {
			actual, e := os.ReadFile(filepath.Join(dir, f.Path))
			if e != nil || string(actual) != string(f.Data) {
				intact = false
				break
			}
		}
		data, e := os.ReadFile(binary)
		digest := sha256.Sum256(data)
		record, _ := os.ReadFile(filepath.Join(dir, "binary.sha256"))
		intact = intact && e == nil && string(record) == hex.EncodeToString(digest[:])
		if info, e := os.Stat(binary); intact && e == nil && info.Mode()&0111 != 0 {
			return binary, nil
		}
	}
	for _, f := range files {
		if _, e := build.WriteIfChanged(filepath.Join(dir, filepath.FromSlash(f.Path)), f.Data); e != nil {
			return "", e
		}
	}
	includeFlags := []string{}
	linkFlags := []string{}
	for _, flag := range flags {
		if strings.HasPrefix(flag, "-I") || strings.HasPrefix(flag, "-D") {
			includeFlags = append(includeFlags, flag)
		} else {
			linkFlags = append(linkFlags, flag)
		}
	}
	compile := func(compiler, source, out string, extra ...string) error {
		args := []string{"-O3", "-fno-fast-math", "-S", "-emit-llvm", "-I.", "-o", out, source}
		args = append(args, extra...)
		args = append(args, includeFlags...)
		_, e := command(dir, compiler, args...)
		return e
	}
	if err := compile("clang++", "program.cpp", "program.ll", "-std=c++20"); err != nil {
		return "", err
	}
	if err := compile("clang++", "fango.cpp", "runtime.ll", "-std=c++20"); err != nil {
		return "", err
	}
	irs := []string{"program.ll", "runtime.ll"}
	for i, source := range cSources {
		out := fmt.Sprintf("native%d.ll", i)
		if err := compile("clang", source, out, "-std=c11", "-include", filepath.ToSlash(filepath.Join(filepath.Dir(source), "fango_native.h")), "-I"+filepath.Dir(source)); err != nil {
			return "", err
		}
		irs = append(irs, out)
	}
	args := append(append([]string{}, irs...), "-o", "linked.bc")
	if _, err := command(dir, "llvm-link", args...); err != nil {
		return "", err
	}
	if _, err := command(dir, "opt", "-passes=verify", "-disable-output", "linked.bc"); err != nil {
		return "", err
	}
	if _, err := command(dir, "opt", "-passes=default<O3>,verify", "linked.bc", "-o", "optimized.bc"); err != nil {
		return "", err
	}
	args = []string{"-O3", "optimized.bc", "-o", "program.next"}
	args = append(args, linkFlags...)
	if _, err := command(dir, "clang++", args...); err != nil {
		return "", err
	}
	if err := os.Rename(filepath.Join(dir, "program.next"), binary); err != nil {
		return "", err
	}
	if noCache {
		return binary, nil
	}
	data, err := os.ReadFile(binary)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	if err := os.WriteFile(filepath.Join(dir, "binary.sha256"), []byte(hex.EncodeToString(digest[:])), 0600); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "linked"), stamp, 0600); err != nil {
		return "", err
	}
	return binary, nil
}
