#!/usr/bin/env python3
"""Opt-in, serial effect-body comparison. Run python3 in the Nix shell.

Outputs raw Go benchmark samples, generated projects, compiler binaries, and
report.json. It does not select a production strategy or rewrite baselines.
"""
import argparse
import hashlib
import json
import os
import platform
from pathlib import Path
import re
import statistics
import subprocess
import time


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--out", type=Path, required=True, help="new evidence directory")
    parser.add_argument("--runs", type=int, default=7)
    parser.add_argument("--benchtime", default="300ms")
    parser.add_argument("--bytes", type=int, default=10_000_000)
    parser.add_argument("--variants", nargs="+", choices=("baseline", "factory", "selective"),
                        default=["baseline", "selective"])
    parser.add_argument("--prepare-only", action="store_true", help="build and check without timing runtime")
    parser.add_argument("--measure-only", action="store_true", help="time already prepared binaries on an idle host")
    args = parser.parse_args()
    if args.runs < 3 or args.bytes < 1000:
        parser.error("require at least three runs and 1000 input bytes")
    if "baseline" not in args.variants or len(set(args.variants)) != len(args.variants):
        parser.error("require baseline and distinct variants")
    root = Path(__file__).resolve().parents[2]
    out = args.out.resolve()
    if args.measure_only:
        report = json.loads((out / "report.json").read_text())
    else:
        out.mkdir(parents=True, exist_ok=False)
        report = {"variants": {}, "runs": args.runs, "benchtime": args.benchtime}
    report["measurement_host"] = platform.platform()
    report["runs"], report["benchtime"] = args.runs, args.benchtime
    env = dict(os.environ, FANGO_ROOT=str(root))
    # A parent GOFLAGS must not silently replace the experiment's strategy.
    env.pop("GOFLAGS", None)

    def run(command, cwd=root, extra=None, log=None):
        start = time.perf_counter()
        result = subprocess.run(command, cwd=cwd, env=env | (extra or {}),
                                stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
        elapsed = time.perf_counter() - start
        if log:
            log.write_text(result.stdout)
        if result.returncode:
            raise RuntimeError(f"{command}: exit {result.returncode}\n{result.stdout}")
        return elapsed, result.stdout

    variants = tuple(args.variants)
    if not args.measure_only:
        _, version = run(["go", "version"])
        report["go_version"] = version.strip()
        _, revision = run(["git", "rev-parse", "HEAD"])
        report["source_revision"] = revision.strip()
        _, growth = run(["go", "test", "./cmd/fango", "-run", "^TestEffectCallbackGrowthIsBounded$", "-count=1", "-v"],
                        log=out / "growth.log")
        report["nesting"] = [
            {"variant":variant, "depth":int(depth), "bodies":int(bodies), "entry_bytes":int(size)}
            for variant, depth, bodies, size in re.findall(r"(baseline|factory|selective): depth=(\d+) bodies=(\d+) entryBytes=(\d+)", growth)
        ]
        run(["python3", str(root / "benchmarks/jsoncompare/generate.py"),
             "--bytes", str(args.bytes), "--output", str(out / "orders.json")], log=out / "input.log")
    meta = json.loads((out / "orders.meta.json").read_text())
    bench_env = {"FANGO_EFFECT_JSON": str(out / "orders.json"),
                 "FANGO_EFFECT_RECORDS": str(meta["expected"]["records"])}
    report["input"] = meta
    if not args.measure_only:
        for variant in variants:
            print(f"Preparing {variant}", flush=True)
            work = out / variant
            work.mkdir()
            compiler = work / "fango"
            run(["go", "build", "-ldflags", "-X github.com/waj/fango/internal/codegen.effectBodyStrategy=" + variant,
                 "-o", str(compiler), "./cmd/fango"], log=work / "compiler.log")
            source = work / "workload.fango"
            source.write_bytes((root / "benchmarks/effectcompare/workload.fango").read_bytes())
            project = work / "project"
            build_env = {"FANGO_BUILD_DIR": str(project)}
            cold = []
            for repeat in range(3):
                elapsed, _ = run([str(compiler), "build", "-no-cache", "--emit-go", "-o", str(project), str(source)],
                                 extra=build_env, log=work / f"emit-{repeat}.log")
                cold.append(elapsed)
            elapsed, _ = run(["go", "build", "-o", str(work / "program"), "./entries/workload"], cwd=project,
                             log=work / "go-build.log")
            metrics = {"uncached_emit_seconds": cold, "initial_go_build_seconds": elapsed,
                       "compiler_sha256": hashlib.sha256(compiler.read_bytes()).hexdigest(),
                       "entry_go_bytes": (project / "entries/workload/main.go").stat().st_size,
                       "dependency_go_bytes": sum(p.stat().st_size for p in (project / "modules").rglob("*.go")),
                       "executable_bytes": (work / "program").stat().st_size}
            build_cmd = [str(compiler), "build", "-o", str(work / "program"), str(source)]
            run(build_cmd, extra=build_env)
            metrics["warm_unchanged_seconds"] = [run(build_cmd, extra=build_env)[0] for _ in range(3)]
            original = source.read_text()
            metrics["warm_changed_seconds"] = []
            for repeat in range(3):
                source.write_text(original.replace('json "[]")', f'json "[]" + {repeat+1})'))
                metrics["warm_changed_seconds"].append(run(build_cmd, extra=build_env)[0])
            source.write_text(original)
            run(build_cmd, extra=build_env)
            (project / "entries/workload/effect_test.go").write_bytes((root / "benchmarks/effectcompare/probe_test.go.txt").read_bytes())
            run(["go", "test", "-c", "-o", str(work / "probe"), "./entries/workload"], cwd=project,
                log=work / "probe-build.log")
            run([str(work / "probe"), "-test.run=TestEffectChecks", "-test.v"], extra=bench_env,
                log=work / "checks.log")
            report["variants"][variant] = metrics
            (out / "report.json").write_text(json.dumps(report, indent=2) + "\n")
    if args.prepare_only:
        return

    pattern = re.compile(r"^Benchmark(\w+)-\d+\s+\d+\s+([\d.]+) ns/op.*?([\d.]+) B/op\s+([\d.]+) allocs/op", re.M)
    samples = {variant: {} for variant in variants}
    # One unrecorded warmup, then rotate the order each round. Compilation is
    # complete before timing; there are no concurrent benchmark processes.
    for repeat in range(-1, args.runs):
        order = variants[repeat % len(variants):] + variants[:repeat % len(variants)]
        for variant in order:
            print(f"Runtime sample {repeat}: {variant}", flush=True)
            _, text = run([str(out / variant / "probe"), "-test.run=^$", "-test.bench=.",
                           "-test.benchmem", "-test.benchtime=" + args.benchtime], extra=bench_env,
                          log=out / variant / f"runtime-{repeat}.log")
            matches = pattern.findall(text)
            if len(matches) != 8:
                raise RuntimeError("missing benchmark results:\n" + text)
            if repeat >= 0:
                for name, ns, allocated, allocs in matches:
                    samples[variant].setdefault(name, []).append({"ns":float(ns), "bytes":float(allocated), "allocs":float(allocs)})
    report["samples"] = samples
    report["comparison"] = {}
    for variant in variants:
        report["comparison"][variant] = {}
        for name, values in samples[variant].items():
            times = [v["ns"] for v in values]
            baseline = statistics.median(v["ns"] for v in samples["baseline"][name])
            report["comparison"][variant][name] = {
                "median_ns":statistics.median(times), "min_ns":min(times), "max_ns":max(times),
                "median_ratio":statistics.median(times)/baseline,
                "median_bytes":statistics.median(v["bytes"] for v in values),
                "median_allocs":statistics.median(v["allocs"] for v in values),
            }
    (out / "report.json").write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps(report["comparison"], indent=2))


if __name__ == "__main__":
    main()
