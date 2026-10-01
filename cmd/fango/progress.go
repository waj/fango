package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/waj/fango/internal/compileevent"
)

// verbosity selects how much a command reports about its own work.
type verbosity int

const (
	quiet    verbosity = iota // the default: diagnostics only
	progress                  // -v: one line per module per phase
	stats                     // -vv: progress plus stage timings and cache totals
)

// stage names the reporter groups by, in pipeline order. Stages absent from a
// build are absent from the table rather than reported as zero.
var stageOrder = []string{
	"parse", "resolve",
	"checked lookup", "checked store", "stage Core",
	"check", "elaborate", "stage snapshot", "semantic-lint",
	"emitted lookup", "emitted store", "lowering", "emission",
	"write + sync", "go build", "LLVM check", "LLVM build",
}

// stageLabel maps the pipeline's event vocabulary onto the rows the table
// shows. Hits and misses share a row: the split between them is the cache
// block's subject, not the timing table's.
var stageLabel = map[string]string{
	"parse":               "parse",
	"resolve":             "resolve",
	"checked-cache-hit":   "checked lookup",
	"checked-cache-miss":  "checked lookup",
	"checked-cache-store": "checked store",
	"stage-section":       "stage Core",
	"stage-snapshot":      "stage snapshot",
	"check":               "check",
	"elaborate":           "elaborate",
	"semantic-lint":       "semantic-lint",
	"emitted-cache-hit":   "emitted lookup",
	"emitted-cache-miss":  "emitted lookup",
	"emitted-cache-store": "emitted store",
	"lowering":            "lowering",
	"emission":            "emission",
}

type stageTally struct {
	owners   map[string]bool
	duration time.Duration
}

// artifactTally is one cache's reuse and traffic over a build.
type artifactTally struct {
	reused, total int
	read, written int
}

// reporter turns compilation events into progress lines and accumulates the
// totals the summary needs. It is the CLI's presentation of the pipeline, and
// holds no compiler state.
type reporter struct {
	level  verbosity
	timing bool // emit machine-readable timings instead of prose
	w      io.Writer
	start  time.Time

	stages  map[string]*stageTally
	order   []string
	checked artifactTally
	emitted artifactTally
	forced  int

	// cached and compiled count modules by how they were obtained, which
	// stays accurate when the cache is off and no artifact event fires.
	cached   int
	compiled int
	noCache  bool

	// seen guards the aggregate Parsing line, which stands in for the
	// per-module parse events.
	parsed    int
	announced bool
}

func newReporter(level verbosity, timing, noCache bool, w io.Writer) *reporter {
	if level == quiet && !timing {
		return nil
	}
	return &reporter{level: level, timing: timing, noCache: noCache, w: w, start: time.Now(), stages: map[string]*stageTally{}}
}

// observer is the seam the compiler reports through. A nil reporter yields a
// nil observer, which costs the pipeline nothing.
func (r *reporter) observer() compileevent.Observer {
	if r == nil {
		return nil
	}
	return r.record
}

func (r *reporter) record(event compileevent.Event) {
	if !event.Begin {
		r.tally(event)
	}
	if event.Stage != "parse" && event.Stage != "resolve" {
		// The loader finishes the whole graph before the first module is
		// checked, so any later stage closes discovery.
		r.discovered()
	}
	if r.level < progress || r.timing {
		return
	}
	// Work in progress is announced when it begins, so a build that pauses
	// pauses under the line naming what it is doing. Reuse is announced on
	// completion instead: a cache hit is the whole of that module's work, and
	// there is no pause to attribute.
	switch {
	case event.Stage == "parse" && !event.Begin:
		r.parsed++
	case event.Stage == "checked-cache-hit":
		r.line("Checking", event.Owner+" (from cache)")
	case event.Stage == "check" && event.Begin:
		r.line("Checking", event.Owner)
	case event.Stage == "emitted-cache-hit":
		r.line("Emitting", event.Owner+" (from cache)")
	case event.Stage == "emission" && event.Begin:
		r.line("Emitting", event.Owner)
	case r.level >= stats && event.Begin && subStage[event.Stage] != "":
		// At -vv the module line alone cannot explain a long pause, because
		// most of a module's cost falls after its check.
		r.line("", "  "+subStage[event.Stage])
	}
}

