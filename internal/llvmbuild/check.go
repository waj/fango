package llvmbuild

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/waj/fango/internal/build"
	"github.com/waj/fango/internal/check"
	"github.com/waj/fango/internal/libroot"
	"github.com/waj/fango/internal/llvmgen"
	"github.com/waj/fango/internal/types"
)

func sidecarPath(entry, path, root string) string {
	if strings.HasPrefix(path, "<stdlib>/") {
		return filepath.Join(root, "stdlib", filepath.FromSlash(strings.TrimPrefix(path, "<stdlib>/")))
	}
	return filepath.Join(filepath.Dir(entry), filepath.FromSlash(path))
}

// Check validates reachable LLVM features and every C sidecar's typed ABI,
// including definitions that an optimized executable could discard.
func Check(entry string, result *check.Result) error {
	flags, _, err := toolchain()
	if err != nil {
		return err
	}
	for _, d := range result.Program.Defs {
		if d.Name == result.Program.Entry || (result.Program.Entry == "" && d.Name == "main") {
			if _, _, err := llvmgen.Emit(result.Program, result.Checker.B, false); err != nil {
				return err
			}
			break
		}
	}
	root, err := libroot.Root()
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(entry)
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "fango-llvm-check-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	data, err := os.ReadFile(filepath.Join(root, "runtime", "llvm", "fango.h"))
	if err != nil {
		return err
	}
	if _, err := build.WriteIfChanged(filepath.Join(dir, "fango.h"), data); err != nil {
		return err
	}
	owners := map[string]bool{}
	for _, native := range result.Graph.Natives {
		owners[native.Module] = true
	}
	headers := llvmgen.NativeHeaders(result.Program, owners)
	for _, header := range headers {
		if _, err := build.WriteIfChanged(filepath.Join(dir, header.Path), header.Data); err != nil {
			return err
		}
	}
	for _, native := range result.Graph.Natives {
		header := llvmgen.NativeHeaderPath(native.Module)
		source := sidecarPath(abs, native.Path, root)
		args := []string{"-std=c11", "-emit-llvm", "-c", source, "-o", "native.bc", "-I.", "-I" + filepath.Dir(header), "-include", header}
		for _, flag := range flags {
			if strings.HasPrefix(flag, "-I") || strings.HasPrefix(flag, "-D") {
				args = append(args, flag)
			}
		}
		if _, err := command(dir, "clang", args...); err != nil {
			return err
		}
		symbols, err := command(dir, "llvm-nm", "--defined-only", "--extern-only", "native.bc")
		if err != nil {
			return err
		}
		defined := map[string]bool{}
		for _, line := range strings.Split(string(symbols), "\n") {
			parts := strings.Fields(line)
			if len(parts) > 0 {
				defined[parts[len(parts)-1]] = true
			}
		}
		var missing []string
		for _, n := range result.Program.Natives {
			if n.Template == nil && n.Module == native.Module {
				symbol := llvmgen.NativeSymbol(n.Module, types.SurfaceName(n.Name))
				if !defined[symbol] && !defined["_"+symbol] {
					missing = append(missing, types.SurfaceName(n.Name))
				}
			}
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			return fmt.Errorf("MISSING C NATIVE FUNCTION: %s lacks %s", native.Path, strings.Join(missing, ", "))
		}
	}
	return nil
}
