"""Opt-in feasibility check for erased stream storage; run with Nix python3.

The adapters use the current generated Stream.fold, not an implementation of
existential syntax. A failure rejects these adapters, not existential types in
general. No tracked files or recorded performance thresholds are modified.
"""

import argparse
import hashlib
import json
import os
from pathlib import Path
import random
import shutil
import statistics
import subprocess
import time


CASES = ("scalar", "pair", "list", "mapped", "rejected", "choice", "taken", "zipped", "cursor")
VARIANTS = ("explicit", "checked", "unsafe", "opened")


def interval(ratios):
    """Paired bootstrap interval for the median ratio, with a fixed seed."""
    rng = random.Random(81321)
    boot = sorted(
        statistics.median(rng.choices(ratios, k=len(ratios)))
        for _ in range(10000)
    )
    return statistics.median(ratios), boot[250], boot[9749]


def summarize(samples, variant):
    baseline = [s for s in samples if s["variant"] == "explicit"]
    candidate = [s for s in samples if s["variant"] == variant]
    assert len(baseline) == len(candidate)
    ratios = []
    for a, b in zip(baseline, candidate):
        assert a["sample"] == b["sample"]
        ratios.append((b["NS"] / b["Reps"]) / (a["NS"] / a["Reps"]))
    median, lower, upper = interval(ratios)
    result = dict(variant=variant, ratio=median, lower95=lower, upper95=upper)
    result["verdict"] = "pass" if upper <= 1.05 else "fail" if lower > 1.05 else "inconclusive"
    for label, rows in (("explicit", baseline), (variant, candidate)):
        result[label] = {
            key: statistics.median(s[key] / s["Reps"] for s in rows)
            for key in ("NS", "Bytes", "Allocs")
        }
    return result


