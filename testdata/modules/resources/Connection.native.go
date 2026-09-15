package native

var nextConnection int64
var connections = map[int64]int64{}

func OpenNative(value int64) int64 {
	nextConnection++
	connections[nextConnection] = value
	return nextConnection
}
func CloseNative(handle int64) {
	if _, ok := connections[handle]; !ok {
		panic("connection released twice")
	}
	delete(connections, handle)
}
func ReadValue(handle int64) int64 {
	value, ok := connections[handle]
	if !ok {
		panic("connection used after release")
	}
	return value
}
func Active() int64 { return int64(len(connections)) }
