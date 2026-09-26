package native

func NewScope() any             { return FangoNewTaskScope() }
func JoinScope(scope any)       { scope.(*FangoTaskScope).Join() }
func CloseScope(scope any)      { scope.(*FangoTaskScope).Close() }
func Cancel(task any)           { task.(*FangoTask).Cancel() }
func Wait(task any)             { task.(*FangoTask).Wait() }
func WaitIn(ctx, task any) bool { return task.(*FangoTask).WaitIn(ctx.(*FangoTaskContext)) }
func Status(task any) int64     { return task.(*FangoTask).Status() }
func Fault(task any) string     { return task.(*FangoTask).Fault() }
func Result(task any) any       { return task.(*FangoTask).Value() }
func IsCancelled(ctx any) bool  { return ctx.(*FangoTaskContext).Err() != nil }
func Sleep(ctx any, milliseconds int64) bool {
	return FangoTaskSleep(ctx.(*FangoTaskContext), milliseconds)
}
