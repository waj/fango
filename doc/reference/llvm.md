# Experimental LLVM backend

[Reference index](../reference.md). [Architecture](../design/llvm.md).

## Commands and support

The Go backend is the default. Select LLVM explicitly:

```sh
nix develop
go build -o fango ./cmd/fango
./fango check --backend llvm main.fango
./fango build --backend llvm -o app main.fango
./fango run --backend llvm main.fango -- argument
```

The experiment targets macOS ARM64 and requires Clang/LLVM 21, BDWGC, zlib,
and pkg-config from the development shell. Other hosts receive
`UNSUPPORTED LLVM TARGET`. Executables link the native collector and zlib;
their libraries must remain available when they run.

Synchronous language and bundled libraries use the same source contracts.
Reachable Async operations receive `UNSUPPORTED LLVM FEATURE: Async`; importing
a module with unused Async APIs is allowed. The concurrent HTTP server requires
the Go backend. GZip uses zlib, so compressed bytes and chunk boundaries can differ
while the decoded response is equivalent.

`check --backend llvm` performs shared semantic checks, checks reachable LLVM
features when an entry exists, and compiles every selected C sidecar to validate
its ABI and required definitions. It does not link or execute a program.
`--emit-go` is incompatible with LLVM. Unknown backend names are usage errors.
`-v`, `-vv`, `-timings json`, and `-no-cache` are supported. `FANGO_BUILD_DIR`
redirects LLVM output as well as Go output; `clean` removes both backends'
source-local artifacts.

## C sidecars

LLVM selects `Module.native.c` beside the Fango source. A `.native.go` file is
unnecessary for LLVM; keep both files to run a module on both backends. Fango
native annotations and wrapper/storage rules are unchanged. Generated headers are
forced into each translation unit, so Clang rejects an ABI mismatch.
`MISSING NATIVE SIDECAR` identifies a missing C file; `MISSING C NATIVE FUNCTION`
identifies a declaration without a definition during `check`.

```c
//go:build ignore

#include "fango_native.h"
int64_t FANGO_NATIVE(Double)(int64_t value) {
    return value * 2;
}
```

`FANGO_NATIVE(Name)` supplies the module-specific linker symbol. Use the exported
form of the Fango declaration name, just as with a Go sidecar. Preserve the exact
Go build constraint when formatting C: it excludes the C source from packages
containing parallel Go sidecars.

| Fango boundary | C representation |
| --- | --- |
| Int | int64_t |
| Float | double |
| Char | uint32_t, a Unicode scalar |
| Bool | bool |
| String | fango_string, UTF-8 data and byte length |
| Unit argument/result | omitted argument / void result |
| Runtime.Native.Any or native storage token | fango_opaque |
| Bundled Bytes boundary | fango_bytes, data and byte length |
| Local one-field wrapper | its field's boundary representation |

`fango.h` supplies scanned `fango_alloc`, atomic buffer allocation, string/byte
copying helpers, and fallback finalizer registration. Returned buffers and handles
must outlive the call; stack addresses are invalid. Keep pointers to Fango values
in scanned GC storage. The compiler validates returned String UTF-8 and Char
scalars. Bundled fallible File/Net calls return `fango_native_error` and write
successful non-Unit payloads through an output pointer. User sidecars retain the
existing scalar, wrapper, and indexed storage restrictions.
