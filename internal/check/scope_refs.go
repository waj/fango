package check

import (
	"fmt"
	"reflect"
	"sort"

	"github.com/waj/fango/internal/types"
)

// Imported result summaries may retain durable handler scopes introduced by
// another module. Their identity belongs to that defining contract; assigning
// a fresh local identity on cache installation would split one activation.
func captureScopeNames(summaries map[string]types.CaptureSummary) map[types.ScopeID]string {
	out := map[types.ScopeID]string{}
	names := make([]string, 0, len(summaries))
	for name := range summaries {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		for i, id := range summaries[name].Captures.Scopes {
			if _, known := out[id]; !known {
				out[id] = fmt.Sprintf("%s#%d", name, i)
			}
		}
	}
	return out
}

func foreignScopeNames(all, own map[string]types.CaptureSummary, referenced any) map[types.ScopeID]string {
	allNames := captureScopeNames(all)
	for id := range captureScopeNames(own) {
		delete(allNames, id)
	}
	ids := &remapIDs{permissions: map[int]bool{}, vars: map[int]*types.TVar{}, captures: map[types.CaptureVar]bool{}, scopes: map[types.ScopeID]bool{}, resumes: map[types.ResumeID]bool{}}
	collectRemapIDs(reflect.ValueOf(referenced), map[uintptr]bool{}, ids)
	for id := range allNames {
		if !ids.scopes[id] {
			delete(allNames, id)
		}
	}
	return allNames
}
