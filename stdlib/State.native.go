package native

// The atomic State runner's cell. Values are opaque payloads; a version is the
// token a later compare-and-set names.

func NewAtomicCell(initial any) any { return FangoNewAtomicCell(initial) }
func LoadAtomic(cell any) any       { return cell.(*FangoAtomicCell).Load() }
func AtomicValue(version any) any   { return version.(*FangoAtomicVersion).Value }
func ReplaceAtomic(version, value any) bool {
	return version.(*FangoAtomicVersion).Replace(value)
}
func StoreAtomic(cell, value any) { cell.(*FangoAtomicCell).Store(value) }
