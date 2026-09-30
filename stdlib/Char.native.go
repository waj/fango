package native

func ToCode(char rune) int64 { return int64(char) }
func Scalar(code int64) rune { return rune(code) }
