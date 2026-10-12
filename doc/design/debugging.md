# Source debugging

The opt-in debug profile retains source metadata for interpreter and native
source debugging. The REPL uses interpreter activation frames and a
synchronous control protocol. [Commands and behavior](../reference/debugging.md)
belong to the reference; [remaining work](../roadmap-debugging.md) to the roadmap.

## Metadata and profile

`internal/debugger` defines immutable source snapshots, expression sites,
source-to-runtime binding names and displayed types, breakpoint identities,
and inspection/control messages. Each Core expression embeds optional
`DebugInfo`; this avoids wrapping semantic nodes or obscuring tail positions.
Elaboration attaches the innermost source expression and the lexical scope
visible before its evaluation. Blocks and lambda creation do not introduce
redundant sites; their executable children carry sites.

Sources retain logical compiler paths, full text, and a SHA-256 content hash.
Prompt source names include a content-hash prefix to distinguish separate
inputs. The frontend resolves module and `<stdlib>` paths against the session
and library roots. Stops use retained snapshots even when disk contents or
current module generations differ.

The debug checker disables inlining and scalar specialization. Numeric
constant folding, semantic lowering, ANF, and eligible self tail loops remain.
Core rewrites and ANF preserve sites; the linter checks source identities,
positions, and binding references in their lexical Core scopes. Metadata uses
displayed type strings, avoiding a second type-identity graph.

Checked debug objects occupy their own `checked-debug-vN` cache namespace.
The metadata version and exact source contents also participate in their
validity key. Semantic and ABI fingerprints ignore `DebugInfo`. The execution
codec carries complete debug snapshots alongside its existing line-only call
origins. The worker build key already hashes support-source contents. Staging
temporarily uses the ordinary elaboration profile and has no runtime controller.

## Suspension and inspection

Each input owns a `debugExecution`. At a source site the evaluator associates
its lexical frame with the current trace activation. Activations receive
monotonic IDs, independent of their stack depth; tail iterations retain the ID
and refresh parameter frames. Handler clauses have source debug frames too.

The hook blocks inside the evaluator's live call stack. It exchanges an event
for commands until a resume or stop request arrives. There is no unwind/replay
continuation. Step-over and step-out track activation membership, so a callee
at the same depth cannot impersonate a returned frame. Breakpoints match the
logical file, content hash, and exact executable line.

Inspection reads lexical frames directly. It never calls the normal value
display path, evaluates an expression, or forces a global cell. A bounded
structural renderer leaves native resources opaque. Handle tables belong to
one pause and are cleared on resume; monotonically allocated handles cannot
alias a later pause. Cancellation disables further pause hooks while resource
cleanup unwinds the input.

Async launch and parallel map reject execution before evaluating operands or
creating children. This makes suspension a sequential contract without
claiming that other tasks have stopped.

## Worker and prompt lifecycle

`debugger.Host` separates execution from prompt input. Both local execution
and native-worker execution use the same controller. The worker protocol adds
reverse `host_debug` events and command replies to its existing serialized
exchange. Frames and bounded previews cross that channel; inspected values
remain in the worker. An explicit breakpoint-replacement flag distinguishes
clearing the last breakpoint from leaving configuration unchanged (gob does
not preserve empty-slice identity).

The REPL waits for either execution completion or a debug exchange. Its input
pump owns debug input while suspended. Handler-level input requests carry
their own debug configuration, preserving the level's resource lifetime and
the worker's persistent heap. Quitting suppresses further pauses while ending
levels. Source mutation commands are available only after evaluation resumes
or is cancelled.

## Shared terminal frontend

`internal/debugui` owns the stopped-command registry, canonical names and
aliases, argument parsing, feature checks, command repetition, and generated
help for both debugger hosts. A feature profile advertises each backend's
supported operations and supplies descriptions for differing lifecycle and
inspection semantics. Invalid syntax and unsupported commands fail before
dispatch and do not replace the repeatable command.

The shared presentation functions format source bindings, frame headings,
breakpoint statuses, errors, stop notices, and source excerpts. Excerpts carry
an explicit exact, enclosing, or line-only precision; the frontend never
manufactures an exact location from incomplete native metadata. The
[shared command reference](../reference/debugger-commands.md) owns syntax and
observable presentation.

