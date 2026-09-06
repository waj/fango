package ast

import "strings"

func dumpPreds(ps []PredExpr) string {
	if len(ps) == 0 {
		return ""
	}
	var parts []string
	for _, p := range ps {
		parts = append(parts, p.Class+" "+DumpTypeExpr(p.Ty))
	}
	return "(" + strings.Join(parts, ", ") + ") => "
}
