package native

type cell struct{ value any }

func Box(value any) any       { return &cell{value: value} }
func Read(handle any) any     { return handle.(*cell).value }
func Write(handle, value any) { handle.(*cell).value = value }
