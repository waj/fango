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
- **Further service interception.** Domain service effects and their runners
  are implemented; see [service effects](design/effects.md#service-effects)
  and the [coverage map](reference/services.md). Native-default interception
  would still need declared operations and a checked operation ABI, and
  arbitrary private sidecar actions are not covered. Deferred until programs
  need them: environment variables, subprocesses, and other OS services, whose
  operation boundary is decided when they are added; scoped capability records
  whose row the handler chooses, which need row-kinded effect parameters or
  existential rows; scripted network fixtures, left to libraries; and
  fixture operations that must wait for another task, such as a mock read
  blocked until a write.
- **Partial handlers.** Forwarding the operations a handler omits, through
  a wildcard clause and `super`, is proposed as
  [W14](#w14-partial-handlers-and-super). Its consumer is a test or
  middleware that overrides one operation of a multi-operation service effect.
  The [typed web APIs](roadmap-web.md) do not depend on it: their contract is
  a record, which record update overrides.
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

## W14 Partial handlers and `super`

Deferred independent language work. Wildcard forwarding can precede
expression-level `super`; its evaluation and lambda-capture semantics need a
separate review. Named clauses can already forward with explicit operation
calls.

Today a handler [needs a clause group for every operation](reference/effects.md#handler-clauses-and-abort-routing).
Overriding one operation of a six-operation service effect therefore takes
six clauses, five of which perform their operation again for the enclosing
handler. Tests that stub one call and operation-level middleware both pay
this cost, and it grows with every operation added. Two additions remove it:

- a **wildcard clause** `_ -> super`, written last, which covers every
  operation and argument pattern the earlier clauses leave unmatched;
- the keyword **`super`**, meaning the same operation performed again, with
  the same arguments, for the next handler out.

```fango
effect Catalog
    lookup : Int -> Maybe String
    names : () -> List String

withKnownName : (() ->{Catalog, e} a) ->{Catalog, e} a
withKnownName action =
    handle action() on
        lookup 7 -> resume (Just "Alice")
        names () -> resume (List.filter isActive super)
        _ -> super
```

Here `lookup 8` falls through to the wildcard. `names` forwards and filters
the outer answer; `isActive` is an application predicate on names.

### Wildcard clause

- `_` matches every operation, of every handled application, not covered by
  an earlier clause. This includes argument patterns of named groups that left
  some calls unmatched.
- Its body must be exactly `super`. No other body type-checks for operations
  with different result types and disciplines.
- It is the last clause before `return`. A clause after it is redundant, and a
  wildcard with nothing left to cover is a redundant-clause error.
- A handler needs at least one named clause, which decides the handled
  effects. A handler whose only clause is `_ -> super` is rejected.
- Coverage checking is unchanged: without a wildcard, a missing group or
  pattern is still an error. A handler opts in visibly, clause by clause, not
  through a handle-level keyword that would also hide mistakes.
- `_` never matches the `return` group, and carries no
  [operation signature](reference/effects.md#operation-signatures). Under
  several applications it covers each one independently.

### `super`

`super` is valid only inside a handler clause, and refers to that clause's
operation and arguments. As in Ruby, it takes no arguments. Performing the
operation with different arguments needs no new syntax: clauses already run
outside their own activation, so calling the operation by name reaches the
next handler out.

- **As a whole clause body, it forwards.** A resumptive operation resumes with
  the outer handler's answer. A [stateful handler](reference/effects.md#stateful-handlers)
  resumes with its snapshot unchanged. An abort operation propagates the
  outer abort. The body counts as the clause's one tail resume, or as its
  abort terminal, under
  [resume discipline](reference/effects.md#resume-discipline).
- **Inside an expression, it is the outer answer.** It has the operation's
  result type, so a resumptive clause can post-process it
  (`resume (List.filter isActive super)`) or record a call before forwarding.
- In a nested `handle` inside a clause body, `super` refers to the innermost
  enclosing clause. A lambda inside a clause body may capture `super`, as it
  captures the clause's argument binders.
- Outside a clause, `super` is a `SUPER OUTSIDE CLAUSE` error.

### Typing

A handler that can forward an operation of an application, through a wildcard
or through `super`, keeps that application in its own effect row, because an
outer handler still has to answer it. A handler that never forwards removes
the application, as it does today. This is the shape of Haskell's `interpose`,
and matches the forwarding rule of open handlers in Eff and Links.

### Lowering

Forwarding is tail-resumptive, so it fits the
[synchronous effect execution](design/effects.md) without continuation
objects:

- a forwarding body is a call through the outer handler's evidence, followed
  by `resume`;
- an operation reached only by the wildcard can bind its evidence slot directly
  to the outer handler's, with no clause frame;
- an abort is forwarded by aborting outward with the same payload.

Core sees ordinary clauses, so the Core linter's coverage and resume checks
apply unchanged.

### Surface changes

- `super` becomes a reserved word, in the lexer and the TextMate grammar, with a
  `tokenize.cjs` case, verified by `make test-grammar`.
- The parser accepts the wildcard clause.
- The formatter keeps it last.
- The [effects reference](reference/effects.md) documents both forms and their
  diagnostics.

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
