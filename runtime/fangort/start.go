package fangort

// MachineStart describes a lazy call. Factories may construct a frame or a
// primitive request, but must not run user code. Synchronous work is deferred
// until its own dispatcher turn. Pause carries the lexical owner explicitly.
type MachineStart struct {
	Frame   MachineFrame
	Run     func() (any, *ExitRequest)
	Owner   *YieldOwner
	Request any
	Pause   bool
}

func FrameStart(frame MachineFrame) MachineStart                 { return MachineStart{Frame: frame} }
func ImmediateStart(run func() (any, *ExitRequest)) MachineStart { return MachineStart{Run: run} }
func PauseStart(owner *YieldOwner, request any) MachineStart {
	return MachineStart{Owner: owner, Request: request, Pause: true}
}

// StartCall is used after the caller has saved its result continuation. A
// primitive borrows that frame until its result arrives; a real frame follows
// the ordinary call/tail-call protocol. No factory or Step recurses here.
func StartCall(start MachineStart, tail bool) MachineStep {
	if start.Frame != nil {
		kind := MachineCall
		if tail {
			kind = MachineTailCall
		}
		return MachineStep{Kind: kind, Frame: start.Frame}
	}
	if start.Run != nil {
		return MachineStep{Kind: MachineRun, Value: start.Run}
	}
	if start.Pause {
		return MachineStep{Kind: MachineSuspend, Owner: start.Owner, Request: start.Request}
	}
	panic("fangort: empty machine start")
}

// StartFrame adapts a start at an owning boundary, which needs an entry frame.
// Ordinary generated calls use StartCall and avoid this boundary allocation.
func StartFrame(start MachineStart) MachineFrame {
	if start.Frame != nil {
		return start.Frame
	}
	if start.Run != nil {
		return ImmediateMachine(start.Run)
	}
	if start.Pause {
		return SuspendMachine(start.Owner, start.Request)
	}
	panic("fangort: empty machine start")
}
