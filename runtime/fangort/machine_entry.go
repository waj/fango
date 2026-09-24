package fangort

// RunMachineEntry runs a closed source entry that constructs Machine service
// evidence. Source checking admits no outward suspension or unhandled effect.
func RunMachineEntry(entry MachineFrame) {
	runClosedMachine(entry)
}

// RunMachineValue constructs a closed, pure service context at value
// initialization. Its retained workers are invoked by a later producer.
func RunMachineValue[A any](entry MachineFrame) A {
	return runClosedMachine(entry).(A)
}

func runClosedMachine(entry MachineFrame) any {
	machine := StartMachine(entry)
	event, err := machine.Run()
	if err != nil {
		panic(err)
	}
	if !event.Done {
		_, _ = machine.Abandon()
		panic("fangort: suspension escaped the program entry")
	}
	if event.Exit != nil {
		RequireNormal(Propagate[Unit](event.Exit))
	}
	return event.Value
}
