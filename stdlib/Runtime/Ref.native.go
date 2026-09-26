package native

// Payloads are opaque typed runtime tokens, never inspected by the sidecar.
// References are task-local and cannot cross a task's transfer boundary.
type reference struct{ value any }

func New(value any) any       { return &reference{value: value} }
func Read(handle any) any     { return handle.(*reference).value }
func Write(handle, value any) { handle.(*reference).value = value }