// subStage names the work that runs under a module's Checking line, which is
// where a slow module spends nearly all of its time.
var subStage = map[string]string{
	"elaborate":      "elaborating",
	"stage-snapshot": "building stage Core",
	"semantic-lint":  "linting Core",
}

func (r *reporter) tally(event compileevent.Event) {
	if event.Stage == "emitted-uncacheable" {
		// No lookup happened and none will, so this owner has no stage of its
		// own to time — it counts only against the reuse it cannot have.
		r.emitted.total++
		return
	}
	label, known := stageLabel[event.Stage]
	if !known {
		return
	}
	duration := event.Duration
	r.add(label, event.Owner, duration)
	switch event.Stage {
	case "checked-cache-hit":
		r.checked.reused++
		r.checked.total++
		r.checked.read += event.Bytes
	case "checked-cache-miss":
		r.checked.total++
		r.checked.read += event.Bytes
	case "checked-cache-store":
		r.checked.written += event.Bytes
	case "emitted-cache-hit":
		r.emitted.reused++
		r.emitted.total++
		r.emitted.read += event.Bytes
	case "emitted-cache-miss":
		r.emitted.total++
		r.emitted.read += event.Bytes
	case "emitted-cache-store":
		r.emitted.written += event.Bytes
	case "stage-section":
		r.forced++
	}
	switch event.Stage {
	case "checked-cache-hit":
		r.cached++
	case "check":
		r.compiled++
	}
}

// add accumulates one stage's time and the owners it touched. owner may be
// empty for whole-command phases that belong to no module.
func (r *reporter) add(label, owner string, d time.Duration) {
	tally := r.stages[label]
	if tally == nil {
		tally = &stageTally{owners: map[string]bool{}}
		r.stages[label] = tally
		r.order = append(r.order, label)
	}
	tally.duration += d
	if owner != "" {
		tally.owners[owner] = true
	}
}

// phase records a whole-command stage the observer cannot see, because it
// belongs to no module: file synchronization and the Go toolchain.
func (r *reporter) phase(label string, start time.Time) {
	if r == nil {
		return
	}
	r.add(label, "", time.Since(start))
	if r.level >= progress && !r.timing && label == "go build" {
		r.line("Linking", "go build")
	}
}

// discovered closes the discovery phase with the one aggregate line that
// stands in for its per-module events. Nothing in discovery is cacheable, so
// per-module lines there carry no information the count does not.
func (r *reporter) discovered() {
	if r == nil || r.announced {
		return
	}
	r.announced = true
	if r.level < progress || r.timing {
		return
	}
	r.line("Parsing", plural(r.parsed, "module"))
}

func (r *reporter) line(verb, detail string) {
	fmt.Fprintf(r.w, "%10s  %s\n", verb, detail)
}

// finish prints the closing summary. name is what the command produced.
func (r *reporter) finish(name string) {
	if r == nil {
		return
	}
	r.discovered()
	if r.timing {
		r.writeTimings(name)
		return
	}
	if r.level >= stats {
		r.writeStages()
		r.writeCache()
	}
	r.line("Finished", r.summary(name))
}

func (r *reporter) summary(name string) string {
	total := r.cached + r.compiled
	line := name
	if line != "" {
		line += " — "
	}
	line += plural(total, "module")
	if total > 0 {
		line += fmt.Sprintf(" (%d cached, %d compiled)", r.cached, r.compiled)
	}
	if read := r.checked.read + r.emitted.read; read > 0 {
		line += ", " + humanBytes(read) + " reused"
	}
	return line + ", " + humanDuration(time.Since(r.start))
}

func (r *reporter) writeStages() {
	total := time.Since(r.start)
	rows := r.rows()
	var measured time.Duration
	for _, label := range rows {
		measured += r.stages[label].duration
	}
	if other := total - measured; other > 0 {
		r.add("other", "", other)
		rows = append(rows, "other")
	}
	fmt.Fprintf(r.w, "\n  %-16s %6s %9s %8s\n", "stage", "owners", "time", "share")
	rule := "  " + strings.Repeat("─", 42)
	fmt.Fprintln(r.w, rule)
	for _, label := range rows {
		tally := r.stages[label]
		owners := "-"
		if n := len(tally.owners); n > 0 {
			owners = fmt.Sprint(n)
		}
		fmt.Fprintf(r.w, "  %-16s %6s %9s %8s\n", label, owners, humanDuration(tally.duration), share(tally.duration, total))
	}
	fmt.Fprintln(r.w, rule)
	fmt.Fprintf(r.w, "  %-16s %6s %9s\n", "total", "", humanDuration(total))
}

