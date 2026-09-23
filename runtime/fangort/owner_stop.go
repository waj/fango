package fangort

// resolveStopExit interprets cleanup aborts against still-live definition-site
// handlers. Only their clauses run; the abandoned producer continuation and
// pending completion publication are never resumed.
func (m *Machine) resolveStopExit(exit *ExitRequest) *ExitRequest {
	for exit != nil {
		m.stopRouting = true
		caught := m.routeExit(exit)
		m.stopRouting = false
		if !caught {
			return exit
		}
		pending := m.caught
		// A checked abort-handler resumption only materializes its clause
		// factory. Executing that clause separately discards its return edge.
		step := m.frames[len(m.frames)-1].Step(m)
		if step.Kind != MachineCall || step.Frame == nil {
			panic("fangort: invalid cleanup abort clause entry")
		}
		clause := StartMachine(step.Frame)
		clause.states = append([]any(nil), m.states...)
		clause.parentStateOwner, clause.parentStateCount = m, len(m.states)
		event, err := clause.Run()
		if err != nil {
			panic(err)
		}
		if !event.Done {
			_, _ = clause.Abandon()
			panic("fangort: suspension escaped a synchronous cleanup handler")
		}
		exit = event.Exit
		if exit != nil {
			exit = Suppress(exit, pending)
		}
	}
	return nil
}
