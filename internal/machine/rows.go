package machine

import (
	"slices"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

func sortedRows(rows map[types.CaptureVar]bool) []types.CaptureVar {
	var result []types.CaptureVar
	for row := range rows {
		result = append(result, row)
	}
	slices.Sort(result)
	return result
}

func workerRows(def *core.Def) []types.CaptureVar {
	rows := core.FreeRows(def.Body)
	if def.RowParam != 0 {
		rows[def.RowParam] = true
	}
	return sortedRows(rows)
}
