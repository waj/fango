# Native retention and driver callbacks

[NativeRequest](../reference/library-native-requests.md) owns the public API and
sidecar protocol. Its scope uses ordinary `Runtime.Scope.bracket`; registration and
release are synchronous, so no owner can disappear during a drain. Dynamically
owned coroutines may create their own request scopes and abandonment runs their
drains before enclosing device cleanup.

## Checked retention

The canonical registration wrapper is the only imported request type admitted
at a native boundary. Module checking admits its erased `any` spelling, then
inference resolves its nominal identity and verifies first-argument placement,
Unit result, and value-native status. `NativeInfo` and `NativeCall` retain the
request contract. Core reconstructs it from the scheme and verifies the scoped
host/token representations independently. Constructors remain private to the
bundled module; user natives cannot create a registration.

A capture-flow request edge stores every retained argument against the token's
owners. This rejects shorter-lived devices even when a submission returns Unit
and no source value escapes. Allocation propagates host captures into tokens;
ordinary ADT and closure flow propagates them into callback bindings. The graph
exports the same obligations through helpers and module objects. Execution
codecs preserve them, and the Core linter rejects missing or forged metadata.
The Async adapter's bridge is a checked shared resource owned outside all
request hosts. Its private result buffer is acquired outside the request host
and closed after that host drains; its native close operation checks that no
worker is still using it. Folded recursive owner IDs remain insufficient proof
of resource outliving, so this buffer does not expose a Fango resource handle.

`Runtime.NativeRequest.immediate` is a checked Completion introduction with an exported
non-suspension obligation. Capture analysis checks the actual action and its
interpreted evidence using the same synchronous boundary as acquisition/release.
It preserves the typed outcome and all capture obligations without inventing a
new resource owner for the returned completion. The evaluator and generated Go
use the existing Completion machinery; Machine lowering retains the same
independently checked boundary when a nested synchronous pull is involved.

## Runtime ownership

The host holds a bounded map of fresh pointer-identity registration tokens.
Tokens are never reused as IDs. A mutex serializes admission, Begin, readiness,
delivery claim, cancellation, and quiescence. Native code retains only the token
and native resources; callback closures remain in ordinary typed Fango bindings.
The host never retains FangoHost, evaluator frames, or Fango callback functions.

Begin publishes a cancellation hook before native work starts. Complete only
publishes readiness; Done acknowledges the end of native resource access.
An optional native `OnDone` hook runs before Done closes the quiescence channel;
this lets a native event source wake a driver without a gap between notification
and claimable completion. The hook cannot call back into its request host.
Delivery claims require both, match the host, and succeed at most once.
Cancellation revokes claim before running its hook outside the mutex. Draining
waits for the worker and any in-flight cancellation hook. Map entries stay live
until both are quiescent; completed readiness consumes admission until claimed.
Closing the host first forbids all publication/claims and further admission,
then cancels and drains outstanding entries.

Sidecar support aliases refer to the same runtime token implementation in both
backends. In the interpreter, native work stays on the worker heap and publishes
readiness without reverse-host RPC. Only a later driver call invokes Fango and
captures its outcome. In generated Go, the same source library and token state
machine implement delivery. Synchronization here protects native requests; it
does not establish concurrent safety for Fango execution.
