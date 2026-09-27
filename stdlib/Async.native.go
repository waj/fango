package native

func NewRoot() any                  { return FangoNewAsyncRoot(FangoHost.ExecutionContext()) }
func ChildScope(parent any) any     { return FangoNewAsyncScope(parent.(*FangoAsyncScope)) }
func ScopeContext(owner any) any    { return owner }
func ScopeCancelled(owner any) bool { return owner.(*FangoAsyncScope).Cancelled() }
func CancelOwner(owner any)         { owner.(*FangoAsyncScope).Cancel() }
func FinishScope(owner any, status int64) {
	owner.(*FangoAsyncScope).Finish(FangoAsyncCompletion{Failed: status == 1, Cancelled: status == 2})
}
func asyncStatus(result FangoAsyncCompletion) int64 {
	if result.Cancelled {
		return 2
	}
	if result.Failed {
		return 1
	}
	return 0
}
func ScopeStatus(owner any) int64 { return asyncStatus(owner.(*FangoAsyncScope).Completion()) }
func ScopeFailure(owner any) any  { return owner.(*FangoAsyncScope).Completion().Failure }
func NewCompleted(value any) any  { return FangoNewAsyncValue(value) }
func CompleteSuccess(context, task any) {
	FangoPublishAsyncValue(context.(*FangoAsyncScope), task.(*FangoAsyncTask), nil, false)
}
func CompleteFailure(owner, task, failure any) {
	FangoPublishAsyncValue(owner.(*FangoAsyncScope), task.(*FangoAsyncTask), failure, true)
}
func CancelTask(task any) { task.(*FangoAsyncTask).Cancel() }
func WaitTask(task any)   { task.(*FangoAsyncTask).Wait() }
func WaitTaskIn(context, task any) bool {
	owner := context.(*FangoAsyncScope)
	_, delivered := task.(*FangoAsyncTask).AwaitObserved(owner)
	return delivered
}
func TaskStatus(task any) int64 { return asyncStatus(task.(*FangoAsyncTask).Result()) }
func TaskValue(task any) any    { return task.(*FangoAsyncTask).Result().Value }
func SleepIn(context any, milliseconds int64) bool {
	return FangoAsyncSleep(context.(*FangoAsyncScope), milliseconds)
}

type asyncChannel struct{ channel *FangoAsyncChannel }
type asyncRead struct {
	value  any
	status int64
}

func NewChannel() any { return &asyncChannel{} }
func SetCapacity(channel any, capacity int64) {
	channel.(*asyncChannel).channel = FangoNewAsyncChannel(int(capacity))
}
func SendIn(context, channel, value any) bool {
	return channel.(*asyncChannel).channel.Send(context.(*FangoAsyncScope).Context(), value) == 0
}
func ReceiveIn(context, channel any) any {
	value, status := channel.(*asyncChannel).channel.Receive(context.(*FangoAsyncScope).Context())
	return &asyncRead{value: value, status: int64(status)}
}
func ReadStatus(result any) int64 { return result.(*asyncRead).status }
func ReadValue(result any) any    { return result.(*asyncRead).value }
func CloseChannel(channel any)    { channel.(*asyncChannel).channel.Close() }
