# Go backend and native runtime

Runtime representations, deterministic module emission, tail loops, build caching, and native boundaries.

[Design index](../design.md). Source and checks: [Codegen](../../internal/codegen/gen.go), [Build driver](../../internal/build/build.go), [Native validation](../../internal/modules/modules.go), [Tail predicate](../../internal/core/tailcall.go), [List runtime](../../runtime/fangort/list.go), [Module stability tests](../../cmd/fango/modules_test.go), [Native worker](../../internal/nativehost).

## Representations and ABI

Code generation builds formatted Go AST/source and invokes supported `go build`.
It does not depend on Go runtime internals, stack maps, barriers, or scheduler ABI.

| Fango | Go representation |
| --- | --- |
| Int, Float | int64, float64 |
| String, Char | Valid UTF-8 string, Unicode-scalar rune |
| Bool | bool; treated as True/False ADT by checking |
| Unit | Shared fangort.Unit/UnitValue when represented |
| Function | Typed Direct/Exit/Machine callable record |
| Ordinary ADT | Typed marker interface and constructor structs |
| Runtime.Native.Any | Go `any`; opaque and valid only behind a private wrapper |
| Parameterized definition | Go generics with explicit instantiation |

Row-kinded ADT parameters are omitted from Go generics; their Core arguments
erase to Unit. Class dictionaries and factories use the ordinary typed internal
ABI. [Effect transport](effects.md#direct-exit-and-machine) and
[Core](core.md) own family/evidence contracts.

The Machine callable member returns a lazy
[MachineStart](machines.md#dispatch-and-frame-lifetime) description. Callable
records also carry a private pause-owner tag, set only for a scoped pause
capability. Module-owned Machine families export both frame constructors and
start factories; the latter may omit proven forwarding frames. These are
generated-code ABI details, not additional source calling conventions.

Closed nominal type descriptors are immutable package values shared by all
invocations in that generated module. Descriptors containing type parameters
remain invocation-dependent; they reuse the closed descriptors of their
concrete arguments. Descriptor equality and failure inspection retain the
same nominal and inspectability checks.

Concrete Unit parameters/results erase at direct worker and operation boundaries,
but remain values at first-class, polymorphic, ADT, and handler-closure boundaries.
Erasing an argument never erases its evaluation: preserve left-to-right effects
and materialize Unit only where a value is required. A handler return closure
returns Unit even if its enclosing worker uses a void result.

Expression-position If/Case/Seq use typed immediately invoked closures. In Exit
mode each closure returns Outcome at its own expression type, not at the outer
worker's result type. Normal leaves wrap at that same type. Propagation never
drops an exit or turns a language type check into a failed host assertion.

## List representation

The bundled List is recognized once by canonical symbol and validated shape;
subsequent passes compare nominal identity. Checking, deriving, reflection, Core,
and lint still treat it as an ordinary parameterized ADT. User cons types keep
normal lowering. The backends share fangort.List: a spine of fixed-size inline
arrays filled downward behind a two-word value.

List emits no marker interface/constructor structs. Construction is a runtime
call and matching uses emptiness/head/tail instead of a type switch. Its own
fields cannot be controlled; family differences are carried by its element type.
Eq/show retain the exported names and generic signatures of ordinary lowering
but delegate to runtime support, keeping element-operation synthesis independent
of storage.

A chunk watermark only decreases. Cons may claim the slot below it only when
the extended list owns the frontier; every existing value's offset is at or above
the watermark at creation. New cons is therefore invisible to all prior values,
without copying, and has worst-case constant cost even under branching. The
interpreter uses the same runtime representation. Public complexity belongs in
[collections](../reference/library-collections.md#list).

Dict is an ordinary opaque Fango [weight-balanced tree](../../stdlib/Dict.fango)
with cached subtree sizes.
Its balance invariant belongs to its module; it needs no compiler representation.

## Bytes representation

The bundled Bytes is recognized the same way List is: once by canonical symbol,
at its declaration, with its shape validated rather than assumed. The backends
share fangort.Bytes, an immutable Go `[]byte`.

`type Bytes = Bytes` declares one nullary constructor, which is the empty
sequence. That is what the type needs to be nameable without a grammar change
and without a second compiler-known primitive: a nullary constructor binds
nothing, so a match on it — possible only inside the module, since the
constructor is not exposed — discriminates nothing and observes nothing the
representation hides. Bytes emits no marker interface or constructor struct,
takes no type arguments, and its eq/show keep the exported names ordinary
lowering gives them while delegating to runtime support, exactly as List's do.

The invariant the layer rests on is that a Bytes never aliases storage anything
will write again. Slicing therefore shares its backing array, capped so nothing
can be appended into what follows it, and every operation that builds a value
allocates its own array. A native filling a scratch buffer owes the same copy
on the way out; that is the one rule a reviewer of native code checks, and it
is why fangort.Bytes is an alias rather than a defined type — a sidecar reading
a file or a socket hands the boundary an ordinary `[]byte`.

A `List Int` would cost eight bytes per byte and forfeit `bytes.Index`, and an
opaque handle table is not used: `Runtime.Native.Any` lets a scoped wrapper retain the
Go object directly, and ordinary Go reachability collects it. Relaxing String
to admit invalid UTF-8 would instead invalidate every String contract and the
boundary validation protecting the Go side. Scanning is therefore a native
over Go's `bytes.Index`, so a parser searching a block never crosses the
language boundary once per byte.

Operations crossing a list come in pairs over one implementation, because
generated code holds a `List[int64]` or a `List[Bytes]` where the interpreter
holds the same list with its elements erased. Public contracts belong in
[byte sequences](../reference/library-bytes.md).

## Module emission and build cache

One generated Go module contains a package main per entry program beneath
entries/, dependencies beneath modules/, shared fangort, and native sidecar
packages. The build directory belongs to a source directory rather than to one
program, as the compilation cache does, so the programs in a directory share
the modules they have in common and keep their own entry package and binary
rather than overwriting each other's. An entry's package is named after its
file's stem; a stem the Go tool refuses as a path component — a space, a
trailing tilde and digits, one of the device names it rejects on every host —
is spelled with a sanitized name and a digest of the stem, because a program
that compiles today has to keep compiling. Cross-package workers,
types, constructors, dictionaries, and effect evidence use a typed exported
internal ABI. Direct source imports remain Go edges even if unused; generated
types may add transitive type-owner imports. An import gets a named Go alias
only when a selector survives final representation lowering; erased references
keep a blank import for initialization without triggering Go's unused-import
check.

Aliases and batch lifted names are deterministic and independent of numeric
identity allocation. Symbol mangling spells separators/operator characters as
words (for example `Basics.++` becomes `v_Basics_dot__plus__plus_`); disjoint
identifier/operator alphabets and the ban on leading underscores prevent
collisions. A dependency's generated package cannot depend on its consumers.
Project emission is the single generation path used by CLI and tests.

Emission does not inspect dependency bodies to rediscover calling conventions.
Owner-scoped Core elaboration records an ABI summary on every definition:
whether its type needs a Direct/Exit/Machine representation family, whether it
actually invokes a controlled callback parameter, and whether it is a passive
Machine factory. Recursive classification may inspect bodies owned by the
current module and consults only these summaries for installed dependencies.

Each owner is lowered and emitted alone. Its unit program holds its own Core
plus the installed declarations it links against. Ordinary dependency bodies
are withheld; the only exposed bodies are the bounded
[execution templates](core.md#adapters-and-specialization), explicitly included
in the dependency ABI fingerprint. The backend therefore cannot depend on an
implementation its key does not name. Machine
selection follows a definition's own control rather than its call sites, so a
consumer and the dependency's own module agree on which families exist without
consulting each other; a family a consumer calls but does not own is a
declaration carrying the parameter, evidence, row, and result contract, and its
blocks are validated only by its owner. The whole-program lowering and emission
path remains as the differential reference the module backend is compared
against, and must stay byte-identical to it.

Emission is cached per owner, and the artifact records what the owner's bytes
were built from together with the digest of what came out. The bytes themselves
are not in it: the build tree already holds exactly one copy of them, at the
path the record names, and that is the copy the Go toolchain compiles. So a
hit is both halves — a record that matches the graph, and a file that still
hashes to what that record produced — and a generated file edited by hand, or
absent because the tree is a fresh `--emit-go` destination, is an ordinary
miss. What a record names is the owner's own runtime Core and declarations, the
semantic and ABI
contracts of every module in its transitive dependency closure, the generated
path it occupies, its role, and, for the entry package, the entry symbol,
active intrinsics, and the sidecar packages it links whether or not it calls
them. The closure is the boundary because everything an owner's bytes can
depend on lies inside it: a type owner reached only through another module is
itself a transitive dependency, so it cannot change unnoticed, while a module
elsewhere in the program that the owner cannot reach cannot affect its bytes
and does not invalidate them. Two programs that share a module therefore share
its generated bytes. Recording the closure is what makes that possible — a key
computed before emission would have to name every installed module, because
which ones the generated code ends up referencing is not known until it exists.
A dependency body edit that preserves those contracts and exported execution
templates rebuilds its owner and leaves runtime-only importers on their generated
bytes. Changing an exported template invalidates reachable importer emission,
including when its source signature is unchanged. A missing or damaged
emission artifact, or a generated file that is not the one it describes,
reuses the checked module objects and runs only the owner's backend work,
which must reproduce the bytes checking from source produces. An
owner whose closure is not fully summarized has no record to compare and is
emitted every build; it reports itself as such, so the reuse counts stay counts
over every owner rather than over the ones a lookup was attempted for.
Sidecar bytes
are materialized separately from current sources, so a sidecar body edit
revalidates its owner and rewrites its native package without changing any
generated module.

Caching emission is worth its cost because machine lowering, not Go emission,
is what an owner's back end costs: reusing it turns the whole back end of a
warm build into one lookup and one digest per owner, and a build that reuses
its checked objects but re-emits is several times a fully warm one. Recording
only what the bytes were and where they are, rather than the bytes, is what
keeps that cheap: the generated Go is on disk once, and a warm build reads it
once.

The driver writes only changed files beneath persistent .fango/build and removes
only stale generated paths recorded in its manifest. The manifest is in two
parts because the two are pruned by different rules. The private module's fixed
sources — its go.mod and the fangort package read from the library root — are
regenerated in full by every build, so each build replaces that list and a
runtime source the library root stops shipping leaves with it; refcounting them
per program would keep a dropped one alive behind a program that has not
rebuilt, and that file is in the package every generated module imports. What
each program generated is listed under that program, and a build prunes only
what its own program listed before and no sibling lists now, so the modules two
programs share survive either one's build. A generated module no program
reaches is inert — `go build` never compiles a package nothing imports — so
leaving one costs storage and nothing else.

Each program's sources.json, beside its entry package, lists logical names,
root-relative or `<stdlib>/` paths, and SHA-256 hashes in deterministic
dependency order: the record of what that build was made from. It does not
decide whether to link. That is decided from a stamp beside the binary, of the
toolchain and the files it compiles, because a program cannot tell from its own
writes whether it needs relinking — a sibling's build can update a shared
module and leave this program's binary stale while this program writes nothing.
Stamping what is compiled also means a source edit that produces the same Go
produces the same binary and does not relink. Go's cache reuses unchanged
packages. Build copies the executable; run reuses it while inputs are
unchanged. [Commands](../reference/commands.md) owns output paths and
managed-directory safeguards.

The source project keeps its artifacts beneath
`.fango/cache/v1/<compiler fingerprint>/<kind>/<group>/<name>.json`, one
namespace per artifact kind — checked module objects and emission records —
holding one slot per module. A slot is the module's own identity, never a digest of
its inputs, so a rebuild replaces an artifact rather than adding one beside it
and storage is bounded by the modules a project has rather than by its edit
history. The group separates entries from named modules, because a headerless
entry answers to its file stem and would otherwise collide with the module of
that name it imports. Each artifact is framed with its kind, payload schema,
and payload digest on a single header line, followed by the payload bytes
themselves, and each is written by atomic rename and validated before use — so
a reader sees one complete artifact and the record that describes it, never a
record paired with bytes it did not ship with. The store does not sync an
artifact to disk, per object or per build: the rename orders it against
concurrent readers, and the digest is what makes a crash harmless, since a
file a lost write leaves empty, zeroed, or truncated fails validation and is
an ordinary miss, and a rename that did not survive leaves the previous
complete artifact, which its record then accepts or rejects. Nothing in the
cache is anything a build cannot reproduce, so durability would buy only the
cost of rebuilding what was lost. Framing the payload as opaque
bytes is what lets a reader find and verify it without parsing it, and what
makes the digest cover the bytes as stored rather than a re-encoded copy of
them. Every payload leads with the record of what its artifact was built from,
which is what a lookup compares against the graph it has; behind it, an emitted
unit carries the digest of the Go it produced, and a checked module object its
[object encoding](pipeline.md#pipeline), whose stage Core is a section of its
own that a compile reads only when it needs it. There is no
whole-project artifact: every invocation discovers and validates the current
graph, and one module-artifact pipeline decides what is still valid, so no
command can be served a stale program by a shortcut that outranks its modules.

The compiler fingerprint is the SHA-256 of the running executable, computed
once per process; both its value and any computation failure are stable for
that process, so a failed fingerprint disables cache use. Compiler or schema
changes select a cold namespace, and artifacts an older compiler wrote are
simply never read; `clean` removes them along with everything else. Reads do
not create directories. If source-local storage cannot be written, writes use a
user-cache namespace keyed by the absolute source root; cache failures never
become diagnostics. `FANGO_BUILD_DIR` affects generated build output only and
never selects the compilation cache. The low-level byte store has no dependency
on modules, inference, or code generation; each layer above owns its own codec
and validation.

## Self tail-call loops

Both backends use the pure Core eligibility predicate. It requires a saturated
self-worker call at identity type instantiation with unchanged evidence, reached
through the tail skeleton (Let body, If branches, Case leaves, Seq tail), never
through a lambda, handler, RHS, scrutinee, guard, or argument.

A definition is excluded if a lambda or handler clause/return captures a parameter
the loop mutates; Go closures capture locals by reference. Unchanged threaded
parameters may be captured. The reference owns the
[public guarantee](../reference/functions.md#tail-call-guarantee).

Go emits one for loop and simultaneous reassignment of changed arguments plus
continue. A dedicated return-position walker keeps continue out of nested
function literals. Seq tails do not introduce IIFEs; erased Unit arguments use
the ordinary ordered temporary prelude. Unchanged arguments, including dictionaries,
are omitted from jumps; a fully unchanged call is bare continue. No unreachable
return is needed after an infinite loop. The interpreter uses the same predicate,
builds the next parameter frame after evaluating arguments, shares decision-tree
dispatch, caches eligibility per definition pointer, and polls cancellation on
every loop iteration.

## Native boundaries

Primitives are declared in bundled Fango modules rather than a compiler catalog.
Inline native templates are bundled-only and retained for scalar primitives and
compiler-only representations. Validation requires a Go expression with each
positional placeholder exactly once, using only placeholders, compiler intrinsics,
predeclared names, and fangort. Emission reparses, scrubs positions, and substitutes
typed AST expressions with precedence intact.

Bundled and user call-form sidecars follow the same declaration correspondence,
standard-library import restriction, and Unit-erased ABI over boundary values.
Go compilation checks function bodies and types. Native effect operations
supply a default only when no Fango handler handles them. Each sidecar's hash
enters sources.json and edits or removals invalidate or prune generated
packages.

Every materialized sidecar gets FangoHost, a reserved process-global interface
for input/output, arguments, directory, and exit, without hidden call parameters.
Scoped background retention uses [native request tokens](native-requests.md)
instead of retaining this global host.
Its single source declaration is copied beside each sidecar with rewritten runtime
imports. Module-specific logic remains in its owner: IO owns console behavior,
File owns file/directory objects, Net owns sockets, and Random supplies system entropy; deterministic
PRNG transitions and state remain Fango handler code.

Boundary shapes are resolved once after constructor declaration and stored in
native metadata, never re-derived by backends:

- A same-module single-constructor/single-boundary-value wrapper is projected before a
  call and reconstructed after it. The loader recognizes its declared shape;
  checking confirms resolved types. Interpreter CtorVal wrapping matches Go.
  Phantom indices and opaque payload tokens follow the
  [native storage contract](shared-capabilities.md#native-storage-and-sharing).
- The bundled `Runtime.Native.Any` is represented as Go `any`. Its constructor is
  private and carries no usable value; libraries expose only nominal wrappers
  such as `File.Handle` and `Net.Connection`. It has no Eq, Show, matching, or
  serialization contract. Resolved-type validation prevents an imported type
  merely named `Any` from acquiring this ABI.
- The bundled Bytes crosses as a plain `[]byte`, admitted in bundled sidecars
  only and recognized by the representation the checker assigned at the
  declaration rather than by name, so a user type of that name keeps its own
  boundary. It is erased to nothing: nothing is projected, rebuilt, or
  validated, because Bytes has no well-formedness contract. A sidecar spells it
  `[]byte` because sidecars cannot import fangort, and owes the copy-out every
  Bytes producer owes.
- Bundled File and Net value natives additionally map Go `(T, error)` to their
  declared Result error. fangort supplies the shared classifiers; checked Kind
  constructor order is their ABI. Go emits the Result construction at the call
  site. User sidecars do not get this fallible shape.

File and socket wrappers carry pointers to their native objects directly; there
is no native ID table and no release primitive. Cleanup scopes close objects,
and Go GC reclaims unreachable closed wrappers. File errors retain the caller's
path rather than host-expanded absolute paths. Resource/lifetime checks prevent
accepted Fango from using stale handles; native code is trusted.

## Interpreter native worker

Evaluation involving sidecars uses one persistent process per sidecar set.
Checking and elaboration remain in the compiler process; checked Core and its
selective Machine program are serialized to the worker, where the evaluator and
sidecars share one Go heap. A `Runtime.Native.Any` therefore passes from Core to a Go
function as the original object and never crosses IPC. Support sources come
from the library root, and cache keys hash their sorted paths and exact bytes.

A framed protocol carries serialized executable Core, scalar results, and
reverse host requests over a dedicated loopback connection. The object codec
drops source spans but preserves shared identities needed by checked Core and
Machine closures. Process stdio stays outside the control channel. The active
interpreter host answers requests, sharing one buffered reader with the prompt.
Globals persist across calls, but importing new sidecars rebuilds the worker.
Panics are reported and reproduced; host exit becomes an interpreter exit error.
This is lifecycle isolation, not a security sandbox.

The in-process native registry is limited to templates, compiler representations,
and explicitly stage-safe bundled behavior. File registry entries validate arity
but refuse execution; actual file calls use the worker. Staging rejects user
sidecars, residual effects, and process-observing natives.
