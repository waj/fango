// Package compileevent is the instrumentation seam every compilation stage
// reports through. It is a leaf package so that the loader, the semantic
// installer, the staging evaluator, and the back end can all name one event
// type without an import cycle.
package compileevent

import "time"

// Event is one completed stage for one owner. Every site reports after the
// work it names, so Duration always covers that work and never the gap to the
// next event. Stage names are the pipeline vocabulary: parse, resolve,
// checked-cache-hit, checked-cache-miss, check, elaborate, semantic-lint,
// stage-section, emitted-cache-hit, emitted-cache-miss, lowering, emission.
type Event struct {
	Stage string
	// Owner is a module name, or "<entry>" for a headerless entry module.
	Owner string
	// Begin marks the start of a stage rather than its completion, so a
	// caller can report what is running while it runs. A begin event
	// carries no duration or size; the completion that follows carries
	// both. Only stages a caller may need to announce emit one.
	Begin bool
	// Duration is the elapsed time of the work the stage names. It is zero
	// for events that mark a decision rather than work.
	Duration time.Duration
	// Bytes is cache payload read or written, and zero for stages that
	// touch no artifact.
	Bytes int
}

// Observer receives events as they happen. A nil observer has no cost and no
// user-visible output; compilation never depends on one being installed.
type Observer func(Event)

// Report sends one event, tolerating a nil observer so call sites stay
// uncluttered.
func (o Observer) Report(event Event) {
	if o != nil {
		o(event)
	}
}

// Stage reports a stage that carries neither a duration nor a payload size.
func (o Observer) Stage(stage, owner string) {
	o.Report(Event{Stage: stage, Owner: owner})
}

// Timed reports a stage with the elapsed time since start.
func (o Observer) Timed(stage, owner string, start time.Time) {
	o.Report(Event{Stage: stage, Owner: owner, Duration: time.Since(start)})
}

// Begin announces that a stage is starting. Pair it with Timed so the stage is
// both announced as it runs and measured when it ends.
func (o Observer) Begin(stage, owner string) time.Time {
	o.Report(Event{Stage: stage, Owner: owner, Begin: true})
	return time.Now()
}
