package repl

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/waj/fango/internal/check"
	"github.com/waj/fango/internal/infer"
	"github.com/waj/fango/internal/types"
)

// Preserve declarations, not source inputs: replaying an accepted prompt
// would change its dependencies, and could repeat an effectful initializer.
func (s *Session) preserveDeclarations(next *infer.Checker) *infer.ModuleState {
	locals := s.ck.PromptDeclarations(s.modules.States())
	moduleTypes, moduleClasses, moduleEffects := map[int]bool{}, map[string]bool{}, map[string]bool{}
	for _, state := range s.modules.States() {
		for _, adt := range state.ADTs {
			moduleTypes[adt.Con.Unique] = true
		}
		for name := range state.Classes {
			moduleClasses[name] = true
		}
		for name := range state.Effects {
			moduleEffects[name] = true
		}
	}
	// Removed modules may still be called by a retained closure. Their
	// sidecars remain in the unchanged worker, without source-visible names.
	for name, native := range s.ck.Natives {
		if next.Natives[name] == nil {
			locals.Natives[name] = native
		}
	}
	// Concrete evidence retained by a historical instance names its original
	// dictionary and methods. Elaboration needs their signatures as well as
	// the runtime definitions, but these names never enter the resolver.
	for name, runtime := range s.runtimeNames {
		if scheme, ok := s.ck.Env.Lookup(name); ok {
			locals.Schemes[runtime] = scheme
		}
		if arity, ok := s.ck.Workers[name]; ok {
			locals.Workers[runtime] = arity
		}
		if capture, ok := s.ck.CaptureSummaries[name]; ok {
			locals.Captures[runtime] = capture
		}
	}
	classes := map[string]string{}
	for name, cl := range s.ck.Classes {
		if !moduleClasses[name] {
			continue
		}
		current := next.Classes[name]
		if current == nil || current.Dict.Con.Unique != cl.Dict.Con.Unique {
			retained := name
			if !strings.Contains(name, "$repl") {
				retained = generationName(name, s.reloadGeneration)
			}
			classes[name] = retained
			locals.Classes[name] = cl
		}
	}
	typeNames, effectNames := map[int]string{}, map[int]string{}
	for _, adt := range s.ck.ADTOrder {
		if moduleTypes[adt.Con.Unique] && next.ADTs[adt.Con.Unique] == nil && !strings.Contains(adt.Con.Name, " [before reload ") {
			typeNames[adt.Con.Unique] = fmt.Sprintf("%s [before reload %d]", adt.Con.Name, s.reloadGeneration+1)
		}
	}
	for name, effect := range s.ck.Effects {
		if !moduleEffects[name] {
			continue
		}
		if next.EffectsByUnique[effect.Unique] == nil {
			retained := name
			if !strings.Contains(name, "$repl") {
				retained = generationName(name, s.reloadGeneration)
			}
			locals.Effects[retained] = effect
			effectNames[effect.Unique] = fmt.Sprintf("%s [before reload %d]", effect.Name, s.reloadGeneration+1)
		}
	}
	// Old instances are useful only for retired nominal types/classes. An
	// instance over unchanged types is replaced by the current module's one;
	// already elaborated calls retain their old concrete dictionary instead.
	for _, in := range s.ck.Instances {
		if in.Owner == "" || strings.Contains(in.Owner, "$repl") {
			continue
		}
		retired := classes[in.Class.Name] != ""
		check.CopyGraph(in.Head, func(v reflect.Value) {
			if v.Type() == reflect.TypeFor[types.TCon]() {
				t := v.Interface().(types.TCon)
				if s.ck.ADTs[t.Unique] != nil && next.ADTs[t.Unique] == nil {
					retired = true
				}
			}
		})
		if retired {
			copyInstance := *in
			copyInstance.Owner = generationName(in.Owner, s.reloadGeneration)
			if name := s.runtimeNames[in.Name]; name != "" {
				copyInstance.Name = name
			}
			copyInstance.Methods = append([]string(nil), in.Methods...)
			for i, method := range copyInstance.Methods {
				if name := s.runtimeNames[method]; name != "" {
					copyInstance.Methods[i] = name
				}
			}
			locals.Instances = append(locals.Instances, &copyInstance)
		}
	}
	return check.CopyGraph(locals, func(v reflect.Value) {
		if v.Kind() == reflect.String {
			if name := classes[v.String()]; name != "" {
				v.SetString(name)
			}
			return
		}
		switch v.Type() {
		case reflect.TypeFor[types.TCon]():
			if name := typeNames[int(v.FieldByName("Unique").Int())]; name != "" {
				v.FieldByName("Name").SetString(name)
			}
		case reflect.TypeFor[types.EffLabel](), reflect.TypeFor[types.EffectInfo]():
			if name := effectNames[int(v.FieldByName("Unique").Int())]; name != "" {
				v.FieldByName("Name").SetString(name)
			}
		}
	})
}
