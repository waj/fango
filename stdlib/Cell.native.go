package native

import "sync"

// The payload is an opaque runtime token: native code never inspects it or
// invokes stored callables. Readers share readiness, not publication authority.
type completionCell struct {
	mu    sync.Mutex
	ready bool
	value any
}
type completionReader struct{ cell *completionCell }

func CellNew() any              { return &completionCell{} }
func CellReader(handle any) any { return &completionReader{handle.(*completionCell)} }
func CellPublish(handle, value any) bool {
	cell := handle.(*completionCell)
	cell.mu.Lock()
	defer cell.mu.Unlock()
	if cell.ready {
		return false
	}
	cell.value, cell.ready = value, true
	return true
}
func CellReady(handle any) bool {
	cell := handle.(*completionReader).cell
	cell.mu.Lock()
	defer cell.mu.Unlock()
	return cell.ready
}
func CellRead(handle any) any {
	cell := handle.(*completionReader).cell
	cell.mu.Lock()
	defer cell.mu.Unlock()
	if !cell.ready {
		panic("read of empty completion cell")
	}
	return cell.value
}
