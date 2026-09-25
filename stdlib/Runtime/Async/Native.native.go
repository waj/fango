package native

func New(capacity int64) any { return FangoNewEventBridge(capacity) }
func Close(value any)        { value.(*FangoEventBridge).Close() }
func Reserve(value any) int64 {
	return value.(*FangoEventBridge).Reserve()
}
func Available(value any) bool           { return value.(*FangoEventBridge).Available() }
func Ready(value any, ticket int64) bool { return value.(*FangoEventBridge).Ready(ticket) }
func Release(value any, ticket int64) {
	value.(*FangoEventBridge).Release(ticket)
}
func Take(value any) int64      { return value.(*FangoEventBridge).Take() }
func Wait(value any) int64      { return value.(*FangoEventBridge).Wait() }
func TakeDrain(value any) int64 { return value.(*FangoEventBridge).TakeDraining() }
func WaitDrain(value any) int64 { return value.(*FangoEventBridge).WaitDraining() }
