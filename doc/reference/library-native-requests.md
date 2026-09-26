# Scoped native requests

`NativeRequest` provides bounded native registration with synchronous draining.
It separates background native work from Fango callback execution. It does not
provide an Async scheduler or authorize concurrent Fango execution.

```fango
scope : Int -> (Host ->{IO | e} a) ->{IO | e} a
register : Host -> (Registration ->{IO | e} ()) -> (() ->{IO | f} a)
    ->{IO | e} Maybe (Callback a {IO | f})
poll : Host -> Callback a {IO | e} ->{IO | e} Maybe (Completion a {IO | e})
cancel : Callback a e ->{IO} ()
drain : Callback a e ->{IO} ()
immediate : (() ->{e} a) ->{e} Completion a e
counts : Host ->{IO} Counts
```

`scope capacity body` owns all registrations created through its `Host`.
Nonpositive capacity admits none. `register host start action` returns `Nothing`
when capacity is exhausted; neither callback runs. Otherwise it reserves a
fresh token, calls `start token` once, and returns a callback binding. Native
completion during `start` is preserved. If `start` aborts, its token is cancelled
and drained before the original typed failure is replayed. This also handles
failure before native work starts.

`poll host callback` never waits. It returns `Nothing` until native work has
published readiness and acknowledged quiescence, or when the binding is
cancelled, already delivered, or belongs to another host. On successful claim,
it invokes the stored action once on the calling Fango driver and returns its
typed `Completion`. Aliases cannot invoke that registration twice. The action's
cleanup finishes before the completion becomes observable. A concurrent host
close waits for a claimed action to finish as well as for native work to drain;
once close begins, no later poll can claim a callback. Outward language
aborts become completion values; replay is explicit and selects the handler
at replay time. IO remains in the callback's row index.

`immediate` invokes its action once per call without a registration. Like
registered callback delivery, it captures success or abort and rejects outward
suspension, including through helper functions and cleanup. Registration's
`start` has this same non-suspension obligation. A callback may synchronously
drive a nested coroutine whose suspension is consumed locally. See
[Completion](library-completion.md) for result, failure, and capture semantics.

`cancel` revokes delivery and invokes the native cancellation hook at most
once. It does not imply that native work has stopped. `drain` cancels and then
blocks until both native work and its cancellation hook are quiescent. Scope
exit revokes all deliveries, drains all requests, and joins claimed actions
before returning or propagating a language failure. A native operation that
never acknowledges quiescence prevents scope closure; it is never forcefully
discarded.

`Counts` has `live : Int` and `registrations : Int` fields. Live counts include
unfinished native work and cancellation hooks. A completed but unclaimed
registration still consumes capacity. Delivery frees its slot; cancelled slots
become reusable only after quiescence. Tokens are never recycled, so late
notifications cannot affect a later registration.

Hosts, tokens, and callback bindings cannot escape their scope, including
through data and closures. A native submission may retain only resources that
outlive the request scope. In particular, open a device outside the request
scope so requests drain before device release. An independently registered child
must create its own request scope; a parent host/token is not a transferable
driver. Fango callback captures retain their ordinary lifetime obligations.

## Sidecar protocol

A value native may accept the canonical `Runtime.NativeRequest.Registration` as its
first parameter, followed by ordinary boundary arguments, and return Unit:

```fango
submit : Runtime.NativeRequest.Registration -> Device ->{IO} ()
submit = native
```

The token crosses as Go `any`. Every materialized sidecar has the reserved
`FangoRequest` type alias. A submission type-asserts the token to
`*FangoRequest` and follows this protocol:

1. Call `Begin(cancel func()) bool` before launching work or using retained
   resources. A false return forbids starting work. The hook may be nil when
   work can only be drained. Install cancellation before publishing readiness.
2. Perform native work, optionally in a goroutine. Call `Complete() bool` once
   the result is ready. Duplicate or cancelled/stale publication returns false.
3. Call `Done()` after the final access to retained resources. It is idempotent.
   Readiness alone never authorizes releasing resources. Results needed by the
   Fango action must remain readable after this acknowledgement.

After `Begin`, a sidecar may install one native-only `OnDone(func())` hook. It
runs once inside `Done`, before quiescence is acknowledged, even when the
request was cancelled. A cooperative adapter uses it to publish a scalar event
after native results are stable. The hook must not invoke Fango, access
`FangoHost`, block on its own host, or call back into the same request host.

Cancellation hooks must revoke or signal native work without invoking Fango.
They may run concurrently with that native work and must synchronize shared
state. A hook must not call `Drain` or close its own request host. Native work
must acknowledge `Done` on cancellation and partial failure after `Begin` too.
Before `Begin`, the sidecar owns cleanup of any partial acquisition.

Fango actions remain Fango values; native code never receives or invokes them.
Use ordinary boundary state or [checked opaque storage](native.md#indexed-native-storage)
for native results. Retain only the explicit request token and declared arguments,
never the global `FangoHost`. Readiness carries no interpreter, handler, or raw
resume capability. Native implementations remain trusted to obey their protocol.

Invalid token placement, fabricated token results, or non-Unit submissions
report `NATIVE REQUEST`. Lifetime violations report `RESOURCE ESCAPES`;
independent driver transfer reports `WORK CAPABILITY TRANSFER`. Callback
suspension reports `SUSPENDING RESOURCE CALLBACK`. Suspending
acquisition or cleanup and concurrent Fango invocation remain separate roadmap
work.
