#!/usr/bin/env python3
"""Measure actual hidden-type compiler output against explicit typed loops.

Run from the Nix development shell. Timing requires an idle host; preparation
alone checks results, allocation growth, aliases, race, and pointer handling.
"""

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess

from compare import summarize


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--out", type=Path, required=True)
    parser.add_argument("--idle", action="store_true")
    parser.add_argument("--prepare-only", action="store_true")
    args = parser.parse_args()
    if not args.idle and not args.prepare_only:
        parser.error("timing requires --idle; use --prepare-only otherwise")
    root = args.out.resolve()
    root.mkdir(parents=True, exist_ok=False)
    here = Path(__file__).resolve().parent
    repo = here.parent.parent
    env = dict(os.environ, GOMAXPROCS="1", GOGC="100")
    env.setdefault("GOCACHE", str(root / "gocache"))
    env.setdefault("GOMODCACHE", str(root / "gomodcache"))

    def command(argv, cwd=repo):
        result = subprocess.run(argv, cwd=cwd, env=env, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
        with (root / "commands.log").open("a") as log:
            log.write(repr(argv) + "\n" + result.stdout + "\n")
        if result.returncode:
            raise RuntimeError(result.stdout)
        return result.stdout

    snapshot = root / "repo"
    snapshot.mkdir()
    names = command(["git", "ls-files", "-z", "--cached", "--others", "--exclude-standard"])
    hashes = {}
    for name in sorted(set(names.split("\0")) - {""}):
        original = repo / name
        if not original.is_file():
            continue
        data = original.read_bytes()
        saved = snapshot / name
        saved.parent.mkdir(parents=True, exist_ok=True)
        saved.write_bytes(data)
        hashes[name] = hashlib.sha256(data).hexdigest()
    (root / "source-hashes.json").write_text(json.dumps(hashes, indent=2) + "\n")
    env["FANGO_ROOT"] = str(snapshot)
    cli, project, binary = root / "fango", root / "generated", root / "bench.test"
    fixtures = snapshot / "benchmarks" / "existentialcompare"
    command(["go", "build", "-o", str(cli), "./cmd/fango"], snapshot)
    command([str(cli), "build", "--emit-go", "-o", str(project), str(fixtures / "compiler.fango")])
    shutil.copyfile(fixtures / "compiler_test.go.txt", project / "entries" / "compiler" / "allocation_test.go")
    command(["go", "test", "-race", "-gcflags=all=-d=checkptr=2", "-count=1", "./entries/compiler"], project)
    command(["go", "test", "-c", "-o", str(binary), "./entries/compiler"], project)
    metadata = dict(go=command(["go", "version"]).strip(), hardware=command(["uname", "-a"]).strip(),
                    GOMAXPROCS=1, GOGC=100, idle_asserted=args.idle, sample_ms=200,
                    binary_sha256=hashlib.sha256(binary.read_bytes()).hexdigest(),
                    kind="actual compiler output: known packages and finite choices")
    (root / "metadata.json").write_text(json.dumps(metadata, indent=2) + "\n")
    print("Checksums, allocation growth, repacking aliases, race, and checkptr passed.", flush=True)
    if args.prepare_only:
        return
    results = []
    for case, baseline, candidate in [("known", "KnownExplicit", "KnownHidden"),
                                      ("factory", "KnownExplicit", "CallHidden"),
                                      ("choice", "ChoiceExplicit", "ChoiceHidden")]:
        rows = []
        for sample in range(60):
            variants = [("explicit", baseline), ("hidden", candidate)]
            if sample % 2:
                variants.reverse()
            for variant, name in variants:
                output = command([str(binary), "-test.run=^$", f"-test.bench=^Benchmark{name}$",
                                  "-test.benchtime=200ms", "-test.count=1"], project)
                match = re.search(r"Benchmark\w+\s+(\d+)\s+([\d.]+) ns/op\s+(\d+) B/op\s+(\d+) allocs/op", output)
                if not match:
                    raise ValueError(output)
                count, ns, size, allocs = map(float, match.groups())
                rows.append(dict(sample=sample, variant=variant, Reps=count, NS=ns*count,
                                 Bytes=size*count, Allocs=allocs*count))
            (root / f"{case}-samples.json").write_text(json.dumps(rows, indent=2) + "\n")
            if sample in (19, 59):
                result = dict(case=case, samples=sample+1, **summarize(rows, "hidden"))
                (root / f"{case}-summary.json").write_text(json.dumps(result, indent=2) + "\n")
                print(json.dumps(result), flush=True)
                if result["verdict"] != "inconclusive":
                    break
        results.append(result)
    (root / "summary.json").write_text(json.dumps(results, indent=2) + "\n")
    if any(result["verdict"] != "pass" for result in results):
        raise SystemExit("Compiler controls did not pass every case.")


if __name__ == "__main__":
    main()