def selection(summaries, variants=("explicit", "checked", "unsafe")):
    """A candidate must pass every workload; prefer checked pointer recovery."""
    verdicts = {}
    for variant in variants[1:]:
        values = [r["verdict"] for r in summaries if r["variant"] == variant]
        verdicts[variant] = "fail" if "fail" in values else "inconclusive" if not values or "inconclusive" in values else "pass"
    chosen = next((v for v in variants[1:] if verdicts[v] == "pass"), None)
    overall = "pass" if chosen else "fail" if all(v == "fail" for v in verdicts.values()) else "inconclusive"
    return dict(verdict=overall, selected=chosen, candidates=verdicts)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--out", type=Path, required=True, help="new evidence directory")
    parser.add_argument("--idle", action="store_true", help="host is idle; enable timing")
    parser.add_argument("--prepare-only", action="store_true", help="build and check without timing")
    parser.add_argument("--cases", default=",".join(CASES[:-1]))
    parser.add_argument("--sizes", default="16,10000,100000")
    parser.add_argument("--modes", default="consume,construct")
    parser.add_argument("--variants", default="explicit,checked,unsafe", help="explicit followed by candidates: checked, unsafe, opened")
    parser.add_argument("--sample-ms", type=int, default=40, help="target duration of each baseline process sample")
    args = parser.parse_args()
    cases, sizes, modes = args.cases.split(","), [int(n) for n in args.sizes.split(",")], args.modes.split(",")
    variants = tuple(args.variants.split(","))
    if len(variants) < 2 or variants[0] != "explicit" or len(set(variants)) != len(variants) or not set(variants) <= set(VARIANTS):
        parser.error("variants must start with explicit and contain distinct known candidates")
    if args.sample_ms < 1:
        parser.error("sample-ms must be positive")
    if "cursor" in cases and set(variants) != {"explicit", "opened"}:
        parser.error("cursor comparison supports explicit,opened only")
    if not set(cases) <= set(CASES) or not set(modes) <= {"consume", "construct"} or any(n < 0 for n in sizes):
        parser.error("invalid cases, sizes, or modes")
    if not args.prepare_only and not args.idle:
        parser.error("timing requires --idle; use --prepare-only otherwise")
    out = args.out.resolve()
    out.mkdir(parents=True, exist_ok=False)
    here = Path(__file__).resolve().parent
    repo = here.parent.parent
    env = dict(os.environ, FANGO_ROOT=str(repo), GOMAXPROCS="1", GOGC="100")
    env.setdefault("GOCACHE", str(out / "gocache"))
    env.setdefault("GOMODCACHE", str(out / "gomodcache"))

    def command(argv, cwd=repo, extra=None):
        result = subprocess.run(argv, cwd=cwd, env={**env, **(extra or {})}, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
        with (out / "commands.log").open("a") as log:
            log.write(f"{argv!r}\n{result.stdout}\n")
        if result.returncode:
            raise RuntimeError(f"{argv!r} exited {result.returncode}:\n{result.stdout}")
        return result.stdout

    source_dir = out / "sources"
    source_dir.mkdir()
    hashes = {}
    for path in (here / "explicit_workload.fango.txt", here / "controls.go.txt", here / "opening.go.txt", Path(__file__).resolve(), here / "explicit_Stream.fango.txt", here / "explicit_Iterator.fango.txt"):
        data = path.read_bytes()
        saved_name = "workload.fango" if path.name == "explicit_workload.fango.txt" else "stdlib-Stream.fango.txt" if path.name == "Stream.fango" else path.name
        (source_dir / saved_name).write_bytes(data)
        hashes[str(path.relative_to(repo))] = hashlib.sha256(data).hexdigest()
    metadata = {
        "started": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "revision": command(["git", "rev-parse", "HEAD"]).strip(),
        "go": command(["go", "version"]).strip(),
        "hardware": command(["uname", "-a"]).strip(),
        "sources": hashes,
        "sizes": sizes, "cases": cases, "modes": modes, "variants": variants, "sample_ms": args.sample_ms,
        "GOMAXPROCS": 1, "GOGC": 100,
        "idle_asserted": args.idle,
        "kind": "generated-Fango storage ABI experiment, not existential language support",
    }
    (out / "working.patch").write_text(command(["git", "diff", "--binary", "HEAD"]))
    snapshot = out / "repo"
    snapshot.mkdir()
    source_hashes = {}
    names = command(["git", "ls-files", "-z", "--cached", "--others", "--exclude-standard"])
    for name in sorted(set(names.split("\0")) - {""}):
        original = repo / name
        if not original.is_file():
            continue
        data = original.read_bytes()
        destination = snapshot / name
        destination.parent.mkdir(parents=True, exist_ok=True)
        destination.write_bytes(data)
        source_hashes[name] = hashlib.sha256(data).hexdigest()
    for module in ("Stream", "Iterator"):
        shutil.copyfile(here / f"explicit_{module}.fango.txt", snapshot / "stdlib" / f"{module}.fango")
        source_hashes[f"stdlib/{module}.fango"] = hashlib.sha256((snapshot / "stdlib" / f"{module}.fango").read_bytes()).hexdigest()
    (out / "source-hashes.json").write_text(json.dumps(source_hashes, indent=2) + "\n")
    env["FANGO_ROOT"] = str(snapshot)
    cli, project, binary = out / "fango", out / "generated", out / "measure"
    command(["go", "build", "-o", str(cli), "./cmd/fango"], snapshot)
    command([str(cli), "build", "--emit-go", "-o", str(project), str(source_dir / "workload.fango")])
    shutil.copyfile(source_dir / "controls.go.txt", project / "entries" / "workload" / "controls_test.go")
    shutil.copyfile(source_dir / "opening.go.txt", project / "entries" / "workload" / "opening_test.go")
    command(["go", "test", "-count=1", "./entries/workload"], project)
    command(["go", "test", "-race", "-gcflags=all=-d=checkptr=2", "-count=1", "./entries/workload"], project)
    command(["go", "test", "-c", "-o", str(binary), "./entries/workload"], project)
    metadata["binary_sha256"] = hashlib.sha256(binary.read_bytes()).hexdigest()
    metadata["binary_bytes"] = binary.stat().st_size
    metadata["generated_go_bytes"] = sum(p.stat().st_size for p in project.rglob("*.go"))
    (out / "metadata.json").write_text(json.dumps(metadata, indent=2) + "\n")
    print("Checksums, allocation slope, repeat/concurrent traversals, race, and checkptr passed.", flush=True)
    if args.prepare_only:
        return

    def measure(case, variant, n, mode, reps):
        extra = {"PROBE_CASE": case, "PROBE_VARIANT": variant, "PROBE_N": str(n), "PROBE_MODE": mode, "PROBE_REPS": str(reps)}
        output = command([str(binary), "-test.run=^TestMeasure$", "-test.count=1"], project, extra)
        return json.loads(next(line[8:] for line in output.splitlines() if line.startswith("MEASURE ")))

    summaries = []
    for n in sizes:
        for case in cases:
            for mode in modes:
                pilot = measure(case, "explicit", n, mode, 20)
                reps = max(1, min(10000000, int(args.sample_ms * 1_000_000 / max(1, pilot["NS"] / 20))))
                rows = []
                for sample in range(60):
                    order = variants if sample % 2 == 0 else tuple(reversed(variants))
                    for variant in order:
                        row = dict(case=case, n=n, mode=mode, sample=sample, variant=variant, **measure(case, variant, n, mode, reps))
                        rows.append(row)
                        with (out / "observations.jsonl").open("a") as log:
                            log.write(json.dumps(row) + "\n")
                    if sample + 1 in (20, 60):
                        results = [dict(case=case, n=n, mode=mode, samples=sample+1, **summarize(rows, v)) for v in variants[1:]]
                        if sample == 59 or all(r["verdict"] != "inconclusive" for r in results):
                            break
                summaries.extend(results)
                (out / "summary.json").write_text(json.dumps(summaries, indent=2) + "\n")
                for result in results:
                    print(f'{case:8} n={n:<6} {mode:9} {result["variant"]:7} {result["ratio"]:.3f}x [{result["lower95"]:.3f}, {result["upper95"]:.3f}] {result["verdict"]}', flush=True)
    verdict = selection(summaries, variants)
    verdict["limited_to"] = "selected cases and these prototype lowerings"
    (out / "verdict.json").write_text(json.dumps(verdict, indent=2) + "\n")
    print(f'Storage experiment: {verdict["verdict"]}. Evidence: {out}', flush=True)


if __name__ == "__main__":
    main()
