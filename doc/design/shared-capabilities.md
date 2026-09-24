# Shared values and invocation authority

The compiler distinguishes the lifetime of a shared context from exclusive
execution authority. [Work transfer](ownership.md#scoped-work-budgets) checks
both explicit captures and retained evidence using the capture-flow heap.

## Native storage and sharing

An indexed native wrapper has one monomorphic boundary field and phantom source
parameters. Resolved native metadata seals its constructors against source
construction and projection, including in its defining module. Native signatures
cannot change a wrapper index, including by crossing between two wrapper types.
An opaque payload can cross only with a checked same-index Native.Any resource
wrapper. The storage contract records allocation, read, write, or same-index
alias edges; Core reconstructs it independently from the native scheme.

Both backends wrap payloads in a runtime token with an inaccessible Go field.
Sidecars receive `any` and store/return that token without depending on CtorVal,
generated structs, function ABIs, or Unit erasure. Generated calls materialize
typed arguments before boxing. Stored Unit remains a value, unlike ordinary
Unit arguments omitted from a scalar sidecar signature. The interpreter keeps
tokens and native handles on the worker's heap. Reading unseals at the original
checked type. Trusted native code must preserve the declared storage edge and
must never inspect or invoke a token.

The abstract heap retains a storage object's payload and aliases. Writes check
that every retained owner outlives the destination; reading preserves those
captures. Storage never launders a scoped closure into an unrestricted value.
The bundled [Cell](../reference/library-cells.md) additionally retains a dynamic
registry facet in its public publisher/reader wrappers and admits only
capture-free publication into its native storage. Its mutex protects readiness
and a single successful publication. No request outlives a native call.

`shared-resource` is a nominal opt-in for a resource with one canonical
Native.Any field. It promises synchronized native operations. Other wrappers
with the same representation gain no sharing authority. Work transfer traverses
closures, ADTs, dictionaries, storage payloads, and hidden handler evidence;
it rejects parent mutable state, borrowed cursors/pauses, and native resources
without that opt-in. Child-local acquisition remains valid. A shared resource
borrowed from `Scope.bracket` must enclose the registry so child draining precedes
release. Native requests with longer retention are outside this boundary.

## Split service evidence

A service effect records one fixed Invocation request/reply protocol in every
operation scheme and in its operation metadata. Its public parameters stay
unchanged. The runtime operation adds one hidden Machine callback. Handler
evidence retains only immutable context; each operation worker receives the
callback supplied at the actual call site. This metadata survives interfaces,
substitution, specialization, Core ANF, module objects, and execution codecs.

`Service.run` validates an active pause, then installs a scoped Invocation
adapter. A service operation constructs a checked callback that forwards to the
caller's lexical Invocation evidence. Its worker installs a fresh local adapter
around the clause computation and removes it before tail-resuming the handled
subject. Thus a bound context can be called from different producers without
retaining their pause. A nested pull forwards the task slot as residual evidence;
its local yield owner does not replace that slot. Independent work must install
its own slot. There is no global current-task variable.

Core verifies declaration/metadata agreement, the hidden argument type and
forwarding shape, the authority introducing each Invocation handler, and the
Service.run source contract. Capture-flow independently checks active producer
ownership, scoped retention, and transfer. Source cannot supply an unproven
adapter, handle Invocation directly, or attach mutable state to service evidence.

Constructing a service handler materializes Machine operation workers even when
its subject only returns a bound callable. That construction carries Machine
transport while retaining its source effect row. Both backends execute such
factories to completion; a closed program entry may construct them without
exposing suspension. Machine operation arguments register their nested callback
workers just like ordinary call arguments.

These contracts establish cooperative sharing. Concurrent execution and native
requests retaining callbacks still require their separate roadmap gates.
