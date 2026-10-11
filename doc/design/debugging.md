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

## Native source stepping

`build --debug` selects the debug checker for every imported module, reusing
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
binary-adjacent manifest with source snapshots, executable lines, a format
version, and the binary digest. The frontend validates that identity before
launching Delve over a private Unix socket. Source excerpts come from the
snapshots. Delve resolves file/line locations to relocated addresses, including
multiple generic instantiations.

Source stepping installs temporary breakpoints at Fango statement addresses
and continues the real process. No replay or target function injection is
involved. The selected goroutine restricts temporary stops; explicit user
breakpoints retain precedence across goroutines. Step accepts the next source
stop; next accepts the current activation or its caller; finish waits until
the activation disappears. Activation identity combines function identity
with Delve's stack-relative frame offset, so recursion and goroutine stack
movement do not reduce to a depth comparison. Tail-loop iterations reuse the
same activation. Calls through Go helpers can still stop in Fango callbacks.

Temporary breakpoints are cleared on success, failure, and interruption;
existing user breakpoints are never owned by the stepping command. Cleanup
failure prevents subsequent execution. Interrupts halt an outstanding
Continue RPC and also cancel a step during breakpoint installation. The
frontend owns and terminates its Delve child and debuggee. It bounds stack
inspection and displays raw native variables; source-name and representation
translation remains separate work. [Native commands](../reference/native-debugging.md)
own the user-visible behavior and limitations.

## Native experiment harness

`make test-debug-native`, inside `nix develop`, compiles
`testdata/debug-native/Main.fango` through `build --debug`, verifies its result,
reads DWARF, and exercises the source controller through live Delve JSON-RPC.
It checks source stepping into stdlib, next, finish, callback breakpoint
precedence, native sidecars, self tail-loop activation identity, and completion.
The fixture also includes matching, captures, and an effect handler.

The target is opt-in and requires host permission to debug a child process.
`FANGO_DEBUG_NATIVE_DWARF_ONLY=1` runs the build, execution, and static
debug-table inspection without live debugging. Deterministic controller tests
cover interruptions, recursion, exit, and temporary-breakpoint cleanup without
host debugger access. Native presentation and platform coverage still have
separate [roadmap criteria](../roadmap-debugging.md).
