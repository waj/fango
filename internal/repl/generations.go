package repl

import (
	"fmt"
	"reflect"
	"sort"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/check"
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/infer"
	"github.com/waj/fango/internal/nativehost"
	"github.com/waj/fango/internal/types"
)

// Runtime names belong to a session, never to a persistent module artifact.
// Freeze module references before a prompt definition can capture them.
func generationName(name string, generation uint64) string {
	return fmt.Sprintf("%s$repl%d", name, generation)
}

func renameExpr(e core.Expr, names map[string]string) core.Expr {
	return core.Rewrite(e, func(t types.Type) types.Type { return t }, func(e core.Expr) core.Expr {
		if ref, ok := e.(*core.VarRef); ok && !ref.Local {
			if name := names[ref.Name]; name != "" {
				ref.Name = name
			}
		}
		return e
	})
}

func (s *Session) runtimeExpr(e core.Expr) core.Expr { return renameExpr(e, s.runtimeNames) }

func renameDefs(defs []core.Def, names map[string]string) []core.Def {
	out := append([]core.Def(nil), defs...)
	for i := range out {
		if name := names[out[i].Name]; name != "" {
			out[i].Name = name
		}
		out[i].Body = renameExpr(out[i].Body, names)
	}
	return out
}

func (s *Session) installModules(defs []core.Def) {
	for _, d := range defs {
		s.runtimeNames[d.Name] = generationName(d.Name, s.reloadGeneration)
	}
	runtime := renameDefs(defs, s.runtimeNames)
	s.runtimeDefs = append(s.runtimeDefs, runtime...)
	s.env.DefineProg(&core.Prog{ADTs: s.ck.ADTOrder, Defs: runtime, Natives: s.ck.Natives})
}

// Keep source dependencies as well as Core references: inlining and sealing
// an effectful value may erase the call that explains its retained version.
func (s *Session) recordReferences(info infer.DeclInfo) {
	refs := map[string]bool{}
	check.CopyGraph(info.Body, func(v reflect.Value) {
		switch v.Type() {
		case reflect.TypeFor[ast.Var](), reflect.TypeFor[ast.Ctor](), reflect.TypeFor[ast.RecordLit]():
		default:
			return
		}
		name := v.FieldByName("Name").String()
		if runtime := s.runtimeNames[name]; runtime != "" {
			name = runtime
		}
		refs[name] = true
	})
	var names []string
	for name := range refs {
		names = append(names, name)
	}
	sort.Strings(names)
	s.promptReferences[info.Name] = names
}

func mergeNativeSources(old, added []nativehost.Source) []nativehost.Source {
	byName := map[string]nativehost.Source{}
	for _, source := range old {
		byName[source.Module] = source
	}
	for _, source := range added {
		byName[source.Module] = source
	}
	var names []string
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]nativehost.Source, 0, len(names))
	for _, name := range names {
		out = append(out, byName[name])
	}
	return out
}

func (s *Session) nativeSourcesChanged(added []nativehost.Source) bool {
	old := map[string]string{}
	for _, source := range s.natives {
		old[source.Module] = string(source.Content)
	}
	for _, source := range added {
		if old[source.Module] != string(source.Content) {
			return true
		}
	}
	return false
}