// rows is the stages this build actually ran, in pipeline order, with any
// stage the order does not name appended so nothing is silently dropped.
func (r *reporter) rows() []string {
	seen := map[string]bool{}
	var rows []string
	for _, label := range stageOrder {
		if r.stages[label] != nil {
			rows = append(rows, label)
			seen[label] = true
		}
	}
	var extra []string
	for label := range r.stages {
		if !seen[label] {
			extra = append(extra, label)
		}
	}
	sort.Strings(extra)
	return append(rows, extra...)
}

func (r *reporter) writeCache() {
	if r.noCache {
		fmt.Fprintf(r.w, "\n  %-6s disabled by -no-cache\n\n", "cache")
		return
	}
	fmt.Fprintf(r.w, "\n  %-6s %s\n", "cache", "checked  "+r.checked.describe())
	// A command that never emits has no emitted artifacts to report.
	if r.emitted.total > 0 {
		fmt.Fprintf(r.w, "  %-6s %s\n", "", "emitted  "+r.emitted.describe())
	}
	if r.forced > 0 {
		fmt.Fprintf(r.w, "  %-6s stage Core forced for %s\n", "", plural(r.forced, "module"))
	}
	fmt.Fprintln(r.w)
}

func (t artifactTally) describe() string {
	return fmt.Sprintf("%-13s %10s read %10s written",
		fmt.Sprintf("%d/%d reused", t.reused, t.total), humanBytes(t.read), humanBytes(t.written))
}

// timings is the machine-readable form of everything the summary shows.
type timings struct {
	Output  string        `json:"output,omitempty"`
	TotalNS int64         `json:"total_ns"`
	Stages  []stageTiming `json:"stages"`
	Cache   cacheTiming   `json:"cache"`
}

type stageTiming struct {
	Stage  string `json:"stage"`
	Owners int    `json:"owners"`
	NS     int64  `json:"ns"`
}

type cacheTiming struct {
	Checked artifactTiming `json:"checked"`
	Emitted artifactTiming `json:"emitted"`
	Forced  int            `json:"stage_core_forced"`
}

type artifactTiming struct {
	Reused  int `json:"reused"`
	Total   int `json:"total"`
	Read    int `json:"bytes_read"`
	Written int `json:"bytes_written"`
}

func (r *reporter) writeTimings(name string) {
	total := time.Since(r.start)
	out := timings{Output: name, TotalNS: total.Nanoseconds(),
		Cache: cacheTiming{Checked: r.checked.timing(), Emitted: r.emitted.timing(), Forced: r.forced}}
	var measured time.Duration
	for _, label := range r.rows() {
		tally := r.stages[label]
		measured += tally.duration
		out.Stages = append(out.Stages, stageTiming{Stage: label, Owners: len(tally.owners), NS: tally.duration.Nanoseconds()})
	}
	if other := total - measured; other > 0 {
		out.Stages = append(out.Stages, stageTiming{Stage: "other", NS: other.Nanoseconds()})
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return
	}
	fmt.Fprintf(r.w, "%s\n", data)
}

func (t artifactTally) timing() artifactTiming {
	return artifactTiming{Reused: t.reused, Total: t.total, Read: t.read, Written: t.written}
}

func share(d, total time.Duration) string {
	if total <= 0 {
		return "-"
	}
	percent := float64(d) / float64(total) * 100
	if percent < 0.5 {
		return "-"
	}
	return fmt.Sprintf("%.0f%%", percent)
}

func humanDuration(d time.Duration) string {
	switch {
	case d >= time.Second:
		return fmt.Sprintf("%.2fs", d.Seconds())
	case d >= time.Millisecond:
		return fmt.Sprintf("%.1fms", float64(d)/float64(time.Millisecond))
	default:
		return fmt.Sprintf("%.0fµs", float64(d)/float64(time.Microsecond))
	}
}

func humanBytes(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.2f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
