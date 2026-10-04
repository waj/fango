# Roadmap: effects, instances, and owned coroutines

The coroutine direction below is deferred by the
[synchronous effect design](design/effects.md). It describes a historical
proposal, not the current compiler. Current reader and task behavior belongs in
[readers](../stdlib/Reader.fango) and [Async](../stdlib/Async.fango).

This document retains unfinished general effect-language work and the
historical owned-coroutine proposal. The
[Async documentation](../stdlib/Async.fango) owns current task behavior. The
[main roadmap](roadmap.md) is the navigation entry point.

Everything proposed here remains unimplemented. Implemented behavior belongs
to [effects](reference/effects.md), [resources](reference/resources.md), and
[streams](../stdlib/Stream.fango); implemented architecture belongs to
[effect execution](design/effects.md), [machines](https://github.com/waj/fango/blob/2a8f313fec6897824549e0d5074d75d4c6be3441/doc/design/machines.md), and
[ownership](design/ownership.md). Promote durable results there when a stage
lands, then remove that completed work or mark its stage DONE under the
[repository milestone rules](../AGENTS.md). Stage IDs and titles remain stable;
Git history is the implementation archive.

## Direction and compiler boundary

Generalize the owned coroutine behind Iterator rather than adding another
domain-specific continuation engine. Stream and Async should be ordinary
libraries over general control capabilities. A small bundled control API may
use compiler intrinsics; this is not a promise to eliminate all intrinsics.
In particular, cleanup across an arbitrary residual exit still needs the
general resource-scope machinery.

Three identities have different jobs:

| Identity | Question it answers | Example |
| --- | --- | --- |
| Handler activation | Which interpretation does this operation use? | Two independent state handlers answer different `get` calls |
| Coroutine owner | Who owns these suspended frames and may advance them? | Two producers retain separate positions between pulls |
| Library context | Which work shares a lifetime and policy? | Several children belong to one task context |

The former instance-binding rule addressed the first question. It does not
by itself answer the second. A handler that calls a consumer and immediately
resumes can implement a push traversal; returning from `next`, doing unrelated
work, and calling `next` again needs saved execution. Several coroutines can
also use one handler, so one handler instance must not imply one saved stack.

| General compiler/runtime support | Ordinary Fango library | Trusted native sidecars |
| --- | --- | --- |
| Effect dispatch, instance identity, targeted exits | Domain effects and interpretations | Host operations |
| Scoped coroutines, typed suspension, exclusive advancement | Stream/Iterator wrappers and stages | External readiness and timer registration |
| Capture, retention, transfer, and lifetime proofs | Task contexts, queues, waits, completion policy | Synchronization and goroutine execution |
| Resource scopes and cleanup execution | Cancellation policy, race, timeout, event adapters | Bounded blocking-operation bridges |

Names such as `Stream.yield`, `Iterator.next`, `Async.spawn`, and
`Async.await` must not receive compiler privileges in the target design.
General control and native contracts are checked by behavior and ownership,
not by recognizing a scheduler's name.

## Shared foundation and delivery order

The proposed control sequence depends on checked ownership, typed completion,
dynamic scope registration, and native retention. The test-only models did not
implement those APIs.

The first usable control delivery is scoped typed coroutines, ordinary
Stream/Iterator wrappers, and a deterministic cooperative scheduling example
that suspends inside an unfinished pull. Source compatibility is not a constraint:
the new Coroutine API and its ordinary Iterator wrapper are the chosen target,
and bundled callers migrate with the implementation rather than keeping obsolete
compiler paths alive.

Later general stages add scope-owned dynamic allocation, suspending cleanup,
and checked native retention/transfer. These are separate obligations:
lexically nesting two coroutines does not prove that a dynamic task registry
is safe, and safe sequential advancement does not prove safe goroutine transfer.
The native work is split into typed values, scoped requests, shared/transferable
capabilities, and concurrent invocation/runtime safety. This sequence belongs
to the historical coroutine proposal. The current [native task architecture](design/tasks.md)
and [Async API](../stdlib/Async.fango) use a different execution boundary.

Keep Direct and Exit fast paths. Select explicit Machine execution only where
control requires it; do not require a scheduler, goroutine, or channel for a
synchronous effect handler or pipeline. Preserve per-curried-arrow timing,
definition-site evidence, deep handler behavior, normal-only return clauses,
deterministic module-owned callable families, and independent Core/Machine
verification. Staging, generated expressions, batch builds, and the REPL must
enter the same applicable proof boundaries.

Stored execution remains explicit frames, without host-stack copying or
runtime internals. A goroutine executor may drive those frames; it must not
implement each effect continuation with a parked goroutine. Unbounded suspended
recursion still needs storage, and lifetime proofs do not imply Go stack
allocation. Optimization follows evidence, not the number of library layers.

## Resume discipline

Keep existing user handlers tail-resumptive or abort-only initially. A
tail-resumptive clause may call the scoped coroutine's `pause` callback
before its final resume:

```fango
-- Proposed use of the coroutine capability; routine imports omitted.
handle producer() on
    emit value -> resume (pause value)
```

The handler itself obeys today's tail-resume discipline. The general control
boundary saves the unfinished execution while `pause` waits for a reply.
There is no Fango value representing the handler's raw continuation.

This differs from non-tail resumption:

```text
result = resume savedContinuation answer
print "the resumed coroutine finished"
return result
```

The second example requires a return path back into the handler after its
resumed subject finishes. That is a separate language feature, not an implicit
consequence of adding scoped coroutines. A concrete consumer and checked
answer/lifetime contract must justify it. Multi-shot cloning and arbitrary
escaping resume callbacks remain outside this direction.

## Handler instances: open questions

Scoped activation binding retains a fresh permission rather than claiming a
stateful callable is pure. See the
[reference rule](reference/effects.md#closures-and-handler-effects).
Handler clauses run outside their own activation and may use enclosing handlers.

Further explicit instance APIs have no selected design. The historical
coroutine proposal does not settle those questions.

## Explicit capture and borrowing annotations

Written contracts should follow the working inferred contracts. They can
describe capture, retention, exclusive access, and transfer obligations for
library authors, but must not weaken what the implementation proves.

A future stage must choose the spelling and placement, check annotations
against inferred bodies, export them through module interfaces, and preserve
them across adaptation and staging. A wrong annotation must identify the
conflicting owner or access in its diagnostic. Ordinary helpers continue to
infer their contracts; annotations are not a prerequisite for every callback.

No notation in these roadmaps is a commitment to capture-annotation syntax.
If syntax is introduced, update parser, formatter, reference, TextMate grammar,
and representative tokenization fixtures in the same implementation change.

## Deferred topics

These are not automatically prerequisites for the first coroutine milestone:

- **Operation-local polymorphism follow-up.** Source-defined resumptive
  operations use checked request packaging and clause skolems; see the
  [implemented contract](reference/effects.md#effects-and-handlers). Class
  constraints still need dictionary transport, and native operations need a
  checked sidecar ABI. Typed coroutine replies use a callback whose types are
  fixed when the owner is opened. The current task API uses a
  [native closure invocation boundary](design/tasks.md#async-runtime-foundation).
- **Row-kinded effect parameters.** Unlike row-indexed ADTs, effect headers
  currently fix parameters to value kind. Generalizing them is not required by
  the current [Async API](../stdlib/Async.fango), which uses native task
  invocation rather than hidden scoped work budgets.
  Invalid use of an effect parameter as both an ordinary type and a row
  reports a kind mismatch; declaration acceptance alone does not establish
  support for row-kinded effect parameters.
- **Complete builtin IO interception.** It still depends on actual native
  declarations and evidence fitting a checked operation ABI. Fixed-signature
  domain effects and suspending interpretations remain useful independently.
- **Escaping coroutine owners and raw resumptions.** The chosen owner stays
  scoped. Dynamic allocation means allocation into a live owner, not detachment
  from every owner. Raw resume escape and continuation cloning are not included.
- **General fallible sidecars.** Extend the bundled File/Net shape only with
  resolved error identities or a declared marker and a documented error
  vocabulary. The [native reference](reference/native.md) owns implemented
  callback and value contracts; they do not imply a universal foreign-function
  interface.

Handlers do not roll back arbitrary external writes. Search may replay fresh
actions without cloning continuations. Cleanup guarantees an attempt, not
successful external close or termination of user release code.

## Verification and documentation lifecycle

Preserve the [repository gates](../AGENTS.md) and
[verification contracts](design/verification.md), including independent lint,
malformed-IR rejection, differential and functional tests, deterministic
emission, and `go vet`.

For a roadmap edit, check local links, anchors, dependencies, proposed labels,
API spelling, and ownership of explanations. Proposed examples are acceptance
specifications, not fixtures claimed to compile today. Do not run timing
benchmarks for documentation work. Implementation timing work belongs on an
otherwise idle machine, with setup, execution, allocations, live storage,
compile latency, and output size reported separately.
