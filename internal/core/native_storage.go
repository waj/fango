package core

import "github.com/waj/fango/internal/types"

func (l *linter) nativeStorageWrapper(wrapper *types.CtorInfo) bool {
	if wrapper == nil || wrapper.Result == nil {
		return false
	}
	adt := l.adts[wrapper.Result.Unique]
	if adt == nil || !adt.Resource || !adt.NativeIndexed || len(adt.Params) != 1 || adt.IsRecord() || len(adt.Ctors) != 1 || len(adt.Ctors[0].Fields) != 1 || len(wrapper.Fields) != 1 {
		return false
	}
	field, ok := adt.Ctors[0].Fields[0].(*types.TCon)
	return ok && len(field.Args) == 0 && field.Name == "Runtime.Native.Any" && l.adts[field.Unique] != nil && l.adts[field.Unique].Repr == types.ReprNativeAny && types.Equal(field, wrapper.Fields[0]) && wrapper.Name == adt.Ctors[0].Name
}
