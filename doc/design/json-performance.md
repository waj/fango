# Typed JSON comparison

[Verification](verification.md) owns the broader testing and timing gates.

```sh
nix develop -c go run ./benchmarks/jsoncompare -out /tmp/fango-json-evidence -profile -probes
```

The opt-in harness generates a deterministic 10 MB nested order array and
compares Fango's derived `Json.Decode` with Go's `encoding/json.Decoder`.
Both retain the complete typed result, then verify independently generated
record, ID, money, quantity, and UTF-8 byte checksums. `-bytes 500000000`
selects the larger fixture; `-input FILE` reuses a fixture and its adjacent
`.meta.json`. Output directories must be new. Run from the Nix development
shell so the generator uses its Python interpreter.
`-field-order reverse` reverses every record's keys, including nested shipping
and item records, while preserving values, byte count, and expected checksums.
The fixture metadata records its field order. This option selects generated
fixtures; reuse the resulting input with `-input` for paired comparisons.

One unmeasured warmup per implementation precedes three fresh measured
processes per implementation, run sequentially with rotating order.
`-warmups 0` omits warmups; `-runs N` selects the measured sample count.
Wall time includes startup, file IO, decoding, verification, and exit; generation
and compilation are excluded. Blocking process waits avoid polling delay.
The harness records every sample, user/system CPU, peak RSS, runtime settings,
revision, working-tree patch, new implementation sources, exported Go project,
and checksums. Each subprocess has a ten-minute timeout. These are manual
same-host measurements, not portable timing gates.

JSON now uses one resumable parser per type. The historical `-no-scan` control
depended on separate record/list streaming decoders, which have been removed;
the option reports that it is unavailable before creating evidence files.
Compare saved baseline binaries to measure the architecture change. Current
evidence records `parser: "resumable"` and `scan_enabled: true`; the older
streaming comparisons below retain their original library snapshots and results.

The optional `jsoncompare` Nix shell adds GHC and Aeson without changing the
ordinary development shell. Include `-aeson` for the Haskell workload:

```sh
nix develop .#jsoncompare -c go run ./benchmarks/jsoncompare \
  -out /tmp/fango-json-aeson-evidence -aeson -runs 7
```

The Haskell program uses Aeson's generic `FromJSON` instances, strict typed
records, `Text` strings, `Int64` integers, and `eitherDecodeStrict` over a
strict file ByteString. It forces all decoded fields with `NFData`, including
fields unused by the checksum, and retains the complete typed list through
verification using a stable pointer. UTF-8 byte checksums use `lengthWord8`
without re-encoding strings. GHC builds the workload with `-O2`; RTS defaults
are preserved and `GHCRTS` is recorded. The evidence includes compiler/library
versions, source copies, and binary hashes. This is a whole-document Aeson
comparison, including file loading and forcing; it does not isolate parsing
from typed conversion or establish streaming-parser performance. Aeson API
semantics are documented in its [reference](https://hackage-content.haskell.org/package/aeson-2.2.4.1/docs/Data-Aeson.html).

`-profile` builds a separate executable and records CPU/allocation profiles and
MemStats. Its decode boundary precedes the checksum fold, with the decoded
output still needed by that fold. A forced GC there measures retained heap;
profile timings include instrumentation and must not replace ordinary timings.
`gc_available_cpu_fraction` measures GC's share of available CPU, not elapsed
wall time. `-probes` checks that state callback invocation and typed
product/Maybe/Result construction have no allocation growth between one and
one thousand operations; fixed activation allocations are reported separately.

## Single-parser comparison

Fifteen alternating paired fresh-process samples per case compare the preceding
dual-method resumable implementation with the single-parser implementation.
The latter includes scalar phase resumption, compact integer continuation state,
and removal of a float fallback closure allocated
on successful buffered reads. One warmup precedes each case; case order rotates.
Both use the same 10,000,260-byte fixtures, default runtime settings, and verified
checksums. Builds and correctness tests finish before timing begins.

| Backend | Field order | Input chunks | Dual-method resumable | Single parser |
| --- | --- | --- | ---: | ---: |
| Go | Declaration | Normal | 0.1481 s | 0.1457 s |
| Go | Reversed | Normal | 0.1982 s | 0.1978 s |
| Go | Declaration | 64 bytes | 0.3203 s | 0.3030 s |
| Go | Reversed | 64 bytes | 0.3436 s | 0.3288 s |

All normal-chunk paired bootstrap 95% intervals include zero change. With
64-byte chunks, Go time falls by 5.4% for declaration order (interval 4.2–6.9%)
and 4.3% for reversed order (3.9–5.4%). The small-chunk
control includes the same extra Reader layer in both variants. Raw samples,
binary/input hashes, settings, and bootstrap intervals are retained in
`/tmp/fango-refill-prototype/unified-no-factory-comparison-20261002-010224.json`.

Earlier fixture controls and optimization measurements are retained in the
[comparison baselines](json-performance-baselines.md).
