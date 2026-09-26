package elaborate

import (
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

func sourceLambdas(expr core.Expr, sourceType types.Type, count int) core.Expr {
	root := expr
	for range count {
		lambda, ok := expr.(*core.Lambda)
		if !ok {
			return root
		}
		arrow, ok := sourceType.(*types.TFun)
		if !ok {
			return root
		}
		lambda.SourceType = sourceType
		expr, sourceType = lambda.Body, arrow.Ret
	}
	return root
}
