# Roadmap: effects, instances, and owned coroutines

The coroutine direction below is deferred by the
[synchronous simplification](roadmap-simplification.md). It describes a historical
proposal, not the current compiler. Active scoped Reader and Task API work lives
in [scoped effects](roadmap-scoped-effects.md).

This document retains unfinished general effect-language work. The shared
[coroutine roadmap](roadmap-coroutines.md) owns the proposed control API,
Iterator reuse, Stream migration, and their implementation stages. The
[Async roadmap](roadmap-async.md) owns tasks, schedulers, native readiness,
executors, and concurrent combinators. The [main roadmap](roadmap.md) is the
navigation entry point, not another specification of these contracts.

Everything proposed here remains unimplemented. Implemented behavior belongs
to [effects](reference/effects.md), [resources](reference/resources.md), and
[streams](reference/library-streams.md); implemented architecture belongs to
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

The [coroutine stages](roadmap-coroutines.md#implementation-stages) define
the control implementation sequence; the [Async dependency table](roadmap-async.md#implementation-stages)
defines its consumers. The joint C0/A0/C4-design feasibility contract is selected;
review it before C1. [C0](roadmap-coroutines.md#c0-control-and-ownership-contracts)
owns the additional general prerequisites, and
[A0](roadmap-async.md#a0-library-representation-contract) records the evidence
and its limits. Revalidate the proposed encoding against real compiler support
before the C2 Stream migration; the test-only models do not implement the APIs.

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
capabilities, and concurrent invocation/runtime safety. Consumers depend only
on the contracts they use, while every independently scheduled child must meet
the same capture rules. The Async roadmap links to these stages instead of
defining a competing owner or cleanup model.

The first practical Async release is cooperative structured IO at A3. It does
not wait for all three executors or generated CPU polling. Suspending cleanup
then supports bounded concurrent combinators and events without requiring
parallel execution. Both concurrent executors consume the same C6d safety gate;
neither is an architectural prerequisite for the other. C7/A8 subsequently
complete the CPU responsiveness contract, and can proceed before parallelism
when their own prerequisites are ready. Exact dependencies and acceptance live
in the two stage tables, not in a second global milestone numbering here.

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
handle producer() of
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

Closures retain their invocation effect rows; implicit binding that drops the
handled effect is no longer implemented. See the
[reference rule](reference/effects.md#closures-and-handler-effects).
Handler clauses run outside their own activation and may use enclosing handlers.

The active proposal for explicit instance identities and local escape checking
is [scoped readers and tasks](roadmap-scoped-effects.md). It owns the fresh
quantification, identity-aware rows, and erased representation gates. The
historical coroutine proposal does not settle those questions.

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

- **Operation-local polymorphism.** A declaration such as `fetch : Key a -> a`
  instantiated independently at each operation is different from an effect
  parameter fixed for its handler. General support needs checked request
  packaging, clause skolems, dictionary transport, answer types, and indirect
  calls. Go generic-field/method restrictions cannot be bypassed by unchecked
  casts. Typed coroutine replies use a callback whose types are fixed when
  the owner is opened. The task API must separately resolve its
  [polymorphic packaging gate](roadmap-async.md#api-representation-gate).
- **Rows keyed by effect arguments.** `{Box Int, Box Bool}` is rejected today.
  Supporting it introduces questions such as whether `{Box a, Box Int}` names
  one or two labels before `a` is known. A rigid/ground restriction is a possible
  answer, not an implemented rule. Nullary control markers and typed callbacks
  avoid requiring this extension for heterogeneous coroutines.
- **Row-kinded effect parameters.** Unlike row-indexed ADTs, effect headers
  currently fix parameters to value kind. Generalizing them is not required by
  the nullary Async encoding; its hidden budgets use the
  [scoped work contract](roadmap-execution-contracts.md#scoped-effects-and-work-packages).
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
  vocabulary. The necessary callback/value contracts are described in the
  [general native stage](roadmap-coroutines.md#c6-native-retention-and-transfer);
  they do not imply a universal foreign-function interface.
- **Shared mutable state.** [STM](roadmap-stm.md) owns that proposed protocol.
  Cooperative `get; wait; put` is not atomic, and the coroutine owner itself
  is not a shared cell. Scheduling policy and its exclusions belong to
  [Async](roadmap-async.md#boundaries-and-non-goals).

Handlers do not roll back arbitrary external writes. Search may replay fresh
actions without cloning continuations. Cleanup guarantees an attempt, not
successful external close or termination of user release code.

## Verification and documentation lifecycle

The detailed [coroutine acceptance matrix](roadmap-coroutines.md#acceptance-and-verification)
and [Async acceptance matrix](roadmap-async.md#acceptance-and-verification)
own their fixtures. Preserve the [repository gates](../AGENTS.md) and
[verification contracts](design/verification.md), including independent lint,
malformed-IR rejection, differential and functional tests, deterministic
emission, and `go vet`.

For a roadmap edit, check local links, anchors, dependencies, proposed labels,
API spelling, and ownership of explanations. Proposed examples are acceptance
specifications, not fixtures claimed to compile today. Do not run timing
benchmarks for documentation work. Implementation timing work belongs on an
otherwise idle machine, with setup, execution, allocations, live storage,
compile latency, and output size reported separately.
