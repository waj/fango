package codegen

import (
	"path"
	"strings"

	"github.com/waj/fango/internal/debugger"
	"github.com/waj/fango/internal/types"
)

// Layout descriptions accompany debug output only. Reuse the actual backend
// decisions and field names instead of reverse-engineering them in the client.
func (g *gen) nativeDebugTypes(unitPath string) map[string]debugger.NativeType {
	result := map[string]debugger.NativeType{}
	pkg := "fangobuild/" + path.Dir(unitPath)
	if strings.HasPrefix(unitPath, "entries/") {
		pkg = "main"
	}
	for _, adt := range g.adts {
		if symbolOwner(adt.Con.Name) != g.unit || adt.Repr != types.ReprADT {
			continue
		}
		layout := debugger.NativeType{Name: types.SurfaceName(adt.Con.Name), Kind: "sum"}
		switch {
		case adt.Resource || adt.NativeIndexed:
			layout.Kind = "opaque"
		case adt.Con.Name == "Tuple.Pair" || adt.Con.Name == "Tuple.Triple":
			layout.Kind = "tuple"
		case adt.IsRecord():
			layout.Kind = "record"
		case taggedADT(adt):
			layout.Kind = "tagged"
		case productADT(adt):
			layout.Kind = "constructor"
		}
		for _, ctor := range adt.Ctors {
			c := debugger.NativeConstructor{Name: types.SurfaceName(ctor.Name), Tag: ctor.Index}
			for i := range ctor.Fields {
				f := debugger.NativeField{Runtime: representationField(adt, ctor, i)}
				if adt.IsRecord() {
					f.Name = adt.RecordFields[i].Name
				}
				c.Fields = append(c.Fields, f)
			}
			layout.Constructors = append(layout.Constructors, c)
			variant := layout
			variant.Constructors = []debugger.NativeConstructor{c}
			if variant.Kind == "sum" {
				variant.Kind = "constructor"
			}
			if variant.Kind == "tagged" {
				continue
			} // aliases resolve to T_ below
			result[pkg+"."+mangleCtor(ctor.Name)] = variant
		}
		result[pkg+"."+mangleType(adt.Con.Name)] = layout
	}
	return result
}