The REPL input pump and native scanner still own their input lifetimes.
Both host `debugui.Terminal`, which sends parsed requests to the shared
session and renders its structured responses. REPL command history spans stops
within one evaluation and resets for the next input. Ordinary REPL parsing
and host cancellation remain outside this terminal adapter.

## Shared debugger sessions

`internal/debugsession` defines a terminal-independent backend interface,
canonical requests, structured events, capability flags, source locations and
snapshots, frame records, breakpoint statuses, and inspection results.
`Session.Execute` validates capabilities and lifecycle state before dispatch;
`Session.Accept` consumes backend events. The API has no input reader, output
writer, or dependency on an execution engine. Structural previews remain
backend-produced strings; raw inspection returns a separate structured tree.

A host serializes requests and events. Native execution completes a request
synchronously through the existing Delve controller. The interpreter adapter
queues a wire command for the REPL host to return from its current debug
exchange. An inspection reports pending until the next exchange supplies its
response; resume reports running until the next stop or evaluation completion.
This preserves the evaluator's live stack and worker heap without blocking
the REPL host on a second exchange or replaying effects. Breakpoint resolution
and source-generation validation remain in the respective adapters. Source
snapshots and their hashes travel with locations; a `sources` API request
returns the backend's current source catalogue.

The shared session owns selected-frame state and public inspection handles.
A stop starts a new epoch and selects frame zero. Optional request epochs
reject stale frame operations; deferred responses carry their originating
epoch. Public handles map to backend handles only for the current pause and
are never reused within a session. Resume, cancellation, completion, and
termination discard them before further execution. Frame selection is local
to the session; inspection and next/finish receive the selected backend frame
index. Native frame selection and expansion remain unavailable capabilities.

Resume failures must provide an authoritative state event or leave the session
in an unknown state, where inspection and execution are rejected. The native
adapter queries Delve's state after an error rather than showing its old cached
stop. Quit remains available. Only `Interrupt` may run concurrently with a
request; the native launcher routes signals through it. Interpreter input
cancellation continues to use the host's context and cleanup rules.

