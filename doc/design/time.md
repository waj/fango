# Dates and clocks

The [Time modules](../../stdlib/Time.fango) own their API contracts in source
comments. [Reference navigation](../reference.md#bundled-standard-library)
lists them; [billing](../../examples/billing.fango) demonstrates their composition.

## Values and invariants

Date is an integer Gregorian day count; TimeOfDay is nanoseconds since
midnight; Instant is Unix floor seconds plus a nonnegative nanosecond fraction.
Duration shares Instant's two-field shape, while Period stores signed months
and days. These are ordinary typed Fango products. Their raw constructors are
declared in [Time.Internal](../../stdlib/Time/Internal.fango) and re-exported
by the public modules without constructors, so validation lives in one place
per type while DateTime and Clock can rebuild values they have already proven
valid without a `Fail` row. The calendar and all arithmetic are implemented in
Fango; no numeric semantics or compiler syntax were changed.

Dates span years 1 through 9999. Instants span one day less at each end,
0001-01-02T00:00:00Z through 9999-12-30T23:59:59Z, and offsets are strictly
within a day, so interpreting any instant in any zone yields a local date
within the calendar. That margin is what makes zone conversion, instant
subtraction, clock reads, and formatting total. Only construction from
components or text, and movement that would leave the range, can fail.

DateTime stores the instant, shared immutable zone rules, and the local Date
and TimeOfDay derived once at construction, so field access is pure. Equality
and ordering use the instant; representation comparison additionally checks
zone identity. ISO text is the local reading with its offset, which the parser
accepts, so a round trip keeps the moment and the local view; the IANA name is
not encoded. Instant's ISO text is the UTC form with `Z`.

Calendar movement changes local fields, clamps months before adding days, then
resolves the final reading once. Elapsed movement changes the instant.
Unchanged replacements preserve the existing overlap occurrence. Range
endpoints are first valid instants and exclusive upper bounds; an upper bound
outside the supported range fails rather than saturating.

## Errors

One shared `Time.Error` serves every module because a row holding two `Fail`
instances makes each surrounding `Fail.attempt` ambiguous, and application code
routinely mixes Date, DateTime, and Clock calls under one attempt. The type is
kept to outcomes that remain reachable: invalid components, invalid text,
leaving the supported range, and the two transition rejections. Each fallible
function's comment names the constructors it raises. Formatting copies unknown
tokens verbatim, Duration and Period arithmetic wrap like Int far beyond any
calendar quantity, sub-second unit conversions answer `Maybe`, and zone lookup
answers `Maybe`, so none of them carries an error type.

## Native timezone boundary

Time.Zone alone owns its native zone handles. Scalar Unix/local seconds and
opaque same-module result wrappers cross the existing native ABI; no new
compiler representation or foreign record support is needed. Loaded Go
locations are immutable and successful loads share a concurrent cache. System
tzdata takes precedence over the toolchain's embedded fallback. Fixed offsets
and UTC do not read the environment; the machine's zone is detected once per
process, only when asked for, from `TZ` or the `/etc/localtime` link, and
falls back to Go's Local rules under the name "Local". Alias names retain
their identity.

The resolver walks actual offset intervals in the candidate window and checks
candidate moments against those intervals. It resolves overlaps and gaps
explicitly instead of depending on Go's unspecified local-time constructor
choice. IANA civil offsets and accepted fixed offsets are within 24 hours;
intervals from externally supplied TZif data outside that contract are not
accepted as candidates, and applying such an offset is a trusted-native fault
that panics rather than a value. Gaps use the actual offset change, including
half-hour and whole-day transitions. Boundary search intersects local calendar
intervals with each absolute offset interval and stops at the first valid instant.

Controlled TZif fixtures test the native transition algorithm and the offset
contract independently of host data. Differential Fango cases cover calendar
policy, historical IANA transitions, range boundaries, formatting, and clock
handling.

## Clock execution

Clock has two operations: the current instant and a default zone. `now` and
`today` combine them; `nowIn` and `todayIn` take a zone explicitly, and
`withZone` re-handles only the default zone while readings keep coming from the
enclosing handler. Clock has no default handler; `runSystem` detects the
machine's zone when installed and `runSystemIn` takes the default explicitly.
The fixed handler is ordinary Fango and answers both operations from one
DateTime. The system handler samples Go's clock once per reading into an
opaque immutable sample and
extracts its two scalar fields; a host clock outside the supported instants
is a runtime fault in the sidecar, so no handler carries a `Fail` row and a
system clock inherits across task boundaries like any other handler. The
system reading has wall-clock semantics; it is not a monotonic elapsed-time
measurement.

`runOffset` stores the starting DateTime and one native clock sample captured
at runner entry. Each instant read adds the monotonic elapsed duration since
that sample to the starting instant, so wall-clock adjustments do not change
progression. Elapsed samples are immutable and expose floor seconds and a
nonnegative nanosecond fraction; the calendar arithmetic remains in Fango.
The starting zone is the default, and tasks inherit the same origin.

`runStepped` keeps the last returned instant in a private
[atomic state](effects.md#atomic-state) runner. The first read returns the
starting instant; later reads add the signed Duration. Only instant reads
advance it, so zone queries and `withZone` do not consume extra steps. Nested
runners have independent state. Each read-and-advance is one atomic update, so
tasks sharing the runner never repeat a reading, and sequential reads have
deterministic progression without IO.

Both advancing runners use checked instant addition and handle its `Fail`
locally, translating an out-of-range reading to a private native runtime fault.
Their clauses therefore have no dependency on an outer abort handler and can
be inherited by tasks. Stepped advancement is computed only when the next
instant is requested, so the final valid reading succeeds even when its next
step would leave the range.

System and elapsed clock reads and zone loading are excluded from compile-time
evaluation.
Deterministic scalar access and timezone calculations have native registry
adapters for the compiler's evaluator. Ordinary execution uses the same
sidecars under the interpreter worker and generated Go.
