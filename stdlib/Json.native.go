package native

import "github.com/waj/fango/runtime/fangort"

func JSONString(s string) string { return fangort.JSONString(s) }

func JSONFloat(v float64) string { return fangort.JSONFloat(v) }

func JSONStringTokenLength(s string) int64 { return fangort.JSONStringTokenLength(s) }

func JSONStringValue(s string) string { return fangort.JSONStringValue(s) }