Hosts retain resource ownership: native termination returns from the prompt
so the launcher's cleanup terminates its process group; interpreter quit
delivers its stop reply before ending handler levels. The session coordinates
state and data, not launch or transport ownership. All of these layers are
frontend-only and add no hooks to ordinary compiled programs. Shared structural
value rendering and capability parity remain in the
[unification roadmap](../roadmap-debugging.md#unified-debugger-frontend).

## Native source stepping

`build --debug` and source-file `debug` launches select the debug checker for every imported module, reusing
its source snapshots and disabled Fango inlining/specialization. The Go
emitter receives an explicit debug flag and suppresses optional callback,
copy-elision, and call-inlining rewrites. Semantic lowering and self tail-call
loops remain. Checked objects and emitted records use separate debug cache
namespaces, and debug generated files and binaries live in the build tree's
`debug/` subdirectory. Debug emission validity includes exact source text,
because source-site metadata is excluded from semantic fingerprints.

Debug emission attributes statement contexts, including literal returns and
local bindings, from Core debug sites. It pairs emitted and reparsed Go AST
nodes as ordinary stack-trace emission does, but resets every unclaimed line
to `<fango-generated>:1`. This prevents braces and function prologues from
inheriting fictitious Fango lines. Mapping failures are errors in the debug
profile. Ordinary emission keeps its existing annotations and optimization
passes; no target-side stepping hooks or debugger dependency are emitted.

`internal/nativedebug` reads linked DWARF statement entries and writes the
binary-adjacent manifest with source snapshots, executable lines, binding
scopes with expression spans, representation layouts, a format version, and
the binary digest. The frontend validates that identity before
launching Delve over a private Unix socket. Source excerpts come from the
snapshots. Delve resolves file/line locations to relocated addresses, including
multiple generic instantiations.

`step` installs temporary breakpoints at Fango statement addresses and
continues the real process, including calls through Go helpers into Fango
callbacks. `next` and `finish` use Delve's native next and step-out operations;
they do not install statement breakpoints throughout skipped callees. Generated
stops are filtered until a Fango location is reached. Once the original
activation returns into generated code, a source step seeks the next Fango
location. No replay or target function injection is involved.

The selected goroutine restricts temporary stops; explicit user breakpoints
retain precedence across goroutines. Next accepts the current activation or
its caller; finish waits until the activation disappears. Activation identity
combines function identity with Delve's stack-relative frame offset, so
recursion and goroutine stack movement do not reduce to a depth comparison.
Next also keeps the starting PC armed for that goroutine and frame offset:
this preserves same-line tail-loop iteration stops, which native line-based
next would otherwise skip. An existing user breakpoint at that address is
preserved instead of replaced.

Temporary breakpoints are cleared on success, failure, and interruption;
Delve native stepping is canceled before returning to the prompt, including
a breakpoint or interruption leaving its operation pending. Existing user
breakpoints are never owned by the stepping command. Cleanup
failure prevents subsequent execution. Interrupts halt an outstanding
execution RPC and also cancel a step during breakpoint installation. The
frontend owns and terminates its Delve child and debuggee. It bounds stack
inspection. [Native commands](../reference/native-debugging.md)
own the user-visible behavior and limitations.

## Native binding inspection

Debug emission associates Core lexical scopes with the same Go AST nodes
that claim source lines. Each recorded binding includes its original name,
source type, and the exact generated identifier from the emitter's mangler.
The generated Go carries deterministic JSON comments containing those scopes,
their source byte spans, and nominal representation layouts. Layouts reuse the
emitter's actual constructor tags and field selectors, keyed by qualified Go
type name; generic instantiations share a layout. Reading both fresh and
cached output supplies the companion manifest. This
avoids a second cache artifact and emits no target-side metadata tables.
Ordinary emission does not collect scopes or layouts or add these comments.

The frontend resolves bindings only from the scopes recorded for the current
Fango file and line. Go's line table does not distinguish expression columns;
when scopes on one line disagree about a name, lookup reports it unavailable.
It never selects a binding by stripping a prefix or guessing between scopes.
The native argument list classifies source arguments without loading synthetic
values. Missing bindings remain visible as unavailable. A raw view retains
Go inspection.

The native structural renderer starts with compiler-recorded identifiers and
uses read-only selectors, pointer dereferences, and Delve's dynamic interface
data projection to load shallow fields on demand. It decodes the active tag
for tagged unions, the concrete constructor of interface unions, records,
products, and tuples from the layout metadata. The runtime List spine is
walked iteratively, so its length does not consume structural nesting depth.
Generic values use the concrete dynamic representation. Native resources are
opaque. Inspection never invokes target functions, including `Show` or List
accessors. Depth, item, node, and RPC budgets bound inspection; visited pointer
addresses detect cycles. [Inspection limits](../reference/native-debugging.md#inspection-and-limits)
own the exact bounds and presentation.

Source excerpts use the shared presentation layer with binary-matched snapshots.
Since the linked line table cannot disambiguate columns, the frontend checks
all spans recorded for the stopped file and line. Identical spans can be
underlined directly. A recorded span enclosing all the others is displayed
with an explicit enclosing-expression label. Disjoint, invalid, or missing
spans produce a line-only excerpt with an unavailable notice. No arbitrary
subexpression is selected from ambiguous metadata.

Source-file launches call the same build pipeline as `build --debug`, write
metadata beside the cached executable, and launch only after a successful
build. They do not copy a binary to the working directory. Existing-binary
launches continue to validate the companion version and binary digest.

## Native experiment harness

`make test-debug-native`, inside `nix develop`, compiles
`testdata/debug-native/Main.fango` through `build --debug`, verifies its result,
reads DWARF, and exercises the source controller through live Delve JSON-RPC.
It checks source stepping into stdlib, next, finish, callback breakpoint
precedence, native sidecars, self tail-loop activation identity, and completion.
The fixture also includes matching, captures, an effect handler, nested
records and ADTs, Lists, tuples, and generic value inspection. It verifies
Fango rendering, long-List truncation, raw inspection, and source underlines.

The target is opt-in and requires host permission to debug a child process.
`FANGO_DEBUG_NATIVE_DWARF_ONLY=1` runs the build, execution, and static
debug-table inspection without live debugging. Deterministic controller tests
cover interruptions, recursion, exit, and temporary-breakpoint cleanup without
host debugger access. Native presentation and platform coverage still have
separate [roadmap criteria](../roadmap-debugging.md).
