# Experimental LLVM backend

[Design index](../design.md). [Commands and C boundary](../reference/llvm.md).

## Pipeline and isolation

`internal/llvmgen` consumes checked, linted Core independently of the Go emitter
and structured Go lowering. It emits typed C++20 and C ABI declarations. Clang
handles target aggregate calling conventions and instantiates generic workers,
then emits LLVM IR. `internal/llvmbuild` links runtime and C sidecar IR, verifies
it, runs `default<O3>,verify`, and links the native executable. Floating point
compilation excludes fast math; integer arithmetic explicitly preserves wrapping,
division, and remainder semantics.

The Go backend remains the default. Its emitter, lowering, and runtime are
unchanged. LLVM checked objects use a separate `llvm` cache namespace. Generated
sources, IR, bitcode, link stamps, and binaries live under the build root's
`llvm/entries/` subtree. Each absolute entry path owns one replaceable directory.
A warm build checks generated input contents, toolchain identity, and executable
digest. Linking publishes the executable only after success. `-no-cache` bypasses
checked-object and executable reuse.

## Typed representations

Scalars use native machine values. Strings and Bytes have distinct pointer and
length structures; immutable lists use typed cons cells. Products use typed
fields. Sums use a tag and a union of constructor payloads. Recursive data and
ordinary nonempty sums use GC pointers, while Maybe and Result remain inline.
Dictionary products with several function fields use pointers. Clang templates
instantiate type parameters; effect-row type parameters erase.

Workers use typed direct calls. Function values contain a GC environment pointer
and Direct/Exit entry pointers. Captured environments are trivially destructible
and scanned by BDWGC. Eligible saturated self calls become loops, evaluating all
new arguments before replacing any parameter. Top-level values initialize on
first demand and retain their result.

## Effects and allocation

Evidence is passed explicitly. Handler activations own typed operation slots,
type descriptors, and mutable state cells. Residual rows have two inline bindings
with persistent overflow layers. Lookup compares applied effect and type arguments;
new bindings take precedence. Direct calls return typed values; Exit calls return
a typed value plus a nullable tagged exit. Cleanup releases after both normal
completion and exit, preserving the primary failure and attaching later failures
in order.

Operation-local polymorphism uses opaque typed boxes and structural adapters for
nominal data, lists, and callbacks. Parameterized evidence also carries erased
operation bridges for callbacks invoked under a handler instantiated inside a
polymorphic operation. Matching-layout calls use typed slots. Boxed structural
payloads retain a normalized layout for recovery across those bridges. Failure
inspection checks exact type identity and safe shape, excluding functions and
resource-bearing types. Suppressed reports detach their handler target.

BDWGC scans object and closure storage. String/byte buffers use atomic allocations.
Native storage retains boxed tokens, never borrowed stack pointers. File, socket,
and zlib handles register fallback finalizers; scope release supplies deterministic
cleanup.

## Verification

`make test-llvm` compares runnable fixtures with the Core interpreter and their
expectations, excluding front-end diagnostic cases and reachable Async. The
existing Go differential gate supplies the third comparison. Outputs and exit
statuses are exact, except the compressed HTTP fixture compares response headers
and the CRC-validated decompressed body because zlib and Go produce different
valid deflate streams. CLI tests cover C-only sidecars, ABI errors, missing
definitions, warm builds, sidecar edits, and Async rejection.

`testdata/llvm/` holds backend-specific regressions. A polymorphic cleanup case
there also records an [unfinished Go payload issue](../roadmap.md#longer-term-candidates);
it is checked against the interpreter without changing the existing Go gate.

Performance measurements remain manual and separate from correctness. The LLVM
pipeline uses optimization throughout; this experiment carries no speedup claim.
